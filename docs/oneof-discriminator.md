# Oneof Discriminator Policy

`protoc-gen-ogen` must not silently emit OpenAPI that later makes `ogen` fail.

For protobuf `oneof`:

- Scalar-only or mixed scalar/message `oneof` must be emitted as plain `oneOf`
  without an explicit OpenAPI `discriminator`.
- `ogen` can handle primitive variants by JSON type discrimination.
- Explicit OpenAPI `discriminator` is allowed only when every `oneof` variant is
  emitted as a `$ref` object schema.
- If `(ogen.oneof).discriminator_property` is set and any variant is scalar or
  inline, the plugin must fail during OpenAPI generation with a clear message.

Recommended fix for users who need explicit discriminator on a mixed `oneof`:
wrap scalar variants in message types so every variant becomes an object schema.
