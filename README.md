# protoc-gen-ogen

A `protoc` plugin that turns annotated protobuf into a **REST/HTTP API** for an
existing gRPC service. In one `protoc` run it:

1. generates an **OpenAPI 3** document from your proto;
2. runs [`ogen`](https://github.com/ogen-go/ogen) **in-process** (as a pinned
   library, no separate invocation) to emit the Go HTTP client/server/types;
3. generates **converters** between the `protoc-gen-go` structs and the ogen
   types;
4. generates an **`OgenAdapter`** that implements the ogen `Handler` by
   delegating to your gRPC service implementation.

The result: you write normal gRPC services, annotate the proto files with HTTP
bindings, and get one typed REST server (params, validation, idempotency,
webhooks, file upload) without hand-writing any HTTP glue.

`protoc-gen-ogen` supports unary RPCs and server-streaming RPCs exposed as
OpenAPI Server-Sent Events (see [Streaming](#streaming)).

## Install

```bash
go install github.com/gopherex/protoc-gen-go-ogen@latest
```

It also runs alongside the protobuf Go plugins, so install those too:

```bash
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
```

All three must be on `PATH` (`$(go env GOBIN)` or `$(go env GOPATH)/bin`).

Add this module to your project — generated code imports its runtime packages
(`convert`, `grpcbridge`):

```bash
go get github.com/gopherex/protoc-gen-go-ogen
```

From source instead:

```bash
git clone https://github.com/gopherex/protoc-gen-go-ogen
cd protoc-gen-go-ogen
make build          # -> bin/protoc-gen-ogen, bin/protoc-gen-go, bin/protoc-gen-go-grpc
```

## Usage

Import the options and annotate your proto files. All files passed to `protoc`
with `option (ogen.file).generate_openapi = true` are bundled into one OpenAPI
document; document metadata and output settings come from the first marked file
in the `protoc` request.

```proto
import "ogen/ogen.proto";

option (ogen.file) = {
  generate_openapi: true
  generate_ogen: true
  generate_converters: true
  generate_grpc_adapter: true
  title: "My API"
  version: "1.0.0"
  ogen_target: "ogen"                                   // dir under --ogen_out
  ogen_package: "github.com/me/app/gen/ogen"
};

service UserAPI {
  rpc GetUser(GetUserRequest) returns (GetUserResponse) {
    option idempotency_level = NO_SIDE_EFFECTS;         // -> HTTP GET semantics
    option (ogen.method) = {
      http_method: HTTP_METHOD_GET
      path: "/users/{id}"
      parameters: { field_path: "id" in: PARAMETER_LOCATION_PATH required: true }
      responses: { status: 200 field_path: "user" }
    };
  }
}
```

### With protoc

`--go_out`, `--go-grpc_out`, and `--ogen_out` should point at the **same**
directory so the pb structs, converters, and ogen package resolve together.

```bash
protoc \
  -I . -I path/to/options \
  --go_out=gen --go_opt=paths=source_relative \
  --go-grpc_out=gen --go-grpc_opt=paths=source_relative \
  --ogen_out=gen --ogen_opt=paths=source_relative \
  --ogen_opt=ogen_config=ogen.yml \
  --ogen_opt=openapi_out=gen \
  api.proto admin.proto
```

If your proto uses `protoc-gen-validate` rules, also add its proto to the
include path:

```bash
  -I "$(go list -m -f '{{.Dir}}' github.com/envoyproxy/protoc-gen-validate)"
```

### With buf

```yaml
# buf.gen.yaml
version: v2
plugins:
  - local: protoc-gen-go
    out: gen
    opt: paths=source_relative
  - local: protoc-gen-go-grpc
    out: gen
    opt: paths=source_relative
  - local: protoc-gen-ogen
    out: gen
    opt:
      - paths=source_relative
      - ogen_config=ogen.yml
      - openapi_out=gen
```

### Plugin options (`--ogen_opt=key=value`)

| option | default | meaning |
|---|---|---|
| `ogen_config` | `""` | path to an ogen config (`ogen.yml`); loaded into ogen's `gen.Options` |
| `openapi_out` | `""` | directory to write the generated `openapi.yaml` to; empty = don't write it to disk |
| `paths`, `module` | — | standard `protoc-gen-go` path options |

### In Go

Wire your gRPC implementation into the generated adapter and serve it with ogen:

```go
import (
    "net/http"
    app "github.com/me/app/gen"          // pb package: structs, converters, OgenAdapter
    ogenapi "github.com/me/app/gen/ogen" // ogen client/server
)

adapter := app.NewOgenAdapter(myUserAPIServer /*, otherServers... */)
srv, err := ogenapi.NewServer(adapter)
if err != nil { /* ... */ }
http.ListenAndServe(":8080", srv)
```

Converters are available directly on the protobuf types:

```go
o, err := pbUser.ToOgen()          // *pb.User  -> *ogen.User
back, err := app.UserFromOgen(o)   // *ogen.User -> *pb.User
```

## What gets generated

One `protoc` run produces, all in `--ogen_out`:

- `api.pb.go`, `api_grpc.pb.go` — standard `protoc-gen-go` / `protoc-gen-go-grpc`.
- `openapi.yaml` — one OpenAPI document containing every marked service (only
  when `openapi_out` is set).
- `<ogen_target>/` — the ogen package (client, server, types, validators).
- `*.converters.go` — `ToOgen`/`FromOgen` between pb and ogen types, emitted
  next to each marked proto package.
- `*.ogenadapter.go` — one `OgenAdapter` per marked Go package implementing the
  ogen `Handler` (and `WebhookHandler`) over the gRPC service stubs. Aggregate
  adapter generation currently requires all marked files to share one
  `go_package`.
- `*.converters_test.go` — faker round-trip tests (when ogen's
  `debug/example_tests` feature is enabled).

## Proto options

Options live in `ogen/ogen.proto`:

- `ogen.file` — document metadata, servers, tags; toggles `generate_openapi`,
  `generate_ogen`, `generate_converters`, `generate_grpc_adapter`; ogen output
  dir/package (`ogen_target`, `ogen_package`, `ogen_package_name`); output paths.
- `ogen.service` — path prefix, tags, service-level servers.
- `ogen.method` — HTTP method, path, parameter bindings, request body,
  responses, webhook.
- `ogen.message` — schema name, schema metadata, additional properties,
  discriminator.
- `ogen.field` — property name, required override, format, enum string/int,
  `x-ogen-properties` Go name.
- `ogen.oneof` — `oneOf`/`anyOf`/`object` mode and discriminator. `object` mode
  emits the protojson form (one property per branch + `oneOf`-by-required), the
  correct shape when branches share a JSON type (e.g. `{id|slug}`).
- `ogen.file.default_oneof_schema_mode` — bundle-wide default mode for oneofs
  with no `(ogen.oneof)` option, including oneofs from imported/vendored protos
  (e.g. `schemapb.Field.kind`) that can't be annotated. Set to
  `ONEOF_SCHEMA_MODE_OBJECT` to make every such oneof protojson form. A per-oneof
  `schema_mode` still overrides it. The plugin flag
  `--ogen_opt=default_oneof_schema_mode=object` (`one_of|any_of|object`)
  overrides the file option for the whole protoc invocation.

Field locations are explicit: `parameters[].field_path` (+ `in:`
PATH/QUERY/HEADER/COOKIE), `request_body.field_path`, and `responses[].field_path`
map protobuf fields onto HTTP locations (the grpc-gateway model). The gRPC
implementation never sees HTTP params — the generated adapter reassembles the
request message and mirrors header params into gRPC incoming metadata.

## Features

**Validation (PGV).** `protoc-gen-validate` field rules are translated into
OpenAPI schema constraints (numeric ranges → `minimum`/`maximum`, string
`min_len`/`pattern` → `minLength`/`pattern`, `repeated`/`map` sizes, enum
filtering, `message.required`, well-known string formats). ogen then generates
the matching validators. The validate proto is read via the go-get'd module, not
imported into the plugin build.

**Idempotency.** The builtin `idempotency_level` becomes an
`x-idempotency-level` extension; `IDEMPOTENT` operations get an optional
`Idempotency-Key` (uuid) header; `NO_SIDE_EFFECTS` is validated to use a safe
HTTP method.

**Converters.** `func (x *Msg) ToOgen() (*ogen.T, error)` and
`func MsgFromOgen(*ogen.T) (*Msg, error)`, emitted into the pb package, both
returning an error. Built from ogen's IR for exact type/field names; nested
structs, enums, oneof↔sum, maps, repeated, well-known types, `uuid`/`uri`/`ip`
formats, and multipart file bytes are handled. Slice/map/timestamp bridging uses
the `convert` runtime package.

**gRPC adapter.** `OgenAdapter` implements the ogen `Handler` (and
`WebhookHandler`) by delegating to the gRPC `<Service>Server` implementations.
gRPC errors are mapped in `NewError`: `status.Code` → HTTP status (grpc-gateway
table), with code/message/details unpacked into the error schema. Runtime
helpers are in the `grpcbridge` package.

**Webhooks, file upload.** OpenAPI 3.1 webhooks via `ogen.method.webhook`; raw
binary and `multipart/form-data` uploads (file parts read into `bytes`).

## Streaming

Server-streaming RPCs exposed through `ogen.method` are emitted as
`text/event-stream` OpenAPI responses. The generated gRPC adapter returns an
ogen `io.Reader` response and serializes each protobuf response message as one
SSE `data: <protojson>` event.

Client-streaming and bidirectional streaming RPCs exposed through `ogen.method`
are rejected with a clear error. Streaming RPCs without an `ogen.method` binding
are ignored. (`x-ogen-json-streaming` is still supported as an ogen JSON
streaming media-type extension; it is separate from SSE.)

## Type mapping

| proto | OpenAPI / ogen |
|---|---|
| `int32`/`uint32`/`sint32`/`fixed32` | `integer` `int32` (unsigned adds `minimum: 0`) |
| `int64`/`uint64`/`sint64`/`fixed64` | `integer` `int64` |
| `float`/`double` | `number` `float`/`double` |
| `bool`/`string` | `boolean`/`string` |
| `bytes` | `string` `byte` (`binary` with `STRING_FORMAT_BINARY`) |
| `repeated` | array · `map<K,V>` → object `additionalProperties` |
| message | reusable component schema |
| enum | integer enum (string enum with `enum_as_string`) |
| `oneof` | `oneOf` (or `anyOf`, or `object` protojson form); optional discriminator |
| Timestamp/Duration | `string` `date-time`/`duration` |
| wrapper types | nullable scalar · Struct/Any → object · Value → any |

## Development

```bash
make build           # build the three plugins into bin/
make gen-test        # run protoc against the multi-file example (full pipeline)
go test ./...        # plugin + generated example
cd example/gen/ogen && go test ./...
make gen-opts        # regenerate ogen/ogen.pb.go from ogen/ogen.proto (easyp)
```

Repository layout:

- `main.go`, `generator/` — the plugin. `openapi.go` (OpenAPI emission,
  idempotency, SSE streaming guard), `validate.go` (PGV → constraints), `ogen_run.go`
  (in-process ogen), `converters*.go` (proto↔ogen), `adapter.go` (gRPC adapter).
- `ogen/` — `ogen.proto` options and generated `ogen.pb.go`.
- `convert/`, `grpcbridge/` — runtime packages imported by generated code.
- `example/golden.proto`, `example/admin.proto` — multi-file fixture exercising
  aggregation plus scalars, optional, repeated, map, enum, oneof, WKT, PGV
  validation, idempotency, webhooks, file upload. Generated output under
  `example/gen/` is rebuilt by `make gen-test`.

## Scope

Unary REST over gRPC only. Streaming (client/server/bidi) is out of scope and
rejected — it belongs to a separate GraphQL/WebSocket generator. Validation is
read from `protoc-gen-validate`; other validation dialects are not yet wired.

## License

See [LICENSE](LICENSE).
