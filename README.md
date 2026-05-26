# protoc-gen-ogen

`protoc-gen-ogen` generates an OpenAPI document from protobuf descriptors. The
generated OpenAPI document is intended to be passed to
[`ogen`](https://github.com/ogen-go/ogen) to generate Go HTTP client/server
types. Converter generation between protobuf and ogen types is the next layer:

```go
proto.Target.ToOgen() ogen.Target
FromOgen(ogen.Target) proto.Target
```

## Current Pipeline

1. User defines protobuf messages and services.
2. `protoc-gen-ogen` reads protobuf descriptors and `ogen/ogen.proto` options.
3. The plugin builds the OpenAPI document in memory.
4. When `ogen.file.generate_ogen` is set, the plugin runs `ogen` **in-process**
   (as a library, pinned through `go.mod`) and emits the generated Go
   client/server/types through the protoc response.
5. Converter generation will use the same descriptor mapping decisions.

A single `protoc` run now produces both the OpenAPI document and the generated
ogen Go code; there is no separate `ogen` invocation.

Golden generation:

```sh
make gen-test
```

Verification:

```sh
go test ./...
cd example/gen/ogen && go test ./...
```

## End-to-End Test Path

The full tested path is:

1. Build the local protoc plugin:

   ```sh
   go build -o ./bin/protoc-gen-ogen ./
   ```

2. Run `protoc` against the golden protobuf file with the local plugin. The
   plugin reads the ogen config, builds the OpenAPI document, runs ogen
   in-process, and writes both outputs:

   ```sh
   protoc \
     -I . \
     -I ./example \
     -I "$(go list -m -f '{{.Dir}}' github.com/envoyproxy/protoc-gen-validate)" \
     --plugin=protoc-gen-ogen=./bin/protoc-gen-ogen \
     --ogen_out=./example/gen \
     --ogen_opt=paths=source_relative \
     --ogen_opt=ogen_config=./example/ogen.yml \
     --ogen_opt=openapi_out=./example/gen \
     ./example/golden.proto
   ```

   This produces `example/gen/openapi.yaml` and the ogen package under
   `example/gen/ogen`.

3. Compile and test both the plugin repo and the generated ogen package:

   ```sh
   go test ./...
   cd example/gen/ogen && go test ./...
   ```

`make gen-test` runs steps 1-2. The final `go test` commands verify that the
generated OpenAPI is accepted by ogen and that the generated Go code compiles,
including webhook client/server output. The PGV `validate.proto` include is
needed because the golden proto uses `(validate.rules)` options.

## Proto Options

Generation options live in `ogen/ogen.proto`.

Current option levels:

- `ogen.file`: OpenAPI document metadata, servers, tags, output paths, ogen
  generation toggle (`generate_ogen`), ogen target dir (`ogen_target`, relative
  to `--ogen_out`), ogen Go import path (`ogen_package`) and short package name
  (`ogen_package_name`, defaults to the last segment of `ogen_package`).
- `ogen.service`: path prefix, tags, service-level servers.
- `ogen.method`: HTTP binding, parameters, request body, responses, webhooks.
- `ogen.message`: schema naming, schema metadata, additional properties.
- `ogen.field`: field/property naming, required override, format override,
  enum string/int mapping, `x-ogen-properties` Go name override.
- `ogen.oneof`: `oneOf`/`anyOf` mode and discriminator metadata.

Validation options are intentionally not duplicated in `ogen/ogen.proto`.
Validation is read from `protoc-gen-validate` (PGV) field rules and translated
into OpenAPI schema constraints. See `## Validation`.

## Plugin Options

Plugin-level options are passed through `--ogen_opt=key=value` (repeatable, or
comma-separated). They are CLI/build concerns, distinct from the per-file
`ogen.file` proto options:

- `ogen_config`: path to an ogen config file (`ogen.yml`), loaded into ogen's
  `gen.Options`. One config applies to every generated spec. Empty means ogen
  defaults.
- `openapi_out`: directory to write generated `openapi.yaml` file(s) to. Empty
  means the OpenAPI document is not written to disk separately.

Output rules per protobuf file:

- `generate_ogen` set: the plugin runs ogen in-process and emits the Go package
  under `ogen_target` (relative to `--ogen_out`).
- `openapi_out` set: the OpenAPI document is also written to that directory.
- Neither set: the OpenAPI document is emitted through protoc under `--ogen_out`
  so the plugin still produces output.

The standard `paths` and `module` protoc-gen parameters are also honored.

## OpenAPI Generation

The generator currently emits OpenAPI YAML and covers the golden example in
`example/golden.proto`.

Implemented protobuf mapping:

- Scalar numbers map to OpenAPI `number`/`integer` with `float`, `double`,
  `int32`, or `int64` formats.
- Unsigned protobuf integers add `minimum: 0`.
- `bool`, `string`, and `bytes` map to boolean/string schemas.
- `bytes` defaults to `format: byte`; `STRING_FORMAT_BINARY` maps it to binary
  file/body schemas.
- `repeated` fields map to arrays.
- `map<K,V>` fields map to objects with `additionalProperties`.
- Messages map to reusable component schemas.
- Enums map to integer enums by default, or string enums with
  `enum_as_string`.
- Well-known types map to OpenAPI-friendly schemas:
  `Timestamp` -> `string/date-time`, `Duration` -> `string/duration`,
  wrapper types -> nullable scalar schemas, `Struct`/`Any` -> arbitrary object,
  `Value` -> any schema, `ListValue` -> array of any.

Response status `0` means OpenAPI `default` response. Empty protobuf responses
do not emit response body content.

## Validation

The generator reads [`protoc-gen-validate`](https://github.com/bufbuild/protoc-gen-validate)
(PGV) field rules from the `(validate.rules)` extension and translates them into
OpenAPI schema constraints. ogen then generates validation code on the generated
ogen types; proto-side validation stays the job of a separate PGV plugin.

The PGV proto is not imported into the generator build. The generator depends on
the PGV Go module (`github.com/envoyproxy/protoc-gen-validate`) only to read the
extension descriptors. The proto file that *uses* PGV options must still import
`validate/validate.proto`, so `protoc` needs that file on its include path (the
golden `make gen-test` adds it via `go list -m`).

Mapping (`generator/validate.go`):

- Numeric (`int*`/`uint*`/`sint*`/`fixed*`/`float`/`double`): `gte`/`lte` ->
  `minimum`/`maximum`; `gt`/`lt` -> exclusive bounds; `const` and `in` -> `enum`.
- String: `len`/`min_len`/`max_len` -> `minLength`/`maxLength`; `pattern` ->
  `pattern`; `prefix`/`suffix`/`contains` -> a single anchored `pattern`;
  `const`/`in` -> `enum`; well-known formats (`email`, `uuid`, `uri`, `uri_ref`,
  `ipv4`, `ipv6`, `ip`, `hostname`) -> `format`.
- Bytes: `len`/`min_len`/`max_len` -> `minLength`/`maxLength`; `pattern` ->
  `pattern` (best effort on the encoded string).
- Repeated: `min_items`/`max_items`/`unique` -> `minItems`/`maxItems`/
  `uniqueItems`; `items` rules apply to the array item schema.
- Map: `min_pairs`/`max_pairs` -> `minProperties`/`maxProperties`; `values`
  rules apply to `additionalProperties`; `keys` rules map to `propertyNames`
  on OpenAPI 3.1 only.
- Enum: `const`/`in`/`not_in` filter the emitted `enum` values, preserving the
  string or integer representation; `defined_only` is a no-op (only defined
  values are emitted anyway).
- `message.required` marks the property as `required`.

Exclusive numeric bounds account for the OpenAPI version: `3.0.x` uses
`minimum` + `exclusiveMinimum: true`, `3.1.x` uses the numeric
`exclusiveMinimum`.

Rules with no native OpenAPI representation are skipped on purpose: `not_in`
(numeric/string), byte-length rules on strings (`len_bytes`/`min_bytes`/
`max_bytes`), `not_contains`, `string.address`, and `timestamp`/`duration`
range rules.

## Oneof Policy

`oneof` fields are emitted as `oneOf` by default and can be switched to `anyOf`.

Explicit discriminator support is fail-fast: if
`discriminator_property` is set, every oneof variant must be an object schema
reference. Mixed scalar/object oneofs cannot safely carry a discriminator, so
generation fails with a clear error instead of letting ogen fail later.

See `docs/oneof-discriminator.md`.

## Ogen Extensions

The generator supports generic `x-*` extensions through `NamedString` raw YAML
values.

Covered extension use cases:

- `x-ogen-name` on schemas.
- `x-ogen-server-name` on servers.
- `x-ogen-properties` through `FieldOptions.go_name`.
- `x-ogen-json-streaming` on JSON request/response media types.
- Operation grouping through `x-ogen-operation-group`.

Operation groups are automatic unless explicitly overridden:

1. Existing `x-ogen-operation-group` extension wins.
2. First operation tag is converted to PascalCase.
3. Service name is used with common `API`/`Service` suffixes trimmed.

## File Uploads

Raw upload bodies are represented with binary string schemas, for example a
`bytes` field with `STRING_FORMAT_BINARY` and request content type such as
`image/png`.

Multipart upload uses `multipart/form-data`; binary `bytes` fields become file
parts in generated ogen code. The golden example covers both raw upload and
multipart upload.

## JSON Streaming

`x-ogen-json-streaming` is supported as a media type extension for JSON bodies.
This is ogen's streaming JSON feature. It is not SSE and does not describe a
WebSocket or bidirectional stream.

SSE and WebSocket are not implemented as protobuf streaming mappings yet.
Current policy from investigation:

- `webhooks` are regular HTTP callbacks, not WebSocket/SSE.
- WebSocket should not be represented through OpenAPI/ogen paths silently.
- SSE through `text/event-stream` is not first-class in ogen; a raw/text stream
  workaround would not provide SSE event semantics.

## Webhooks

OpenAPI webhooks are supported through method options. A webhook is a regular
HTTP callback operation in the top-level OpenAPI 3.1 `webhooks` object.

Mark a protobuf method with `webhook.name` to put it into `webhooks` instead of
`paths`:

```proto
service WebhookAPI {
  rpc UserChanged(UserChangedEvent) returns (google.protobuf.Empty) {
    option (ogen.method) = {
      http_method: HTTP_METHOD_POST
      operation_id: "userChangedWebhook"
      summary: "Receive user change webhook"
      webhook: {
        name: "userChanged"
      }
      parameters: {
        field_path: "delivery_id"
        in: PARAMETER_LOCATION_HEADER
        name: "X-Webhook-Delivery"
        required: true
        schema: {
          string_format: STRING_FORMAT_UUID
        }
      }
      request_body: {
        required: true
        field_path: "event"
      }
      responses: {
        status: 204
        description: "Webhook accepted."
      }
    };
  }
}
```

When any webhook is present, the generator defaults the OpenAPI version to
`3.1.0`, because ogen requires OpenAPI 3.1 for the top-level `webhooks` object.
If `openapi_version` is explicitly set to `3.0.x` while webhooks are present,
generation fails with a clear error.

Webhook methods must not use path parameters. Query, header, and cookie
parameters are allowed.

With `webhooks/client` and `webhooks/server` enabled, ogen generates:

- `WebhookClient`
- `WebhookHandler`
- `NewWebhookServer`
- webhook operation methods such as `UserChangedWebhook`

For `ogen v1.20.3`, the golden config disables `client/editors`. That feature is
currently incompatible with `webhooks/client`: ogen generates calls to
`WebhookClient.onRequest` and `WebhookClient.onResponse`, but does not generate
those methods on `WebhookClient`.

## Golden Example

The golden fixture is `example/golden.proto`.

It covers:

- regular path operations;
- request parameters and request bodies;
- default and explicit responses;
- scalar, optional, repeated, map, enum, nested message, oneof, and WKT schemas;
- PGV validation constraints (numeric range, string length/pattern/format,
  repeated items/unique, map size, enum filtering);
- OpenAPI/ogen extensions;
- automatic operation groups;
- raw and multipart file upload;
- JSON streaming extension output;
- OpenAPI webhooks.

Generated outputs under `example/gen/` are ignored by git and regenerated by
`make gen-test` or `make gen-ogen-test`.
