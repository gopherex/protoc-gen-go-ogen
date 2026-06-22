package generator

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ogen-go/ogen/gen/ir"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// unsupportedType reports whether an ogen type involves a Go type the converter
// generator cannot bridge to protobuf: raw JSON (any), streams, or external
// types other than the known string-format ones (e.g. ogen http.MultipartFile).
func unsupportedType(t *ir.Type) bool {
	if t == nil {
		return true
	}
	if externalImports[t.Go()] != "" || isMultipart(t) {
		return false
	}
	switch {
	case t.Is(ir.KindGeneric):
		return unsupportedType(t.GenericOf)
	case t.Is(ir.KindArray), t.Is(ir.KindMap):
		return unsupportedType(t.Item)
	case t.Is(ir.KindAlias):
		return unsupportedType(t.AliasTo)
	case t.Is(ir.KindSum):
		for _, v := range t.SumOf {
			if unsupportedType(v) {
				return true
			}
		}
		return false
	case t.Is(ir.KindStruct), t.Is(ir.KindEnum):
		return false
	default:
		// Primitive externals (MultipartFile), any/stream, etc. carry a package
		// qualifier in their Go type.
		return strings.Contains(t.Go(), ".")
	}
}

const convertImport = protogen.GoImportPath("github.com/gopherex/protoc-gen-go-ogen/convert")

func (c *convGen) conv(name string) protogen.GoIdent {
	return protogen.GoIdent{GoName: name, GoImportPath: convertImport}
}

func (c *convGen) qual(id protogen.GoIdent) string { return c.gf.QualifiedGoIdent(id) }

func (c *convGen) grpcb(name string) protogen.GoIdent {
	return protogen.GoIdent{GoName: name, GoImportPath: grpcbridgeImport}
}

func isMultipart(t *ir.Type) bool { return strings.Contains(t.Go(), "MultipartFile") }

func extIdent(pkg, name string) protogen.GoIdent {
	imports := map[string]protogen.GoImportPath{
		"uuid":       "github.com/google/uuid",
		"url":        "net/url",
		"netip":      "net/netip",
		"time":       "time",
		"wrapperspb": "google.golang.org/protobuf/types/known/wrapperspb",
		"fmt":        "fmt",
	}
	return protogen.GoIdent{GoName: name, GoImportPath: imports[pkg]}
}

func protoScalarGo(kind protoreflect.Kind) string {
	switch kind {
	case protoreflect.BoolKind:
		return "bool"
	case protoreflect.StringKind:
		return "string"
	case protoreflect.BytesKind:
		return "[]byte"
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return "int32"
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return "uint32"
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return "int64"
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return "uint64"
	case protoreflect.FloatKind:
		return "float32"
	case protoreflect.DoubleKind:
		return "float64"
	default:
		return "any"
	}
}

func (c *convGen) protoElemGoType(pf *protogen.Field) string {
	switch {
	case pf.Message != nil:
		return "*" + c.qual(pf.Message.GoIdent)
	case pf.Enum != nil:
		return c.qual(pf.Enum.GoIdent)
	default:
		return protoScalarGo(pf.Desc.Kind())
	}
}

func mapValueField(pf *protogen.Field) *protogen.Field { return pf.Message.Fields[1] }

func unwrapGeneric(t *ir.Type) (*ir.Type, bool) {
	if t.Is(ir.KindGeneric) {
		return t.GenericOf, true
	}
	return t, false
}

func wktName(pf *protogen.Field) string {
	if pf.Message == nil {
		return ""
	}
	return string(pf.Message.Desc.FullName())
}

// isWKT reports whether the field is a well-known type the converter bridges
// without a recursive message converter.
func isWKT(pf *protogen.Field) bool {
	n := wktName(pf)
	return n == wktTimestamp || n == wktDuration || n == wktFieldMask || wktWrappers[n]
}

// isWKTJSON reports whether the field is a free-form JSON well-known type
// (Struct/Value/ListValue/Any) bridged to jx.Raw via protojson.
func isWKTJSON(pf *protogen.Field) bool {
	_, ok := wktJSON[wktName(pf)]
	return ok
}

var wrapperCtor = map[string]string{
	"google.protobuf.StringValue": "String",
	"google.protobuf.Int32Value":  "Int32",
	"google.protobuf.Int64Value":  "Int64",
	"google.protobuf.UInt32Value": "UInt32",
	"google.protobuf.UInt64Value": "UInt64",
	"google.protobuf.FloatValue":  "Float",
	"google.protobuf.DoubleValue": "Double",
	"google.protobuf.BoolValue":   "Bool",
	"google.protobuf.BytesValue":  "Bytes",
}

// ---- ToOgen ----

func (c *convGen) toOgenField(pf *protogen.Field, of *ir.Field) {
	if unsupportedType(of.Type) && !isWKTJSON(pf) {
		c.gf.P("// ", of.Name, ": unsupported type, skipped")
		return
	}
	dst := "dst." + of.Name
	getter := "src.Get" + pf.GoName + "()"
	core, wrapped := unwrapGeneric(of.Type)
	switch {
	case pf.Desc.IsList():
		c.toOgenList(pf, core, getter, dst, wrapped)
	case pf.Desc.IsMap():
		c.toOgenMap(pf, core, getter, dst, wrapped)
	case wrapped:
		c.toOgenSingularGeneric(pf, core, getter, dst)
	default:
		if expr, ok := c.toOgenInner(pf, of.Type, getter); ok {
			c.gf.P(dst, " = ", expr)
		}
	}
}

func (c *convGen) toOgenSingularGeneric(pf *protogen.Field, inner *ir.Type, getter, dst string) {
	emit := func(src string) {
		if iv, ok := c.toOgenInner(pf, inner, src); ok {
			c.gf.P(dst, ".SetTo(", iv, ")")
		}
	}
	if pf.Desc.HasPresence() {
		c.gf.P("if src.", pf.GoName, " != nil {")
		emit(getter)
		c.gf.P("}")
	} else {
		emit(getter)
	}
}

// elemCanError reports whether converting a single element can fail in the
// given direction (toOgen=true). WKT bridges never fail.
func (c *convGen) elemCanError(pf *protogen.Field, ot *ir.Type, toOgen bool) bool {
	if isWKT(pf) {
		return false
	}
	if isWKTJSON(pf) {
		return true
	}
	if isMultipart(ot) {
		return !toOgen // reading a multipart file can fail; wrapping cannot
	}
	if ot.Is(ir.KindStruct) {
		return true
	}
	if ot.Is(ir.KindEnum) {
		return true
	}
	if toOgen {
		return externalImports[ot.Go()] != ""
	}
	return false
}

func (c *convGen) toOgenList(pf *protogen.Field, core *ir.Type, getter, dst string, wrapped bool) {
	res := c.toOgenColl(pf, core.Item, getter, c.protoElemGoType(pf), c.goType(core.Item), false)
	if wrapped {
		c.gf.P(dst, ".SetTo(", res, ")")
	} else {
		c.gf.P(dst, " = ", res)
	}
}

func (c *convGen) toOgenMap(pf *protogen.Field, core *ir.Type, getter, dst string, wrapped bool) {
	vf := mapValueField(pf)
	res := c.toOgenColl(vf, core.Item, getter, c.protoElemGoType(vf), c.goType(core.Item), true)
	if wrapped {
		c.gf.P(dst, ".SetTo(", res, ")")
	} else {
		c.gf.P(dst, " = ", res)
	}
}

// toOgenColl emits a convert.Slice/Map (or *Err) call converting a proto
// collection to ogen and returns the result variable.
func (c *convGen) toOgenColl(pf *protogen.Field, elem *ir.Type, getter, protoElem, ogenElem string, isMap bool) string {
	param := "e"
	if isMap {
		param = "v"
	}
	tmp := c.newTmp("c")
	if c.elemCanError(pf, elem, true) {
		fn := slicemap(isMap, "SliceErr", "MapErr")
		c.gf.P(tmp, ", err := ", c.qual(c.conv(fn)), "(", getter, ", func(", param, " ", protoElem, ") (zero ", ogenElem, ", _ error) {")
		save := c.fail
		c.fail = "zero"
		expr, _ := c.toOgenInner(pf, elem, param)
		c.gf.P("return ", expr, ", nil")
		c.fail = save
		c.gf.P("})")
		c.gf.P("if err != nil {")
		c.failLine()
		c.gf.P("}")
		return tmp
	}
	c.gf.P(tmp, " := ", c.qual(c.conv(slicemap(isMap, "Slice", "Map"))), "(", getter, ", func(", param, " ", protoElem, ") ", ogenElem, " {")
	expr, _ := c.toOgenInner(pf, elem, param)
	c.gf.P("return ", expr)
	c.gf.P("})")
	return tmp
}

func slicemap(isMap bool, slice, mp string) string {
	if isMap {
		return mp
	}
	return slice
}

// toOgenInner converts a single proto value (matching pf's element kind) to the
// ogen type ot, emitting any prep statements; returns the result expression.
func (c *convGen) toOgenInner(pf *protogen.Field, ot *ir.Type, src string) (string, bool) {
	switch wktName(pf) {
	case wktTimestamp:
		return c.qual(c.conv("TimeFromProto")) + "(" + src + ")", true
	case wktDuration:
		return c.qual(c.conv("DurationFromProto")) + "(" + src + ")", true
	case wktFieldMask:
		return c.qual(c.conv("FieldMaskToString")) + "(" + src + ")", true
	}
	if wktWrappers[wktName(pf)] {
		return ot.Go() + "(" + src + ".GetValue())", true
	}
	if kind, ok := wktJSON[wktName(pf)]; ok {
		tmp := c.newTmp("j")
		c.gf.P(tmp, ", err := ", c.qual(c.conv(kind+"ToJSON")), "(", src, ")")
		c.gf.P("if err != nil {")
		c.failLine()
		c.gf.P("}")
		return tmp, true
	}
	switch {
	case isMultipart(ot):
		return c.qual(c.grpcb("BytesMultipart")) + "(" + src + ")", true
	case ot.Is(ir.KindStruct):
		tmp := c.newTmp("o")
		if pf.Message != nil && !c.isLocal(pf.Message) {
			// Cross-package message: call the package-level converter function.
			c.gf.P(tmp, ", err := ", ot.Name, "ToOgen(", src, ")")
		} else {
			c.gf.P(tmp, ", err := ", src, ".ToOgen()")
		}
		c.gf.P("if err != nil {")
		c.failLine()
		c.gf.P("}")
		// ToOgen returns *ogen.T; ogen holds nested structs by value.
		return "*" + tmp, true
	case ot.Is(ir.KindEnum) && pf.Enum != nil:
		return c.toOgenEnum(pf, ot, src), true
	case externalImports[ot.Go()] != "":
		return c.toOgenExternal(ot, src)
	case ot.Is(ir.KindPrimitive):
		return ot.Go() + "(" + src + ")", true
	case ot.Is(ir.KindAlias):
		inner, ok := c.toOgenInner(pf, ot.AliasTo, src)
		if !ok {
			return "", false
		}
		return c.qual(c.oid(ot.Name)) + "(" + inner + ")", true
	default:
		return "", false
	}
}

func (c *convGen) toOgenEnum(pf *protogen.Field, ot *ir.Type, src string) string {
	tmp := c.newTmp("en")
	c.gf.P("var ", tmp, " ", c.oid(ot.Name))
	c.gf.P("switch ", src, " {")
	for _, p := range c.enumPairs(pf, ot) {
		c.gf.P("case ", p.pb, ":")
		c.gf.P(tmp, " = ", p.ogen)
	}
	c.gf.P("default:")
	msg := fmt.Sprintf("%s: enum value %%v has no ogen %s variant", pf.Desc.FullName(), ot.Name)
	c.gf.P("return ", c.fail, ", ", c.qual(extIdent("fmt", "Errorf")), "(", strconv.Quote(msg), ", ", src, ")")
	c.gf.P("}")
	return tmp
}

func (c *convGen) toOgenExternal(ot *ir.Type, src string) (string, bool) {
	switch ot.Go() {
	case "uuid.UUID":
		tmp := c.newTmp("ext")
		c.gf.P(tmp, ", err := ", c.qual(extIdent("uuid", "Parse")), "(", src, ")")
		c.gf.P("if err != nil {")
		c.failLine()
		c.gf.P("}")
		return tmp, true
	case "netip.Addr":
		tmp := c.newTmp("ext")
		c.gf.P(tmp, ", err := ", c.qual(extIdent("netip", "ParseAddr")), "(", src, ")")
		c.gf.P("if err != nil {")
		c.failLine()
		c.gf.P("}")
		return tmp, true
	case "url.URL":
		tmp := c.newTmp("ext")
		c.gf.P(tmp, ", err := ", c.qual(extIdent("url", "Parse")), "(", src, ")")
		c.gf.P("if err != nil {")
		c.failLine()
		c.gf.P("}")
		return "*" + tmp, true
	case "time.Time":
		tmp := c.newTmp("ext")
		c.gf.P(tmp, ", err := ", c.qual(extIdent("time", "Parse")), "(", c.qual(extIdent("time", "RFC3339")), ", ", src, ")")
		c.gf.P("if err != nil {")
		c.failLine()
		c.gf.P("}")
		return tmp, true
	default:
		return "", false
	}
}

func (c *convGen) toOgenOneof(oneof *protogen.Oneof, of *ir.Field) {
	if unsupportedType(of.Type) {
		c.gf.P("// ", of.Name, ": unsupported oneof, skipped")
		return
	}
	sumType, wrap := unwrapGeneric(of.Type)
	c.gf.P("switch src.Get", oneof.GoName, "().(type) {")
	for i, pf := range oneof.Fields {
		if i >= len(sumType.SumOf) {
			break
		}
		variant := sumType.SumOf[i]
		post := capitalize(variant.NamePostfix())
		c.gf.P("case *", pf.GoIdent, ":")
		iv, ok := c.toOgenInner(pf, variant, "src.Get"+pf.GoName+"()")
		if !ok {
			continue
		}
		sv := c.newTmp("sum")
		c.gf.P(sv, " := ", c.oid("New"+post+sumType.Name), "(", iv, ")")
		if wrap {
			c.gf.P("dst.", of.Name, ".SetTo(", sv, ")")
		} else {
			c.gf.P("dst.", of.Name, " = ", sv)
		}
	}
	c.gf.P("}")
}

// toOgenOneofObject renders an OBJECT-mode oneof: each branch maps to an
// optional ogen property, set inside the matched proto oneof case.
func (c *convGen) toOgenOneofObject(oneof *protogen.Oneof, fields map[string]*ir.Field) {
	c.gf.P("switch src.Get", oneof.GoName, "().(type) {")
	for _, pf := range oneof.Fields {
		of := fields[fieldOpenAPIName(pf)]
		if of == nil {
			continue
		}
		c.gf.P("case *", pf.GoIdent, ":")
		c.toOgenOneofBranch(pf, of)
	}
	c.gf.P("}")
}

// toOgenOneofBranch sets the ogen property for one oneof branch. The branch is
// known to be the active case, so no presence guard is emitted.
func (c *convGen) toOgenOneofBranch(pf *protogen.Field, of *ir.Field) {
	if unsupportedType(of.Type) && !isWKTJSON(pf) {
		c.gf.P("// ", of.Name, ": unsupported type, skipped")
		return
	}
	dst := "dst." + of.Name
	getter := "src.Get" + pf.GoName + "()"
	core, wrapped := unwrapGeneric(of.Type)
	if wrapped {
		if iv, ok := c.toOgenInner(pf, core, getter); ok {
			c.gf.P(dst, ".SetTo(", iv, ")")
		}
		return
	}
	if expr, ok := c.toOgenInner(pf, of.Type, getter); ok {
		c.gf.P(dst, " = ", expr)
	}
}

// ---- FromOgen ----

func (c *convGen) fromOgenField(pf *protogen.Field, of *ir.Field) {
	if unsupportedType(of.Type) && !isWKTJSON(pf) {
		c.gf.P("// ", pf.GoName, ": unsupported type, skipped")
		return
	}
	dst := "dst." + pf.GoName
	src := "src." + of.Name
	core, wrapped := unwrapGeneric(of.Type)
	switch {
	case pf.Desc.IsList():
		c.fromOgenList(pf, core, src, dst, wrapped)
	case pf.Desc.IsMap():
		c.fromOgenMap(pf, core, src, dst, wrapped)
	case wrapped:
		c.fromOgenSingularGeneric(pf, core, src, dst)
	default:
		if expr, ok := c.fromOgenInner(pf, of.Type, src); ok {
			c.gf.P(dst, " = ", expr)
		}
	}
}

func (c *convGen) fromOgenSingularGeneric(pf *protogen.Field, inner *ir.Type, src, dst string) {
	v := c.newTmp("v")
	c.gf.P("if ", v, ", ok := ", src, ".Get(); ok {")
	if pe, ok := c.fromOgenInner(pf, inner, v); ok {
		if pf.Message == nil && pf.Desc.HasPresence() {
			p := c.newTmp("p")
			c.gf.P(p, " := ", pe)
			c.gf.P(dst, " = &", p)
		} else {
			c.gf.P(dst, " = ", pe)
		}
	}
	c.gf.P("}")
}

func (c *convGen) fromOgenList(pf *protogen.Field, core *ir.Type, src, dst string, wrapped bool) {
	in := src
	if wrapped {
		lv := c.newTmp("lv")
		c.gf.P("if ", lv, ", ok := ", src, ".Get(); ok {")
		in = lv
	}
	res := c.fromOgenColl(pf, core.Item, in, c.goType(core.Item), c.protoElemGoType(pf), false)
	c.gf.P(dst, " = ", res)
	if wrapped {
		c.gf.P("}")
	}
}

func (c *convGen) fromOgenMap(pf *protogen.Field, core *ir.Type, src, dst string, wrapped bool) {
	vf := mapValueField(pf)
	in := src
	if wrapped {
		mv := c.newTmp("mv")
		c.gf.P("if ", mv, ", ok := ", src, ".Get(); ok {")
		in = mv
	}
	res := c.fromOgenColl(vf, core.Item, in, c.goType(core.Item), c.protoElemGoType(vf), true)
	c.gf.P(dst, " = ", res)
	if wrapped {
		c.gf.P("}")
	}
}

// fromOgenColl emits a convert.Slice/Map (or *Err) call converting an ogen
// collection to proto and returns the result variable.
func (c *convGen) fromOgenColl(pf *protogen.Field, elem *ir.Type, in, ogenElem, protoElem string, isMap bool) string {
	param := "e"
	if isMap {
		param = "v"
	}
	tmp := c.newTmp("c")
	if c.elemCanError(pf, elem, false) {
		fn := slicemap(isMap, "SliceErr", "MapErr")
		c.gf.P(tmp, ", err := ", c.qual(c.conv(fn)), "(", in, ", func(", param, " ", ogenElem, ") (zero ", protoElem, ", _ error) {")
		save := c.fail
		c.fail = "zero"
		expr, _ := c.fromOgenInner(pf, elem, param)
		c.gf.P("return ", expr, ", nil")
		c.fail = save
		c.gf.P("})")
		c.gf.P("if err != nil {")
		c.failLine()
		c.gf.P("}")
		return tmp
	}
	c.gf.P(tmp, " := ", c.qual(c.conv(slicemap(isMap, "Slice", "Map"))), "(", in, ", func(", param, " ", ogenElem, ") ", protoElem, " {")
	expr, _ := c.fromOgenInner(pf, elem, param)
	c.gf.P("return ", expr)
	c.gf.P("})")
	return tmp
}

// fromOgenInner converts a single ogen value to its proto-typed expression.
func (c *convGen) fromOgenInner(pf *protogen.Field, ot *ir.Type, src string) (string, bool) {
	switch wktName(pf) {
	case wktTimestamp:
		return c.qual(c.conv("TimeToProto")) + "(" + src + ")", true
	case wktDuration:
		return c.qual(c.conv("DurationToProto")) + "(" + src + ")", true
	case wktFieldMask:
		return c.qual(c.conv("StringToFieldMask")) + "(" + src + ")", true
	}
	if ctor, ok := wrapperCtor[wktName(pf)]; ok {
		valKind := pf.Message.Fields[0].Desc.Kind()
		return c.qual(extIdent("wrapperspb", ctor)) + "(" + protoScalarGo(valKind) + "(" + src + "))", true
	}
	if kind, ok := wktJSON[wktName(pf)]; ok {
		tmp := c.newTmp("j")
		c.gf.P(tmp, ", err := ", c.qual(c.conv("JSONTo"+kind)), "(", src, ")")
		c.gf.P("if err != nil {")
		c.failLine()
		c.gf.P("}")
		return tmp, true
	}
	switch {
	case isMultipart(ot):
		tmp := c.newTmp("mp")
		c.gf.P(tmp, ", err := ", c.qual(c.grpcb("ReadMultipart")), "(", src, ")")
		c.gf.P("if err != nil {")
		c.failLine()
		c.gf.P("}")
		return tmp, true
	case ot.Is(ir.KindStruct):
		tmp := c.newTmp("m")
		// FromOgen takes *ogen.T; ogen holds nested structs by value, so address it.
		fn := pf.Message.GoIdent.GoName + "FromOgen"
		if !c.isLocal(pf.Message) {
			fn = ot.Name + "FromOgen"
		}
		c.gf.P(tmp, ", err := ", fn, "(&", src, ")")
		c.gf.P("if err != nil {")
		c.failLine()
		c.gf.P("}")
		return tmp, true
	case ot.Is(ir.KindEnum) && pf.Enum != nil:
		return c.fromOgenEnum(pf, ot, src), true
	case externalImports[ot.Go()] != "":
		return c.fromOgenExternal(ot, src), true
	case ot.Is(ir.KindPrimitive):
		return protoScalarGo(pf.Desc.Kind()) + "(" + src + ")", true
	case ot.Is(ir.KindAlias):
		inner, ok := c.fromOgenInner(pf, ot.AliasTo, src)
		if !ok {
			return "", false
		}
		return inner, true
	default:
		return "", false
	}
}

func (c *convGen) fromOgenEnum(pf *protogen.Field, ot *ir.Type, src string) string {
	tmp := c.newTmp("en")
	c.gf.P("var ", tmp, " ", c.qual(pf.Enum.GoIdent))
	c.gf.P("switch ", src, " {")
	for _, p := range c.enumPairs(pf, ot) {
		c.gf.P("case ", p.ogen, ":")
		c.gf.P(tmp, " = ", p.pb)
	}
	c.gf.P("default:")
	msg := fmt.Sprintf("%s: enum value %%v has no %s variant", pf.Desc.FullName(), pf.Enum.GoIdent.GoName)
	c.gf.P("return ", c.fail, ", ", c.qual(extIdent("fmt", "Errorf")), "(", strconv.Quote(msg), ", ", src, ")")
	c.gf.P("}")
	return tmp
}

func (c *convGen) fromOgenExternal(ot *ir.Type, src string) string {
	if ot.Go() == "time.Time" {
		return src + ".Format(" + c.qual(extIdent("time", "RFC3339")) + ")"
	}
	return src + ".String()"
}

func (c *convGen) fromOgenOneof(oneof *protogen.Oneof, of *ir.Field) {
	if unsupportedType(of.Type) {
		c.gf.P("// ", oneof.GoName, ": unsupported oneof, skipped")
		return
	}
	sumType, wrap := unwrapGeneric(of.Type)
	emit := func(s string) {
		c.gf.P("switch ", s, ".Type {")
		for i, pf := range oneof.Fields {
			if i >= len(sumType.SumOf) {
				break
			}
			variant := sumType.SumOf[i]
			post := capitalize(variant.NamePostfix())
			c.gf.P("case ", c.oid(post+sumType.Name), ":")
			if pe, ok := c.fromOgenInner(pf, variant, s+"."+post); ok {
				c.gf.P("dst.", oneof.GoName, " = &", pf.GoIdent, "{", pf.GoName, ": ", pe, "}")
			}
		}
		c.gf.P("}")
	}
	if wrap {
		s := c.newTmp("s")
		c.gf.P("if ", s, ", ok := src.", of.Name, ".Get(); ok {")
		emit(s)
		c.gf.P("}")
	} else {
		emit("src." + of.Name)
	}
}

// fromOgenOneofObject reconstructs a protobuf oneof from OBJECT-mode ogen
// properties: whichever branch property is present builds the oneof wrapper.
func (c *convGen) fromOgenOneofObject(oneof *protogen.Oneof, fields map[string]*ir.Field) {
	for _, pf := range oneof.Fields {
		of := fields[fieldOpenAPIName(pf)]
		if of == nil {
			continue
		}
		core, wrapped := unwrapGeneric(of.Type)
		if unsupportedType(core) {
			c.gf.P("// ", of.Name, ": unsupported oneof branch, skipped")
			continue
		}
		if !wrapped {
			continue
		}
		v := c.newTmp("v")
		c.gf.P("if ", v, ", ok := src.", of.Name, ".Get(); ok {")
		if pe, ok := c.fromOgenInner(pf, core, v); ok {
			c.gf.P("dst.", oneof.GoName, " = &", pf.GoIdent, "{", pf.GoName, ": ", pe, "}")
		}
		c.gf.P("}")
	}
}

type enumPair struct{ pb, ogen string }

func (c *convGen) enumPairs(pf *protogen.Field, ot *ir.Type) []enumPair {
	var out []enumPair
	for _, ev := range ot.EnumVariants {
		for _, pv := range pf.Enum.Values {
			if enumValueMatch(ev.Value, pv) {
				out = append(out, enumPair{
					pb:   c.qual(pv.GoIdent),
					ogen: c.qual(c.oid(ev.Name)),
				})
				break
			}
		}
	}
	return out
}

func enumValueMatch(v any, pv *protogen.EnumValue) bool {
	if s, ok := v.(string); ok {
		return s == string(pv.Desc.Name())
	}
	return fmt.Sprint(v) == fmt.Sprint(int64(pv.Desc.Number()))
}
