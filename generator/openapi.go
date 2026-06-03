package generator

import (
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/gopherex/protoc-gen-go-ogen/ogen"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"gopkg.in/yaml.v3"
)

type OpenAPIGenerator struct {
	Plugin        *protogen.Plugin
	Settings      *PluginSettings
	componentName map[protoreflect.FullName]string
	oasVersion    string
	// defaultOneofMode is the bundle-wide oneof representation applied to oneofs
	// without an explicit (ogen.oneof) schema_mode.
	defaultOneofMode ogen.OneofSchemaMode
	errs             []error
}

type openAPIBundle struct {
	root     *protogen.File
	rootOpts *ogen.FileOptions
	files    []*protogen.File
}

func NewOpenAPIGenerator(p *protogen.Plugin, settings *PluginSettings) *OpenAPIGenerator {
	return &OpenAPIGenerator{
		Plugin:        p,
		Settings:      settings,
		componentName: map[protoreflect.FullName]string{},
	}
}

func (g *OpenAPIGenerator) Generate() error {
	bundle := g.openAPIBundle()
	if bundle == nil {
		return nil
	}
	if err := g.generateBundle(bundle); err != nil {
		return fmt.Errorf("%s: %w", bundle.root.Desc.Path(), err)
	}
	return nil
}

func (g *OpenAPIGenerator) openAPIBundle() *openAPIBundle {
	var bundle *openAPIBundle
	for _, file := range g.Plugin.Files {
		if !file.Generate {
			continue
		}
		fileOpts := getFileOptions(file)
		if fileOpts == nil || !fileOpts.GetGenerateOpenapi() {
			continue
		}
		if bundle == nil {
			bundle = &openAPIBundle{}
		}
		bundle.files = append(bundle.files, file)
		// The aggregate root is the single file carrying document-level options.
		// Selecting it by content (not file order) keeps the bundle deterministic
		// regardless of how protoc orders the inputs; a second carrier is rejected
		// in validateBundle.
		if bundle.root == nil || (!hasDocLevelOptions(bundle.rootOpts) && hasDocLevelOptions(fileOpts)) {
			bundle.root = file
			bundle.rootOpts = fileOpts
		}
	}
	return bundle
}

// hasDocLevelOptions reports whether ogen.file carries any document- or
// generation-level option (anything beyond the generate_openapi inclusion
// marker). Only the bundle root may set these.
func hasDocLevelOptions(o *ogen.FileOptions) bool {
	if o == nil {
		return false
	}
	return o.GetGenerateOgen() || o.GetGenerateConverters() || o.GetGenerateGrpcAdapter() ||
		o.GetOpenapiVersion() != "" || o.GetTitle() != "" || o.GetVersion() != "" ||
		o.GetSummary() != "" || o.GetDescription() != "" || o.GetTermsOfService() != "" ||
		o.GetContact() != nil || o.GetLicense() != nil ||
		len(o.GetServers()) > 0 || len(o.GetTags()) > 0 || o.GetExternalDocs() != nil ||
		o.GetOpenapiOutput() != "" || o.GetOgenTarget() != "" ||
		o.GetOgenPackage() != "" || o.GetOgenPackageName() != "" || len(o.GetExtensions()) > 0
}

// validateBundle rejects ambiguous bundles: any non-root file carrying
// document-level options would have those options silently dropped (only the
// root's options shape the aggregate document and the single ogen run).
func (g *OpenAPIGenerator) validateBundle(bundle *openAPIBundle) {
	for _, file := range bundle.files {
		if file == bundle.root {
			continue
		}
		if hasDocLevelOptions(getFileOptions(file)) {
			g.errs = append(g.errs, fmt.Errorf(
				"file %s sets document-level ogen.file options, but %s is the aggregate root; only one file may carry document-level options (the others must set generate_openapi only)",
				file.Desc.Path(), bundle.root.Desc.Path()))
		}
	}
}

func (g *OpenAPIGenerator) generateBundle(bundle *openAPIBundle) error {
	g.componentName = map[protoreflect.FullName]string{}
	g.errs = nil
	mode, err := g.bundleOneofDefault(bundle.rootOpts)
	if err != nil {
		return err
	}
	g.defaultOneofMode = mode
	g.validateBundle(bundle)
	messages := g.bundleMessages(bundle.files)
	g.collectComponentNames(messages)
	g.oasVersion = openAPIVersion(bundle.rootOpts, bundleHasWebhooks(bundle.files))
	for _, file := range bundle.files {
		g.rejectUnsupportedStreaming(file)
	}
	if len(g.errs) > 0 {
		return errors.Join(g.errs...)
	}

	paths := map[string]any{}
	webhooks := map[string]any{}
	for _, file := range bundle.files {
		if err := mergeOperationMaps(paths, g.pathsObject(file), "path"); err != nil {
			return err
		}
		if err := mergeOperationMaps(webhooks, g.webhooksObject(file), "webhook"); err != nil {
			return err
		}
	}
	if len(webhooks) > 0 && strings.HasPrefix(bundle.rootOpts.GetOpenapiVersion(), "3.0.") {
		return fmt.Errorf("webhooks require OpenAPI 3.1; remove openapi_version override or set it to 3.1.0")
	}
	if err := checkUniqueOperationIDs(paths, webhooks); err != nil {
		return err
	}
	doc := map[string]any{
		"openapi": g.oasVersion,
		"info":    g.infoObject(bundle.root, bundle.rootOpts),
		"paths":   paths,
	}
	if len(webhooks) > 0 {
		doc["webhooks"] = webhooks
	}
	if servers := serversObject(bundle.rootOpts.GetServers()); len(servers) > 0 {
		doc["servers"] = servers
	}
	if tags := tagsObject(bundle.rootOpts.GetTags()); len(tags) > 0 {
		doc["tags"] = tags
	}
	if externalDocs := externalDocsObject(bundle.rootOpts.GetExternalDocs()); len(externalDocs) > 0 {
		doc["externalDocs"] = externalDocs
	}
	applyExtensions(doc, bundle.rootOpts.GetExtensions())

	schemas := g.schemasObject(messages)
	if len(g.errs) > 0 {
		return errors.Join(g.errs...)
	}
	if len(schemas) > 0 {
		doc["components"] = map[string]any{
			"schemas": schemas,
		}
	}

	// The document fed to ogen must not carry OBJECT-mode oneof markers (ogen
	// would build a sum type from a top-level oneOf). Expand them into a real
	// oneOf/allOf for the public output, and strip them for ogen.
	var ogenDoc map[string]any
	if containsObjectOneof(doc) {
		ogenDoc = deepCopyNode(doc).(map[string]any)
		finalizeObjectOneof(ogenDoc, false)
		finalizeObjectOneof(doc, true)
	}

	data, err := yaml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("marshal openapi yaml: %w", err)
	}
	ogenData := data
	if ogenDoc != nil {
		ogenData, err = yaml.Marshal(ogenDoc)
		if err != nil {
			return fmt.Errorf("marshal ogen openapi yaml: %w", err)
		}
	}

	yamlName := openapiFileName(bundle.root, bundle.rootOpts)
	switch {
	case g.Settings.OpenAPIOut != "":
		// Explicit CLI destination: write the document to disk.
		if err := writeOpenAPIFile(g.Settings.OpenAPIOut, yamlName, data); err != nil {
			return err
		}
	case !bundle.rootOpts.GetGenerateOgen():
		// No ogen output requested and no disk destination: emit the document
		// through protoc so the plugin still produces something.
		gf := g.Plugin.NewGeneratedFile(yamlName, "")
		if _, err := gf.Write(data); err != nil {
			return err
		}
	}

	if bundle.rootOpts.GetGenerateOgen() {
		if err := g.generateOgen(bundle.files, bundle.rootOpts, ogenData); err != nil {
			return err
		}
	}
	return nil
}

func (g *OpenAPIGenerator) bundleMessages(files []*protogen.File) []*protogen.Message {
	seen := map[protoreflect.FullName]bool{}
	index := g.messageIndex()
	var out []*protogen.Message
	for _, file := range files {
		for _, msg := range file.Messages {
			g.appendBundleMessage(&out, seen, msg)
		}
	}
	for i := 0; i < len(out); i++ {
		for _, field := range out[i].Fields {
			if field.Message == nil || wellKnownSchema(field.Message.Desc.FullName()) != nil {
				continue
			}
			target := field.Message
			if target.Desc.IsMapEntry() && len(target.Fields) == 2 {
				target = target.Fields[1].Message
				if target == nil || wellKnownSchema(target.Desc.FullName()) != nil {
					continue
				}
			}
			if indexed := index[target.Desc.FullName()]; indexed != nil {
				target = indexed
			}
			g.appendBundleMessage(&out, seen, target)
		}
	}
	return out
}

func (g *OpenAPIGenerator) messageIndex() map[protoreflect.FullName]*protogen.Message {
	index := map[protoreflect.FullName]*protogen.Message{}
	for _, file := range g.Plugin.Files {
		indexMessages(index, file.Messages)
	}
	return index
}

func indexMessages(index map[protoreflect.FullName]*protogen.Message, messages []*protogen.Message) {
	for _, msg := range messages {
		if msg.Desc.IsMapEntry() {
			continue
		}
		index[msg.Desc.FullName()] = msg
		indexMessages(index, msg.Messages)
	}
}

func (g *OpenAPIGenerator) appendBundleMessage(out *[]*protogen.Message, seen map[protoreflect.FullName]bool, msg *protogen.Message) {
	if msg.Desc.IsMapEntry() || messageOmitted(msg) || seen[msg.Desc.FullName()] {
		return
	}
	seen[msg.Desc.FullName()] = true
	*out = append(*out, msg)
	for _, nested := range msg.Messages {
		g.appendBundleMessage(out, seen, nested)
	}
}

func (g *OpenAPIGenerator) collectComponentNames(messages []*protogen.Message) {
	for _, msg := range messages {
		if msg.Desc.IsMapEntry() || messageOmitted(msg) {
			continue
		}
		if _, ok := g.componentName[msg.Desc.FullName()]; ok {
			continue
		}
		name := ""
		if opts := getMessageOptions(msg); opts != nil {
			name = opts.GetSchemaName()
		}
		if name == "" {
			name = componentNameFromFullName(msg.Desc.FullName())
			// A non-WKT message whose simple name shadows a well-known type (e.g.
			// schemapb.Duration) would take the same Go name ogen derives for the
			// WKT's optional wrapper (OptDuration), colliding. Qualify it.
			if reservedWKTName(name) {
				name = qualifiedComponentName(msg.Desc.FullName())
			}
		}
		g.componentName[msg.Desc.FullName()] = uniqueComponentName(name, g.componentName)
		g.collectComponentNames(msg.Messages)
	}
}

func (g *OpenAPIGenerator) infoObject(file *protogen.File, opts *ogen.FileOptions) map[string]any {
	title := opts.GetTitle()
	if title == "" {
		title = string(file.Desc.Package())
		if title == "" {
			title = path.Base(file.Desc.Path())
		}
	}
	version := opts.GetVersion()
	if version == "" {
		version = "0.0.0"
	}
	info := map[string]any{
		"title":   title,
		"version": version,
	}
	setString(info, "summary", opts.GetSummary())
	setString(info, "description", opts.GetDescription())
	setString(info, "termsOfService", opts.GetTermsOfService())
	if contact := contactObject(opts.GetContact()); len(contact) > 0 {
		info["contact"] = contact
	}
	if license := licenseObject(opts.GetLicense()); len(license) > 0 {
		info["license"] = license
	}
	return info
}

func (g *OpenAPIGenerator) pathsObject(file *protogen.File) map[string]any {
	paths := map[string]any{}
	for _, svc := range file.Services {
		svcOpts := getServiceOptions(svc)
		if svcOpts != nil && svcOpts.GetOmit() {
			continue
		}
		prefix := ""
		tags := []string{string(svc.Desc.Name())}
		var servers []*ogen.Server
		if svcOpts != nil {
			prefix = svcOpts.GetPathPrefix()
			if len(svcOpts.GetTags()) > 0 {
				tags = svcOpts.GetTags()
			}
			servers = svcOpts.GetServers()
		}
		for _, method := range svc.Methods {
			methodOpts := getMethodOptions(method)
			if methodOpts == nil || methodOpts.GetOmit() || methodOpts.GetHttpMethod() == ogen.HttpMethod_HTTP_METHOD_UNSPECIFIED || methodOpts.GetPath() == "" {
				continue
			}
			if methodOpts.GetWebhook().GetName() != "" {
				continue
			}
			httpPath := joinHTTPPath(prefix, methodOpts.GetPath())
			item, _ := paths[httpPath].(map[string]any)
			if item == nil {
				item = map[string]any{}
				paths[httpPath] = item
			}
			item[httpMethodName(methodOpts.GetHttpMethod())] = g.operationObject(svc, method, tags, servers, methodOpts)
		}
	}
	return paths
}

func (g *OpenAPIGenerator) webhooksObject(file *protogen.File) map[string]any {
	webhooks := map[string]any{}
	for _, svc := range file.Services {
		svcOpts := getServiceOptions(svc)
		if svcOpts != nil && svcOpts.GetOmit() {
			continue
		}
		tags := []string{string(svc.Desc.Name())}
		var servers []*ogen.Server
		if svcOpts != nil {
			if len(svcOpts.GetTags()) > 0 {
				tags = svcOpts.GetTags()
			}
			servers = svcOpts.GetServers()
		}
		for _, method := range svc.Methods {
			methodOpts := getMethodOptions(method)
			if methodOpts == nil || methodOpts.GetOmit() || methodOpts.GetHttpMethod() == ogen.HttpMethod_HTTP_METHOD_UNSPECIFIED {
				continue
			}
			name := methodOpts.GetWebhook().GetName()
			if name == "" {
				continue
			}
			for _, param := range methodOpts.GetParameters() {
				if param.GetIn() == ogen.ParameterLocation_PARAMETER_LOCATION_PATH {
					g.errs = append(g.errs, fmt.Errorf("webhook %q method %s cannot use path parameter %q", name, method.Desc.FullName(), param.GetFieldPath()))
				}
			}
			item, _ := webhooks[name].(map[string]any)
			if item == nil {
				item = map[string]any{}
				webhooks[name] = item
			}
			item[httpMethodName(methodOpts.GetHttpMethod())] = g.operationObject(svc, method, tags, servers, methodOpts)
		}
	}
	return webhooks
}

func (g *OpenAPIGenerator) operationObject(svc *protogen.Service, method *protogen.Method, serviceTags []string, serviceServers []*ogen.Server, opts *ogen.MethodOptions) map[string]any {
	op := map[string]any{
		"operationId": firstNonEmpty(opts.GetOperationId(), lowerFirst(string(svc.Desc.Name()))+string(method.Desc.Name())),
		"responses":   g.responsesObject(method, opts.GetResponses()),
	}
	if summary := firstNonEmpty(opts.GetSummary(), firstCommentLine(method.Comments.Leading)); summary != "" {
		op["summary"] = summary
	}
	if desc := firstNonEmpty(opts.GetDescription(), comments(method.Comments.Leading)); desc != "" {
		op["description"] = desc
	}
	tags := opts.GetTags()
	if len(tags) == 0 {
		tags = serviceTags
	}
	if len(tags) > 0 {
		op["tags"] = tags
	}
	if opts.GetDeprecated() || method.Desc.Options().(*descriptorpb.MethodOptions).GetDeprecated() {
		op["deprecated"] = true
	}
	params := g.parametersObject(method.Input, opts.GetParameters())
	params = g.applyIdempotency(op, method, opts, params)
	if len(params) > 0 {
		op["parameters"] = params
	}
	if body := g.requestBodyObject(method.Input, opts.GetRequestBody()); len(body) > 0 {
		op["requestBody"] = body
	}
	servers := opts.GetServers()
	if len(servers) == 0 {
		servers = serviceServers
	}
	if serverObjects := serversObject(servers); len(serverObjects) > 0 {
		op["servers"] = serverObjects
	}
	applyExtensions(op, opts.GetExtensions())
	if _, ok := op["x-ogen-operation-group"]; !ok {
		if group := defaultOperationGroup(svc, tags); group != "" {
			op["x-ogen-operation-group"] = group
		}
	}
	return op
}

// applyIdempotency reads the builtin google.protobuf.MethodOptions.idempotency_level
// and surfaces it: an x-idempotency-level extension on the operation, fail-fast
// validation that NO_SIDE_EFFECTS uses a safe HTTP method, and an injected
// Idempotency-Key header for IDEMPOTENT operations.
func (g *OpenAPIGenerator) applyIdempotency(op map[string]any, method *protogen.Method, opts *ogen.MethodOptions, params []any) []any {
	switch method.Desc.Options().(*descriptorpb.MethodOptions).GetIdempotencyLevel() {
	case descriptorpb.MethodOptions_NO_SIDE_EFFECTS:
		op["x-idempotency-level"] = "NO_SIDE_EFFECTS"
		if !safeHTTPMethod(opts.GetHttpMethod()) {
			g.errs = append(g.errs, fmt.Errorf(
				"method %s declares idempotency_level=NO_SIDE_EFFECTS but uses non-safe HTTP method %q; use GET, HEAD, OPTIONS, or TRACE",
				method.Desc.FullName(), httpMethodName(opts.GetHttpMethod())))
		}
	case descriptorpb.MethodOptions_IDEMPOTENT:
		op["x-idempotency-level"] = "IDEMPOTENT"
		if !hasHeaderParam(params, "Idempotency-Key") {
			params = append(params, idempotencyKeyHeader())
		}
	}
	return params
}

func safeHTTPMethod(m ogen.HttpMethod) bool {
	switch m {
	case ogen.HttpMethod_HTTP_METHOD_GET, ogen.HttpMethod_HTTP_METHOD_HEAD,
		ogen.HttpMethod_HTTP_METHOD_OPTIONS, ogen.HttpMethod_HTTP_METHOD_TRACE:
		return true
	default:
		return false
	}
}

func hasHeaderParam(params []any, name string) bool {
	for _, p := range params {
		m, ok := p.(map[string]any)
		if !ok {
			continue
		}
		if m["in"] == "header" {
			if n, _ := m["name"].(string); strings.EqualFold(n, name) {
				return true
			}
		}
	}
	return false
}

func idempotencyKeyHeader() map[string]any {
	return map[string]any{
		"name":        "Idempotency-Key",
		"in":          "header",
		"required":    false,
		"description": "Optional idempotency key used to safely retry the request.",
		"schema":      map[string]any{"type": "string", "format": "uuid"},
	}
}

func (g *OpenAPIGenerator) parametersObject(input *protogen.Message, bindings []*ogen.ParameterBinding) []any {
	out := make([]any, 0, len(bindings))
	for _, binding := range bindings {
		field := findField(input, binding.GetFieldPath())
		name := binding.GetName()
		if name == "" && field != nil {
			name = jsonName(field)
		}
		if name == "" {
			name = binding.GetFieldPath()
		}
		param := map[string]any{
			"name":   name,
			"in":     parameterLocationName(binding.GetIn()),
			"schema": g.parameterSchema(field, binding.GetSchema()),
		}
		if binding.GetIn() == ogen.ParameterLocation_PARAMETER_LOCATION_PATH {
			param["required"] = true
		} else if binding.Required != nil {
			param["required"] = binding.GetRequired()
		}
		setString(param, "description", binding.GetDescription())
		setString(param, "style", binding.GetStyle())
		if binding.Explode != nil {
			param["explode"] = binding.GetExplode()
		}
		out = append(out, param)
	}
	return out
}

func (g *OpenAPIGenerator) parameterSchema(field *protogen.Field, override *ogen.SchemaOptions) map[string]any {
	if field == nil {
		schema := map[string]any{"type": "string"}
		applySchemaOptions(schema, override)
		return schema
	}
	schema := g.schemaForField(field)
	applySchemaOptions(schema, override)
	return schema
}

func (g *OpenAPIGenerator) requestBodyObject(input *protogen.Message, body *ogen.RequestBody) map[string]any {
	if body == nil {
		return nil
	}
	schema := g.messageOrFieldSchema(input, body.GetFieldPath())
	media := map[string]any{
		"schema": schema,
	}
	applyExtensions(media, body.GetMediaExtensions())
	req := map[string]any{
		"content": map[string]any{
			firstNonEmpty(body.GetContentType(), "application/json"): media,
		},
	}
	setString(req, "description", body.GetDescription())
	if body.GetRequired() {
		req["required"] = true
	}
	applyExtensions(req, body.GetExtensions())
	return req
}

func (g *OpenAPIGenerator) responsesObject(method *protogen.Method, responses []*ogen.Response) map[string]any {
	if len(responses) == 0 {
		responses = []*ogen.Response{{
			Status:      200,
			Description: "OK",
		}}
	}
	out := map[string]any{}
	for _, response := range responses {
		status := response.GetStatus()
		res := map[string]any{
			"description": firstNonEmpty(response.GetDescription(), defaultStatusDescription(status)),
		}
		if content := g.responseContent(method, response); len(content) > 0 {
			res["content"] = content
		}
		if headers := headersObject(response.GetHeaders()); len(headers) > 0 {
			res["headers"] = headers
		}
		key := "default"
		if status != 0 {
			key = fmt.Sprintf("%d", status)
		}
		out[key] = res
	}
	return out
}

func (g *OpenAPIGenerator) responseContent(method *protogen.Method, response *ogen.Response) map[string]any {
	output := method.Output
	if method.Desc.IsStreamingServer() && response.GetStatus() >= 200 && response.GetStatus() < 300 {
		// Server-streaming RPCs are exposed to HTTP clients as Server-Sent
		// Events. ogen has no native SSE codec, so generate an io.Reader body
		// under the SSE media type; the grpc adapter serializes each protobuf
		// response message as one "data: <protojson>" event.
		return map[string]any{
			firstNonEmpty(response.GetContentType(), "text/event-stream"): map[string]any{
				"schema": map[string]any{
					"type":   "string",
					"format": "binary",
				},
				"x-ogen-sse": true,
			},
		}
	}
	var schema map[string]any
	switch {
	case response.GetSchema().GetRef() != "":
		schema = map[string]any{"$ref": response.GetSchema().GetRef()}
	case response.GetFieldPath() != "":
		schema = g.messageOrFieldSchema(output, response.GetFieldPath())
	case isEmptyMessage(output):
		return nil
	default:
		schema = g.schemaForMessage(output)
	}
	return map[string]any{
		firstNonEmpty(response.GetContentType(), "application/json"): map[string]any{
			"schema": schema,
		},
	}
}

func (g *OpenAPIGenerator) messageOrFieldSchema(msg *protogen.Message, fieldPath string) map[string]any {
	if fieldPath == "" {
		return g.schemaForMessage(msg)
	}
	if field := findField(msg, fieldPath); field != nil {
		return g.schemaForField(field)
	}
	return g.schemaForMessage(msg)
}

func (g *OpenAPIGenerator) schemasObject(messages []*protogen.Message) map[string]any {
	schemas := map[string]any{}
	for _, msg := range messages {
		g.appendSchemas(schemas, msg)
	}
	return schemas
}

func (g *OpenAPIGenerator) appendSchemas(schemas map[string]any, msg *protogen.Message) {
	if msg.Desc.IsMapEntry() || messageOmitted(msg) {
		return
	}
	if name := g.componentName[msg.Desc.FullName()]; name != "" {
		schemas[name] = g.objectSchema(msg)
	}
	for _, nested := range msg.Messages {
		g.appendSchemas(schemas, nested)
	}
}

func (g *OpenAPIGenerator) objectSchema(msg *protogen.Message) map[string]any {
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
	required := []string{}
	if opts := getMessageOptions(msg); opts != nil {
		applySchemaOptions(schema, opts.GetSchema())
		if opts.AdditionalProperties != nil {
			if opts.GetAdditionalProperties() {
				if ref := opts.GetAdditionalPropertiesSchema().GetRef(); ref != "" {
					schema["additionalProperties"] = map[string]any{"$ref": ref}
				} else {
					schema["additionalProperties"] = true
				}
			} else {
				schema["additionalProperties"] = false
			}
		} else {
			schema["additionalProperties"] = false
		}
		if opts.GetDiscriminatorProperty() != "" {
			schema["discriminator"] = discriminatorObject(opts.GetDiscriminatorProperty(), opts.GetDiscriminatorMapping())
		}
		applyExtensions(schema, opts.GetExtensions())
	} else {
		schema["additionalProperties"] = false
	}
	properties := schema["properties"].(map[string]any)
	ogenProperties := map[string]any{}
	for _, field := range msg.Fields {
		if fieldOmitted(field) || field.Oneof != nil && !field.Oneof.Desc.IsSynthetic() {
			continue
		}
		name := fieldOpenAPIName(field)
		properties[name] = g.schemaForField(field)
		if opts := getFieldOptions(field); opts != nil && opts.GetGoName() != "" {
			ogenProperties[name] = map[string]any{"name": opts.GetGoName()}
		}
		if fieldRequired(field) {
			required = append(required, name)
		}
	}
	var objectOneofGroups [][]any
	for _, oneof := range msg.Oneofs {
		if oneof.Desc.IsSynthetic() {
			continue
		}
		if oneofMode(oneof, g.defaultOneofMode) == ogen.OneofSchemaMode_ONEOF_SCHEMA_MODE_OBJECT {
			// protojson form: each branch is its own (optional) object property,
			// with a oneOf-by-required constraint enforcing exactly one branch.
			var branchReqs []any
			for _, field := range oneof.Fields {
				if fieldOmitted(field) {
					continue
				}
				bname := fieldOpenAPIName(field)
				properties[bname] = g.schemaForField(field)
				if opts := getFieldOptions(field); opts != nil && opts.GetGoName() != "" {
					ogenProperties[bname] = map[string]any{"name": opts.GetGoName()}
				}
				branchReqs = append(branchReqs, map[string]any{"required": []any{bname}})
			}
			if len(branchReqs) > 0 {
				objectOneofGroups = append(objectOneofGroups, branchReqs)
			}
			continue
		}
		name := string(oneof.Desc.Name())
		properties[name] = g.schemaForOneof(oneof)
	}
	if len(ogenProperties) > 0 {
		schema["x-ogen-properties"] = ogenProperties
	}
	applyObjectOneofGroups(schema, objectOneofGroups)
	if len(required) > 0 {
		sort.Strings(required)
		schema["required"] = required
	}
	return schema
}

func (g *OpenAPIGenerator) schemaForField(field *protogen.Field) map[string]any {
	var schema map[string]any
	if field.Desc.IsMap() {
		value := field.Message.Fields[1]
		schema = map[string]any{
			"type":                 "object",
			"additionalProperties": g.schemaForField(value),
		}
	} else if field.Desc.IsList() {
		items := g.schemaForSingularField(field)
		if opts := getFieldOptions(field); opts != nil && field.Desc.Kind() == protoreflect.BytesKind {
			applySchemaOptions(items, opts.GetSchema())
		}
		schema = map[string]any{
			"type":  "array",
			"items": items,
		}
	} else {
		schema = g.schemaForSingularField(field)
	}
	g.applyValidation(schema, field)
	if opts := getFieldOptions(field); opts != nil {
		applySchemaOptions(schema, opts.GetSchema())
		applyExtensions(schema, opts.GetExtensions())
	}
	return schema
}

func (g *OpenAPIGenerator) schemaForSingularField(field *protogen.Field) map[string]any {
	switch field.Desc.Kind() {
	case protoreflect.DoubleKind:
		return map[string]any{"type": "number", "format": "double"}
	case protoreflect.FloatKind:
		return map[string]any{"type": "number", "format": "float"}
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return map[string]any{"type": "integer", "format": "int32"}
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return map[string]any{"type": "string", "format": "int64"}
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return map[string]any{"type": "integer", "format": "int32", "minimum": 0}
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return map[string]any{"type": "string", "format": "uint64"}
	case protoreflect.BoolKind:
		return map[string]any{"type": "boolean"}
	case protoreflect.StringKind:
		return map[string]any{"type": "string"}
	case protoreflect.BytesKind:
		return map[string]any{"type": "string", "format": "byte"}
	case protoreflect.EnumKind:
		return enumSchema(field)
	case protoreflect.MessageKind, protoreflect.GroupKind:
		return g.schemaForMessage(field.Message)
	default:
		return map[string]any{}
	}
}

func (g *OpenAPIGenerator) schemaForMessage(msg *protogen.Message) map[string]any {
	if msg == nil {
		return map[string]any{}
	}
	if schema := wellKnownSchema(msg.Desc.FullName()); schema != nil {
		return schema
	}
	if name := g.componentName[msg.Desc.FullName()]; name != "" {
		return map[string]any{"$ref": "#/components/schemas/" + name}
	}
	return g.objectSchema(msg)
}

func (g *OpenAPIGenerator) schemaForOneof(oneof *protogen.Oneof) map[string]any {
	var variants []any
	for _, field := range oneof.Fields {
		variants = append(variants, g.schemaForField(field))
	}
	schema := map[string]any{"oneOf": variants}
	if opts := getOneofOptions(oneof); opts != nil {
		if opts.GetSchemaMode() == ogen.OneofSchemaMode_ONEOF_SCHEMA_MODE_ANY_OF {
			delete(schema, "oneOf")
			schema["anyOf"] = variants
		}
		applySchemaOptions(schema, opts.GetSchema())
		if opts.GetDiscriminatorProperty() != "" {
			if !allOneOfVariantsAreRefs(variants) {
				g.errs = append(g.errs, fmt.Errorf(
					"oneof %s sets discriminator_property=%q, but not all variants are message refs; remove discriminator_property or wrap scalar variants in messages",
					oneof.Desc.FullName(),
					opts.GetDiscriminatorProperty(),
				))
			} else {
				schema["discriminator"] = discriminatorObject(opts.GetDiscriminatorProperty(), opts.GetDiscriminatorMapping())
			}
		}
	}
	return schema
}

func getFileOptions(file *protogen.File) *ogen.FileOptions {
	if opts, ok := file.Desc.Options().(*descriptorpb.FileOptions); ok && proto.HasExtension(opts, ogen.E_File) {
		if ext, ok := proto.GetExtension(opts, ogen.E_File).(*ogen.FileOptions); ok {
			return ext
		}
	}
	return nil
}

func getMessageOptions(msg *protogen.Message) *ogen.MessageOptions {
	if opts, ok := msg.Desc.Options().(*descriptorpb.MessageOptions); ok && proto.HasExtension(opts, ogen.E_Message) {
		if ext, ok := proto.GetExtension(opts, ogen.E_Message).(*ogen.MessageOptions); ok {
			return ext
		}
	}
	return nil
}

func getFieldOptions(field *protogen.Field) *ogen.FieldOptions {
	if opts, ok := field.Desc.Options().(*descriptorpb.FieldOptions); ok && proto.HasExtension(opts, ogen.E_Field) {
		if ext, ok := proto.GetExtension(opts, ogen.E_Field).(*ogen.FieldOptions); ok {
			return ext
		}
	}
	return nil
}

// oneofMode returns the effective OpenAPI representation for a oneof. A per-oneof
// schema_mode wins; otherwise the bundle default applies; otherwise ONE_OF.
func oneofMode(oneof *protogen.Oneof, dflt ogen.OneofSchemaMode) ogen.OneofSchemaMode {
	if opts := getOneofOptions(oneof); opts != nil {
		if m := opts.GetSchemaMode(); m != ogen.OneofSchemaMode_ONEOF_SCHEMA_MODE_UNSPECIFIED {
			return m
		}
	}
	if dflt != ogen.OneofSchemaMode_ONEOF_SCHEMA_MODE_UNSPECIFIED {
		return dflt
	}
	return ogen.OneofSchemaMode_ONEOF_SCHEMA_MODE_ONE_OF
}

// parseOneofSchemaMode maps the --ogen_opt=default_oneof_schema_mode flag value
// to its enum. An empty string yields UNSPECIFIED (defer to file option).
func parseOneofSchemaMode(s string) (ogen.OneofSchemaMode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "unspecified":
		return ogen.OneofSchemaMode_ONEOF_SCHEMA_MODE_UNSPECIFIED, nil
	case "one_of", "oneof":
		return ogen.OneofSchemaMode_ONEOF_SCHEMA_MODE_ONE_OF, nil
	case "any_of", "anyof":
		return ogen.OneofSchemaMode_ONEOF_SCHEMA_MODE_ANY_OF, nil
	case "object":
		return ogen.OneofSchemaMode_ONEOF_SCHEMA_MODE_OBJECT, nil
	default:
		return ogen.OneofSchemaMode_ONEOF_SCHEMA_MODE_UNSPECIFIED,
			fmt.Errorf("invalid default_oneof_schema_mode %q (want one_of|any_of|object)", s)
	}
}

// bundleOneofDefault resolves the effective bundle-wide oneof default: the plugin
// flag overrides the document's default_oneof_schema_mode file option.
func (g *OpenAPIGenerator) bundleOneofDefault(rootOpts *ogen.FileOptions) (ogen.OneofSchemaMode, error) {
	flagMode, err := parseOneofSchemaMode(g.Settings.DefaultOneofSchemaMode)
	if err != nil {
		return ogen.OneofSchemaMode_ONEOF_SCHEMA_MODE_UNSPECIFIED, err
	}
	if flagMode != ogen.OneofSchemaMode_ONEOF_SCHEMA_MODE_UNSPECIFIED {
		return flagMode, nil
	}
	return rootOpts.GetDefaultOneofSchemaMode(), nil
}

// objectOneofMarker holds OBJECT-mode oneOf-by-required constraints on a schema
// until the document is serialized. ogen cannot model an object that also carries
// a top-level oneOf (it builds a sum type and the required-only variants collide),
// so the constraint is stripped from the document fed to ogen and expanded into a
// real oneOf/allOf only in the public OpenAPI output. See finalizeObjectOneof.
const objectOneofMarker = "x-ogen-object-oneof"

// applyObjectOneofGroups records OBJECT-mode oneOf-by-required constraints on an
// object schema. A single oneof becomes a oneOf; multiple oneofs are combined
// under allOf so each is independently constrained.
func applyObjectOneofGroups(schema map[string]any, groups [][]any) {
	switch len(groups) {
	case 0:
		return
	case 1:
		schema[objectOneofMarker] = map[string]any{"oneOf": groups[0]}
	default:
		allOf := make([]any, 0, len(groups))
		for _, g := range groups {
			allOf = append(allOf, map[string]any{"oneOf": g})
		}
		schema[objectOneofMarker] = map[string]any{"allOf": allOf}
	}
}

// containsObjectOneof reports whether any schema in the document carries an
// OBJECT-mode oneof marker.
func containsObjectOneof(node any) bool {
	switch n := node.(type) {
	case map[string]any:
		if _, ok := n[objectOneofMarker]; ok {
			return true
		}
		for _, v := range n {
			if containsObjectOneof(v) {
				return true
			}
		}
	case []any:
		for _, v := range n {
			if containsObjectOneof(v) {
				return true
			}
		}
	}
	return false
}

// finalizeObjectOneof rewrites OBJECT-mode oneof markers throughout the document.
// When expand is true the marker is merged into its schema as a real oneOf/allOf
// (public output); otherwise it is dropped (the document fed to ogen).
func finalizeObjectOneof(node any, expand bool) {
	switch n := node.(type) {
	case map[string]any:
		if m, ok := n[objectOneofMarker]; ok {
			delete(n, objectOneofMarker)
			if expand {
				if constraint, ok := m.(map[string]any); ok {
					for k, v := range constraint {
						n[k] = v
					}
				}
			}
		}
		for _, v := range n {
			finalizeObjectOneof(v, expand)
		}
	case []any:
		for _, v := range n {
			finalizeObjectOneof(v, expand)
		}
	}
}

// deepCopyNode clones a decoded YAML/JSON node so the public and ogen documents
// can be finalized independently.
func deepCopyNode(n any) any {
	switch v := n.(type) {
	case map[string]any:
		m := make(map[string]any, len(v))
		for k, val := range v {
			m[k] = deepCopyNode(val)
		}
		return m
	case []any:
		s := make([]any, len(v))
		for i, val := range v {
			s[i] = deepCopyNode(val)
		}
		return s
	default:
		return v
	}
}

func getOneofOptions(oneof *protogen.Oneof) *ogen.OneofOptions {
	if opts, ok := oneof.Desc.Options().(*descriptorpb.OneofOptions); ok && proto.HasExtension(opts, ogen.E_Oneof) {
		if ext, ok := proto.GetExtension(opts, ogen.E_Oneof).(*ogen.OneofOptions); ok {
			return ext
		}
	}
	return nil
}

func getServiceOptions(svc *protogen.Service) *ogen.ServiceOptions {
	if opts, ok := svc.Desc.Options().(*descriptorpb.ServiceOptions); ok && proto.HasExtension(opts, ogen.E_Service) {
		if ext, ok := proto.GetExtension(opts, ogen.E_Service).(*ogen.ServiceOptions); ok {
			return ext
		}
	}
	return nil
}

func getMethodOptions(method *protogen.Method) *ogen.MethodOptions {
	if opts, ok := method.Desc.Options().(*descriptorpb.MethodOptions); ok && proto.HasExtension(opts, ogen.E_Method) {
		if ext, ok := proto.GetExtension(opts, ogen.E_Method).(*ogen.MethodOptions); ok {
			return ext
		}
	}
	return nil
}

// checkUniqueOperationIDs rejects duplicate operationId values across all merged
// path and webhook operations. ogen also enforces this when building its IR, but
// failing here yields a clearer message that names the colliding operations.
func checkUniqueOperationIDs(maps ...map[string]any) error {
	seen := map[string]string{} // operationId -> "METHOD name"
	check := func(m map[string]any) error {
		// Iterate deterministically so the reported collision is stable.
		names := make([]string, 0, len(m))
		for name := range m {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			item, ok := m[name].(map[string]any)
			if !ok {
				continue
			}
			methods := make([]string, 0, len(item))
			for method := range item {
				methods = append(methods, method)
			}
			sort.Strings(methods)
			for _, method := range methods {
				op, ok := item[method].(map[string]any)
				if !ok {
					continue
				}
				id, _ := op["operationId"].(string)
				if id == "" {
					continue
				}
				where := strings.ToUpper(method) + " " + name
				if prev, dup := seen[id]; dup {
					return fmt.Errorf("duplicate operationId %q on %s and %s", id, prev, where)
				}
				seen[id] = where
			}
		}
		return nil
	}
	for _, m := range maps {
		if err := check(m); err != nil {
			return err
		}
	}
	return nil
}

func mergeOperationMaps(dst, src map[string]any, kind string) error {
	for name, srcItemAny := range src {
		srcItem, ok := srcItemAny.(map[string]any)
		if !ok {
			dst[name] = srcItemAny
			continue
		}
		dstItem, _ := dst[name].(map[string]any)
		if dstItem == nil {
			dstItem = map[string]any{}
			dst[name] = dstItem
		}
		for method, op := range srcItem {
			if _, exists := dstItem[method]; exists {
				return fmt.Errorf("duplicate OpenAPI %s operation %s %s", kind, strings.ToUpper(method), name)
			}
			dstItem[method] = op
		}
	}
	return nil
}

// rejectUnsupportedStreaming fails fast if an unsupported streaming RPC is
// exposed via ogen. Server-streaming is mapped to SSE; client- and bidi-
// streaming still have no HTTP mapping here.
func (g *OpenAPIGenerator) rejectUnsupportedStreaming(file *protogen.File) {
	for _, svc := range file.Services {
		if opts := getServiceOptions(svc); opts != nil && opts.GetOmit() {
			continue
		}
		for _, method := range svc.Methods {
			mo := getMethodOptions(method)
			if mo == nil || mo.GetOmit() {
				continue
			}
			exposed := mo.GetHttpMethod() != ogen.HttpMethod_HTTP_METHOD_UNSPECIFIED || mo.GetWebhook().GetName() != ""
			if !exposed {
				continue
			}
			if method.Desc.IsStreamingClient() {
				g.errs = append(g.errs, fmt.Errorf(
					"method %s is a client-streaming RPC; protoc-gen-ogen supports only unary and server-streaming methods",
					method.Desc.FullName()))
			}
		}
	}
}

func bundleHasWebhooks(files []*protogen.File) bool {
	for _, file := range files {
		if fileHasWebhooks(file) {
			return true
		}
	}
	return false
}

func fileHasWebhooks(file *protogen.File) bool {
	for _, svc := range file.Services {
		if opts := getServiceOptions(svc); opts != nil && opts.GetOmit() {
			continue
		}
		for _, method := range svc.Methods {
			methodOpts := getMethodOptions(method)
			if methodOpts == nil || methodOpts.GetOmit() {
				continue
			}
			if methodOpts.GetWebhook().GetName() != "" {
				return true
			}
		}
	}
	return false
}

func openAPIVersion(opts *ogen.FileOptions, hasWebhooks bool) string {
	if opts.GetOpenapiVersion() != "" {
		return opts.GetOpenapiVersion()
	}
	if hasWebhooks {
		return "3.1.0"
	}
	return "3.0.3"
}

func componentNameFromFullName(name protoreflect.FullName) string {
	parts := strings.Split(string(name), ".")
	if len(parts) == 0 {
		return string(name)
	}
	return parts[len(parts)-1]
}

// reservedWKTSimpleNames are the Go-identifier simple names of the well-known
// types the generator renders inline (not as components). A user message taking
// one of these names would collide with the Go type/optional wrapper ogen derives
// for the corresponding WKT (e.g. Duration -> OptDuration).
var reservedWKTSimpleNames = map[string]bool{
	"Timestamp": true, "Duration": true, "FieldMask": true, "Empty": true,
	"Struct": true, "Any": true, "Value": true, "ListValue": true,
	"StringValue": true, "Int32Value": true, "Int64Value": true,
	"UInt32Value": true, "UInt64Value": true, "FloatValue": true,
	"DoubleValue": true, "BoolValue": true, "BytesValue": true,
}

func reservedWKTName(name string) bool { return reservedWKTSimpleNames[name] }

// qualifiedComponentName prefixes a message's simple name with its immediate
// qualifier (package last segment or enclosing message), e.g. schemapb.Duration
// -> SchemapbDuration.
func qualifiedComponentName(full protoreflect.FullName) string {
	parts := strings.Split(string(full), ".")
	simple := parts[len(parts)-1]
	if len(parts) < 2 {
		return simple
	}
	return capitalize(parts[len(parts)-2]) + simple
}

func uniqueComponentName(name string, existing map[protoreflect.FullName]string) string {
	used := map[string]bool{}
	for _, value := range existing {
		used[value] = true
	}
	if !used[name] {
		return name
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s%d", name, i)
		if !used[candidate] {
			return candidate
		}
	}
}

func messageOmitted(msg *protogen.Message) bool {
	opts := getMessageOptions(msg)
	return opts != nil && opts.GetOmit()
}

func fieldOmitted(field *protogen.Field) bool {
	opts := getFieldOptions(field)
	return opts != nil && opts.GetOmit()
}

func fieldRequired(field *protogen.Field) bool {
	if opts := getFieldOptions(field); opts != nil && opts.Required != nil && opts.GetRequired() {
		return true
	}
	return validateRequired(field)
}

func fieldOpenAPIName(field *protogen.Field) string {
	if opts := getFieldOptions(field); opts != nil && opts.GetName() != "" {
		return opts.GetName()
	}
	return jsonName(field)
}

func jsonName(field *protogen.Field) string {
	if name := field.Desc.JSONName(); name != "" {
		return name
	}
	return string(field.Desc.Name())
}

func findField(msg *protogen.Message, fieldPath string) *protogen.Field {
	if msg == nil || fieldPath == "" {
		return nil
	}
	current := msg
	var found *protogen.Field
	for _, part := range strings.Split(fieldPath, ".") {
		found = nil
		for _, field := range current.Fields {
			if string(field.Desc.Name()) == part || field.Desc.JSONName() == part || fieldOpenAPIName(field) == part {
				found = field
				break
			}
		}
		if found == nil {
			return nil
		}
		if found.Message != nil {
			current = found.Message
		}
	}
	return found
}

func enumSchema(field *protogen.Field) map[string]any {
	opts := getFieldOptions(field)
	asString := opts != nil && opts.GetEnumAsString()
	if asString {
		values := make([]string, 0, len(field.Enum.Values))
		for _, value := range field.Enum.Values {
			values = append(values, string(value.Desc.Name()))
		}
		return map[string]any{"type": "string", "enum": values}
	}
	values := make([]int32, 0, len(field.Enum.Values))
	for _, value := range field.Enum.Values {
		values = append(values, int32(value.Desc.Number()))
	}
	return map[string]any{"type": "integer", "format": "int32", "enum": values}
}

func wellKnownSchema(name protoreflect.FullName) map[string]any {
	switch name {
	case "google.protobuf.Timestamp":
		return map[string]any{"type": "string", "format": "date-time"}
	case "google.protobuf.Duration":
		return map[string]any{"type": "string", "format": "duration"}
	case "google.protobuf.FieldMask":
		return map[string]any{"type": "string", "format": "field-mask"}
	case "google.protobuf.StringValue":
		return map[string]any{"type": "string", "nullable": true}
	case "google.protobuf.Int32Value":
		return map[string]any{"type": "integer", "format": "int32", "nullable": true}
	case "google.protobuf.Int64Value":
		return map[string]any{"type": "string", "format": "int64", "nullable": true}
	case "google.protobuf.UInt32Value":
		return map[string]any{"type": "integer", "format": "int32", "minimum": 0, "nullable": true}
	case "google.protobuf.UInt64Value":
		return map[string]any{"type": "string", "format": "uint64", "nullable": true}
	case "google.protobuf.FloatValue":
		return map[string]any{"type": "number", "format": "float", "nullable": true}
	case "google.protobuf.DoubleValue":
		return map[string]any{"type": "number", "format": "double", "nullable": true}
	case "google.protobuf.BoolValue":
		return map[string]any{"type": "boolean", "nullable": true}
	case "google.protobuf.BytesValue":
		return map[string]any{"type": "string", "format": "byte", "nullable": true}
	case "google.protobuf.Empty":
		return map[string]any{"type": "object", "additionalProperties": false}
	// Struct/Value/ListValue/Any carry arbitrary JSON; emit a free-form schema so
	// ogen represents them as jx.Raw and the converters bridge via protojson.
	case "google.protobuf.Struct", "google.protobuf.Any", "google.protobuf.Value", "google.protobuf.ListValue":
		return map[string]any{}
	default:
		return nil
	}
}

func applySchemaOptions(schema map[string]any, opts *ogen.SchemaOptions) {
	if opts == nil {
		return
	}
	setString(schema, "title", opts.GetTitle())
	setString(schema, "description", opts.GetDescription())
	if opts.GetDefaultJson() != "" {
		schema["default"] = yamlRaw(opts.GetDefaultJson())
	}
	if len(opts.GetExamplesJson()) > 0 {
		examples := make([]any, 0, len(opts.GetExamplesJson()))
		for _, example := range opts.GetExamplesJson() {
			examples = append(examples, yamlRaw(example))
		}
		schema["examples"] = examples
	}
	if len(opts.GetEnumJson()) > 0 {
		values := make([]any, 0, len(opts.GetEnumJson()))
		for _, value := range opts.GetEnumJson() {
			values = append(values, yamlRaw(value))
		}
		schema["enum"] = values
	}
	if format := schemaFormat(opts); format != "" {
		schema["format"] = format
	}
	if opts.Nullable != nil {
		schema["nullable"] = opts.GetNullable()
	}
	if opts.ReadOnly != nil {
		schema["readOnly"] = opts.GetReadOnly()
	}
	if opts.WriteOnly != nil {
		schema["writeOnly"] = opts.GetWriteOnly()
	}
	applyExtensions(schema, opts.GetExtensions())
}

func schemaFormat(opts *ogen.SchemaOptions) string {
	if opts.GetCustomFormat() != "" {
		return opts.GetCustomFormat()
	}
	switch opts.GetStringFormat() {
	case ogen.StringFormat_STRING_FORMAT_BYTE:
		return "byte"
	case ogen.StringFormat_STRING_FORMAT_BINARY:
		return "binary"
	case ogen.StringFormat_STRING_FORMAT_DATE:
		return "date"
	case ogen.StringFormat_STRING_FORMAT_DATE_TIME:
		return "date-time"
	case ogen.StringFormat_STRING_FORMAT_TIME:
		return "time"
	case ogen.StringFormat_STRING_FORMAT_DURATION:
		return "duration"
	case ogen.StringFormat_STRING_FORMAT_UUID:
		return "uuid"
	case ogen.StringFormat_STRING_FORMAT_IP:
		return "ip"
	case ogen.StringFormat_STRING_FORMAT_IPV4:
		return "ipv4"
	case ogen.StringFormat_STRING_FORMAT_IPV6:
		return "ipv6"
	case ogen.StringFormat_STRING_FORMAT_URI:
		return "uri"
	case ogen.StringFormat_STRING_FORMAT_EMAIL:
		return "email"
	case ogen.StringFormat_STRING_FORMAT_HOSTNAME:
		return "hostname"
	case ogen.StringFormat_STRING_FORMAT_UNIX:
		return "unix"
	case ogen.StringFormat_STRING_FORMAT_UNIX_SECONDS:
		return "unix-seconds"
	case ogen.StringFormat_STRING_FORMAT_UNIX_MILLI:
		return "unix-milli"
	case ogen.StringFormat_STRING_FORMAT_UNIX_MICRO:
		return "unix-micro"
	case ogen.StringFormat_STRING_FORMAT_UNIX_NANO:
		return "unix-nano"
	case ogen.StringFormat_STRING_FORMAT_INT32:
		return "int32"
	case ogen.StringFormat_STRING_FORMAT_INT64:
		return "int64"
	default:
		return ""
	}
}

func yamlRaw(raw string) any {
	var out any
	if err := yaml.Unmarshal([]byte(raw), &out); err != nil {
		return raw
	}
	return out
}

func discriminatorObject(property string, mapping []*ogen.NamedString) map[string]any {
	out := map[string]any{"propertyName": property}
	if len(mapping) > 0 {
		values := map[string]any{}
		for _, item := range mapping {
			values[item.GetName()] = item.GetValue()
		}
		out["mapping"] = values
	}
	return out
}

func allOneOfVariantsAreRefs(variants []any) bool {
	if len(variants) == 0 {
		return false
	}
	for _, variant := range variants {
		schema, ok := variant.(map[string]any)
		if !ok {
			return false
		}
		if _, ok := schema["$ref"]; !ok {
			return false
		}
	}
	return true
}

func serversObject(servers []*ogen.Server) []any {
	out := make([]any, 0, len(servers))
	for _, server := range servers {
		item := map[string]any{"url": server.GetUrl()}
		setString(item, "description", server.GetDescription())
		applyExtensions(item, server.GetExtensions())
		out = append(out, item)
	}
	return out
}

func tagsObject(tags []*ogen.Tag) []any {
	out := make([]any, 0, len(tags))
	for _, tag := range tags {
		item := map[string]any{"name": tag.GetName()}
		setString(item, "description", tag.GetDescription())
		if externalDocs := externalDocsObject(tag.GetExternalDocs()); len(externalDocs) > 0 {
			item["externalDocs"] = externalDocs
		}
		out = append(out, item)
	}
	return out
}

func contactObject(contact *ogen.Contact) map[string]any {
	if contact == nil {
		return nil
	}
	out := map[string]any{}
	setString(out, "name", contact.GetName())
	setString(out, "url", contact.GetUrl())
	setString(out, "email", contact.GetEmail())
	return out
}

func licenseObject(license *ogen.License) map[string]any {
	if license == nil {
		return nil
	}
	out := map[string]any{}
	setString(out, "name", license.GetName())
	setString(out, "url", license.GetUrl())
	return out
}

func externalDocsObject(docs *ogen.ExternalDocs) map[string]any {
	if docs == nil {
		return nil
	}
	out := map[string]any{}
	setString(out, "url", docs.GetUrl())
	setString(out, "description", docs.GetDescription())
	return out
}

func headersObject(headers []*ogen.NamedString) map[string]any {
	out := map[string]any{}
	for _, header := range headers {
		out[header.GetName()] = map[string]any{
			"schema": map[string]any{"type": "string"},
		}
		if header.GetValue() != "" {
			out[header.GetName()].(map[string]any)["description"] = header.GetValue()
		}
	}
	return out
}

func applyExtensions(target map[string]any, extensions []*ogen.NamedString) {
	for _, ext := range extensions {
		if strings.HasPrefix(ext.GetName(), "x-") {
			target[ext.GetName()] = yamlRaw(ext.GetValue())
		}
	}
}

func setString(target map[string]any, key, value string) {
	if value != "" {
		target[key] = value
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func comments(c protogen.Comments) string {
	return strings.TrimSpace(string(c))
}

func firstCommentLine(c protogen.Comments) string {
	text := comments(c)
	if text == "" {
		return ""
	}
	return strings.Split(text, "\n")[0]
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

func joinHTTPPath(prefix, p string) string {
	if prefix == "" {
		return p
	}
	return "/" + strings.Trim(strings.TrimRight(prefix, "/")+"/"+strings.TrimLeft(p, "/"), "/")
}

func httpMethodName(method ogen.HttpMethod) string {
	switch method {
	case ogen.HttpMethod_HTTP_METHOD_GET:
		return "get"
	case ogen.HttpMethod_HTTP_METHOD_POST:
		return "post"
	case ogen.HttpMethod_HTTP_METHOD_PUT:
		return "put"
	case ogen.HttpMethod_HTTP_METHOD_PATCH:
		return "patch"
	case ogen.HttpMethod_HTTP_METHOD_DELETE:
		return "delete"
	case ogen.HttpMethod_HTTP_METHOD_HEAD:
		return "head"
	case ogen.HttpMethod_HTTP_METHOD_OPTIONS:
		return "options"
	case ogen.HttpMethod_HTTP_METHOD_TRACE:
		return "trace"
	default:
		return "get"
	}
}

func parameterLocationName(location ogen.ParameterLocation) string {
	switch location {
	case ogen.ParameterLocation_PARAMETER_LOCATION_PATH:
		return "path"
	case ogen.ParameterLocation_PARAMETER_LOCATION_QUERY:
		return "query"
	case ogen.ParameterLocation_PARAMETER_LOCATION_HEADER:
		return "header"
	case ogen.ParameterLocation_PARAMETER_LOCATION_COOKIE:
		return "cookie"
	default:
		return "query"
	}
}

func defaultStatusDescription(status uint32) string {
	switch status {
	case 0:
		return "Default response"
	case 200:
		return "OK"
	case 201:
		return "Created"
	case 204:
		return "No Content"
	case 400:
		return "Bad Request"
	case 401:
		return "Unauthorized"
	case 403:
		return "Forbidden"
	case 404:
		return "Not Found"
	default:
		return fmt.Sprintf("HTTP %d", status)
	}
}

func isEmptyMessage(msg *protogen.Message) bool {
	return msg == nil || msg.Desc.FullName() == "google.protobuf.Empty"
}

func defaultOperationGroup(svc *protogen.Service, tags []string) string {
	if len(tags) > 0 && tags[0] != "" {
		return pascalIdentifier(tags[0])
	}
	name := string(svc.Desc.Name())
	name = strings.TrimSuffix(name, "API")
	name = strings.TrimSuffix(name, "Service")
	return pascalIdentifier(name)
}

func pascalIdentifier(value string) string {
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == '-' || r == '_' || r == ' ' || r == '.' || r == '/'
	})
	var b strings.Builder
	for _, part := range parts {
		if part == "" {
			continue
		}
		b.WriteString(strings.ToUpper(part[:1]))
		if len(part) > 1 {
			b.WriteString(part[1:])
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return b.String()
}
