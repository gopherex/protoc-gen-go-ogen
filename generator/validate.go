package generator

import (
	"regexp"
	"strings"

	"github.com/envoyproxy/protoc-gen-validate/validate"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

// getValidateRules returns the protoc-gen-validate (PGV) field rules attached to
// a field through the (validate.rules) extension, or nil when absent.
func getValidateRules(field *protogen.Field) *validate.FieldRules {
	opts, ok := field.Desc.Options().(*descriptorpb.FieldOptions)
	if !ok || opts == nil {
		return nil
	}
	if !proto.HasExtension(opts, validate.E_Rules) {
		return nil
	}
	rules, _ := proto.GetExtension(opts, validate.E_Rules).(*validate.FieldRules)
	return rules
}

// validateRequired reports whether PGV marks a message-typed field as required.
func validateRequired(field *protogen.Field) bool {
	return getValidateRules(field).GetMessage().GetRequired()
}

// applyValidation translates PGV field rules into OpenAPI schema constraints and
// writes them onto schema. Explicit ogen.field schema options applied later win
// over the derived constraints.
func (g *OpenAPIGenerator) applyValidation(schema map[string]any, field *protogen.Field) {
	if rules := getValidateRules(field); rules != nil {
		g.applyFieldRules(schema, rules, field)
	}
}

// applyFieldRules dispatches on the PGV rule type. field describes the value the
// schema represents and is used for enum value lookups.
func (g *OpenAPIGenerator) applyFieldRules(schema map[string]any, rules *validate.FieldRules, field *protogen.Field) {
	switch r := rules.GetType().(type) {
	case *validate.FieldRules_Float:
		applyNumericRules(schema, g.oasVersion, r.Float.Const, r.Float.Lt, r.Float.Lte, r.Float.Gt, r.Float.Gte, r.Float.In)
	case *validate.FieldRules_Double:
		applyNumericRules(schema, g.oasVersion, r.Double.Const, r.Double.Lt, r.Double.Lte, r.Double.Gt, r.Double.Gte, r.Double.In)
	case *validate.FieldRules_Int32:
		applyNumericRules(schema, g.oasVersion, r.Int32.Const, r.Int32.Lt, r.Int32.Lte, r.Int32.Gt, r.Int32.Gte, r.Int32.In)
	case *validate.FieldRules_Int64:
		applyNumericRules(schema, g.oasVersion, r.Int64.Const, r.Int64.Lt, r.Int64.Lte, r.Int64.Gt, r.Int64.Gte, r.Int64.In)
	case *validate.FieldRules_Uint32:
		applyNumericRules(schema, g.oasVersion, r.Uint32.Const, r.Uint32.Lt, r.Uint32.Lte, r.Uint32.Gt, r.Uint32.Gte, r.Uint32.In)
	case *validate.FieldRules_Uint64:
		applyNumericRules(schema, g.oasVersion, r.Uint64.Const, r.Uint64.Lt, r.Uint64.Lte, r.Uint64.Gt, r.Uint64.Gte, r.Uint64.In)
	case *validate.FieldRules_Sint32:
		applyNumericRules(schema, g.oasVersion, r.Sint32.Const, r.Sint32.Lt, r.Sint32.Lte, r.Sint32.Gt, r.Sint32.Gte, r.Sint32.In)
	case *validate.FieldRules_Sint64:
		applyNumericRules(schema, g.oasVersion, r.Sint64.Const, r.Sint64.Lt, r.Sint64.Lte, r.Sint64.Gt, r.Sint64.Gte, r.Sint64.In)
	case *validate.FieldRules_Fixed32:
		applyNumericRules(schema, g.oasVersion, r.Fixed32.Const, r.Fixed32.Lt, r.Fixed32.Lte, r.Fixed32.Gt, r.Fixed32.Gte, r.Fixed32.In)
	case *validate.FieldRules_Fixed64:
		applyNumericRules(schema, g.oasVersion, r.Fixed64.Const, r.Fixed64.Lt, r.Fixed64.Lte, r.Fixed64.Gt, r.Fixed64.Gte, r.Fixed64.In)
	case *validate.FieldRules_Sfixed32:
		applyNumericRules(schema, g.oasVersion, r.Sfixed32.Const, r.Sfixed32.Lt, r.Sfixed32.Lte, r.Sfixed32.Gt, r.Sfixed32.Gte, r.Sfixed32.In)
	case *validate.FieldRules_Sfixed64:
		applyNumericRules(schema, g.oasVersion, r.Sfixed64.Const, r.Sfixed64.Lt, r.Sfixed64.Lte, r.Sfixed64.Gt, r.Sfixed64.Gte, r.Sfixed64.In)
	case *validate.FieldRules_Bool:
		if r.Bool.Const != nil {
			schema["enum"] = []any{*r.Bool.Const}
		}
	case *validate.FieldRules_String_:
		applyStringRules(schema, r.String_)
	case *validate.FieldRules_Bytes:
		applyBytesRules(schema, r.Bytes)
	case *validate.FieldRules_Enum:
		applyEnumRules(schema, field, r.Enum)
	case *validate.FieldRules_Repeated:
		g.applyRepeatedRules(schema, r.Repeated, field)
	case *validate.FieldRules_Map:
		g.applyMapRules(schema, r.Map, field)
	}
	// Any/Duration/Timestamp rules have no native OpenAPI representation.
}

type numeric interface {
	int32 | int64 | uint32 | uint64 | float32 | float64
}

// applyNumericRules maps PGV numeric range/const/in rules onto an OpenAPI schema.
func applyNumericRules[T numeric](schema map[string]any, ver string, constv, lt, lte, gt, gte *T, in []T) {
	if constv != nil {
		schema["enum"] = []any{*constv}
		return
	}
	if gte != nil {
		setBound(schema, "minimum", "exclusiveMinimum", *gte, false, ver)
	}
	if gt != nil {
		setBound(schema, "minimum", "exclusiveMinimum", *gt, true, ver)
	}
	if lte != nil {
		setBound(schema, "maximum", "exclusiveMaximum", *lte, false, ver)
	}
	if lt != nil {
		setBound(schema, "maximum", "exclusiveMaximum", *lt, true, ver)
	}
	if len(in) > 0 {
		values := make([]any, len(in))
		for i, v := range in {
			values[i] = v
		}
		schema["enum"] = values
	}
}

// setBound writes an inclusive or exclusive numeric bound, accounting for the
// OpenAPI 3.0 vs 3.1 difference in how exclusive bounds are expressed.
func setBound[T numeric](schema map[string]any, incKey, exclKey string, v T, exclusive bool, ver string) {
	if !exclusive {
		schema[incKey] = v
		return
	}
	if strings.HasPrefix(ver, "3.1") {
		schema[exclKey] = v
		return
	}
	schema[incKey] = v
	schema[exclKey] = true
}

func applyStringRules(schema map[string]any, r *validate.StringRules) {
	if r == nil {
		return
	}
	if r.Const != nil {
		schema["enum"] = []any{*r.Const}
		return
	}
	if r.Len != nil {
		schema["minLength"] = *r.Len
		schema["maxLength"] = *r.Len
	}
	if r.MinLen != nil {
		schema["minLength"] = *r.MinLen
	}
	if r.MaxLen != nil {
		schema["maxLength"] = *r.MaxLen
	}
	if r.Pattern != nil {
		schema["pattern"] = *r.Pattern
	} else if pattern := affixPattern(r.GetPrefix(), r.GetSuffix(), r.GetContains()); pattern != "" {
		schema["pattern"] = pattern
	}
	if len(r.In) > 0 {
		values := make([]any, len(r.In))
		for i, v := range r.In {
			values[i] = v
		}
		schema["enum"] = values
	}
	if format := stringFormat(r); format != "" {
		if _, ok := schema["format"]; !ok {
			schema["format"] = format
		}
	}
}

// affixPattern builds a single anchored regular expression from PGV prefix,
// suffix, and contains rules. ogen uses regexp2, so lookahead is supported.
func affixPattern(prefix, suffix, contains string) string {
	if prefix == "" && suffix == "" && contains == "" {
		return ""
	}
	if prefix == "" && suffix == "" {
		return regexp.QuoteMeta(contains)
	}
	var b strings.Builder
	b.WriteString("^")
	if contains != "" {
		b.WriteString("(?=.*")
		b.WriteString(regexp.QuoteMeta(contains))
		b.WriteString(")")
	}
	b.WriteString(regexp.QuoteMeta(prefix))
	b.WriteString(".*")
	b.WriteString(regexp.QuoteMeta(suffix))
	b.WriteString("$")
	return b.String()
}

func stringFormat(r *validate.StringRules) string {
	switch {
	case r.GetEmail():
		return "email"
	case r.GetUuid():
		return "uuid"
	case r.GetUri():
		return "uri"
	case r.GetUriRef():
		return "uri-reference"
	case r.GetIpv4():
		return "ipv4"
	case r.GetIpv6():
		return "ipv6"
	case r.GetIp():
		return "ip"
	case r.GetHostname():
		return "hostname"
	default:
		// address (hostname or ip) has no native OpenAPI format.
		return ""
	}
}

func applyBytesRules(schema map[string]any, r *validate.BytesRules) {
	if r == nil {
		return
	}
	// PGV byte lengths are byte counts; mapped to string length as a best effort.
	if r.Len != nil {
		schema["minLength"] = *r.Len
		schema["maxLength"] = *r.Len
	}
	if r.MinLen != nil {
		schema["minLength"] = *r.MinLen
	}
	if r.MaxLen != nil {
		schema["maxLength"] = *r.MaxLen
	}
	if r.Pattern != nil {
		schema["pattern"] = *r.Pattern
	}
}

// applyEnumRules restricts the enum values already present on schema using PGV
// const/in/not_in rules, preserving the string or integer representation.
func applyEnumRules(schema map[string]any, field *protogen.Field, r *validate.EnumRules) {
	if r == nil || field == nil || field.Enum == nil {
		return
	}
	asString := false
	if opts := getFieldOptions(field); opts != nil {
		asString = opts.GetEnumAsString()
	}
	in := map[int32]bool{}
	for _, v := range r.In {
		in[v] = true
	}
	notIn := map[int32]bool{}
	for _, v := range r.NotIn {
		notIn[v] = true
	}
	allowed := func(num int32) bool {
		if r.Const != nil {
			return num == *r.Const
		}
		if len(in) > 0 && !in[num] {
			return false
		}
		if notIn[num] {
			return false
		}
		return true
	}
	if r.Const == nil && len(in) == 0 && len(notIn) == 0 {
		return
	}
	if asString {
		values := []string{}
		for _, value := range field.Enum.Values {
			if allowed(int32(value.Desc.Number())) {
				values = append(values, string(value.Desc.Name()))
			}
		}
		schema["enum"] = values
		return
	}
	values := []int32{}
	for _, value := range field.Enum.Values {
		if allowed(int32(value.Desc.Number())) {
			values = append(values, int32(value.Desc.Number()))
		}
	}
	schema["enum"] = values
}

func (g *OpenAPIGenerator) applyRepeatedRules(schema map[string]any, r *validate.RepeatedRules, field *protogen.Field) {
	if r == nil {
		return
	}
	if r.MinItems != nil {
		schema["minItems"] = *r.MinItems
	}
	if r.MaxItems != nil {
		schema["maxItems"] = *r.MaxItems
	}
	if r.GetUnique() {
		schema["uniqueItems"] = true
	}
	if r.Items != nil {
		if items, ok := schema["items"].(map[string]any); ok {
			g.applyFieldRules(items, r.Items, field)
		}
	}
}

func (g *OpenAPIGenerator) applyMapRules(schema map[string]any, r *validate.MapRules, field *protogen.Field) {
	if r == nil {
		return
	}
	if r.MinPairs != nil {
		schema["minProperties"] = *r.MinPairs
	}
	if r.MaxPairs != nil {
		schema["maxProperties"] = *r.MaxPairs
	}
	var keyField, valueField *protogen.Field
	if field != nil && field.Message != nil && len(field.Message.Fields) == 2 {
		keyField = field.Message.Fields[0]
		valueField = field.Message.Fields[1]
	}
	if r.Values != nil {
		if ap, ok := schema["additionalProperties"].(map[string]any); ok {
			g.applyFieldRules(ap, r.Values, valueField)
		}
	}
	// OpenAPI key constraints (propertyNames) only exist in 3.1.
	if r.Keys != nil && strings.HasPrefix(g.oasVersion, "3.1") {
		names := map[string]any{}
		g.applyFieldRules(names, r.Keys, keyField)
		if len(names) > 0 {
			schema["propertyNames"] = names
		}
	}
}
