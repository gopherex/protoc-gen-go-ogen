// Package convert provides runtime helpers used by protoc-gen-ogen generated
// proto<->ogen converters. Generated code imports these so the per-message
// converter functions stay small.
package convert

import (
	"strings"
	"time"

	"github.com/go-faster/jx"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Slice converts each element of in using fn. A nil input yields a nil slice.
func Slice[A, B any](in []A, fn func(A) B) []B {
	if in == nil {
		return nil
	}
	out := make([]B, len(in))
	for i, v := range in {
		out[i] = fn(v)
	}
	return out
}

// SliceErr is Slice for element converters that may fail.
func SliceErr[A, B any](in []A, fn func(A) (B, error)) ([]B, error) {
	if in == nil {
		return nil, nil
	}
	out := make([]B, len(in))
	for i, v := range in {
		b, err := fn(v)
		if err != nil {
			return nil, err
		}
		out[i] = b
	}
	return out, nil
}

// Map converts each value of in using fn, preserving keys.
func Map[K comparable, A, B any](in map[K]A, fn func(A) B) map[K]B {
	if in == nil {
		return nil
	}
	out := make(map[K]B, len(in))
	for k, v := range in {
		out[k] = fn(v)
	}
	return out
}

// MapErr is Map for value converters that may fail.
func MapErr[K comparable, A, B any](in map[K]A, fn func(A) (B, error)) (map[K]B, error) {
	if in == nil {
		return nil, nil
	}
	out := make(map[K]B, len(in))
	for k, v := range in {
		b, err := fn(v)
		if err != nil {
			return nil, err
		}
		out[k] = b
	}
	return out, nil
}

// TimeFromProto converts a protobuf Timestamp to time.Time (zero when nil).
func TimeFromProto(ts *timestamppb.Timestamp) time.Time {
	if ts == nil {
		return time.Time{}
	}
	return ts.AsTime()
}

// TimeToProto converts a time.Time to a protobuf Timestamp.
func TimeToProto(t time.Time) *timestamppb.Timestamp { return timestamppb.New(t) }

// DurationFromProto converts a protobuf Duration to time.Duration (0 when nil).
func DurationFromProto(d *durationpb.Duration) time.Duration {
	if d == nil {
		return 0
	}
	return d.AsDuration()
}

// DurationToProto converts a time.Duration to a protobuf Duration.
func DurationToProto(d time.Duration) *durationpb.Duration { return durationpb.New(d) }

// FieldMaskToString renders a protobuf FieldMask as comma-separated paths (empty
// when nil).
func FieldMaskToString(m *fieldmaskpb.FieldMask) string {
	if m == nil {
		return ""
	}
	return strings.Join(m.GetPaths(), ",")
}

// StringToFieldMask parses comma-separated paths into a protobuf FieldMask (nil
// when empty).
func StringToFieldMask(s string) *fieldmaskpb.FieldMask {
	if s == "" {
		return nil
	}
	return &fieldmaskpb.FieldMask{Paths: strings.Split(s, ",")}
}

// wktToJSON marshals a free-form JSON well-known type to its protojson form as
// jx.Raw. A nil message yields a nil raw (omitted from the ogen payload).
func wktToJSON(m proto.Message, isNil bool) (jx.Raw, error) {
	if isNil {
		return nil, nil
	}
	b, err := protojson.Marshal(m)
	if err != nil {
		return nil, err
	}
	return jx.Raw(b), nil
}

// isJSONNull reports whether a raw JSON value is absent or the literal null.
func isJSONNull(r jx.Raw) bool { return len(r) == 0 || string(r) == "null" }

// StructToJSON converts a protobuf Struct to protojson jx.Raw.
func StructToJSON(s *structpb.Struct) (jx.Raw, error) { return wktToJSON(s, s == nil) }

// JSONToStruct converts protojson jx.Raw back to a protobuf Struct.
func JSONToStruct(r jx.Raw) (*structpb.Struct, error) {
	if isJSONNull(r) {
		return nil, nil
	}
	v := &structpb.Struct{}
	if err := protojson.Unmarshal(r, v); err != nil {
		return nil, err
	}
	return v, nil
}

// ValueToJSON converts a protobuf Value to protojson jx.Raw.
func ValueToJSON(v *structpb.Value) (jx.Raw, error) { return wktToJSON(v, v == nil) }

// JSONToValue converts protojson jx.Raw back to a protobuf Value.
func JSONToValue(r jx.Raw) (*structpb.Value, error) {
	if isJSONNull(r) {
		return nil, nil
	}
	v := &structpb.Value{}
	if err := protojson.Unmarshal(r, v); err != nil {
		return nil, err
	}
	return v, nil
}

// ListValueToJSON converts a protobuf ListValue to protojson jx.Raw.
func ListValueToJSON(v *structpb.ListValue) (jx.Raw, error) { return wktToJSON(v, v == nil) }

// JSONToListValue converts protojson jx.Raw back to a protobuf ListValue.
func JSONToListValue(r jx.Raw) (*structpb.ListValue, error) {
	if isJSONNull(r) {
		return nil, nil
	}
	v := &structpb.ListValue{}
	if err := protojson.Unmarshal(r, v); err != nil {
		return nil, err
	}
	return v, nil
}

// AnyToJSON converts a protobuf Any to protojson jx.Raw. The Any's message type
// must be resolvable via the global type registry.
func AnyToJSON(a *anypb.Any) (jx.Raw, error) { return wktToJSON(a, a == nil) }

// JSONToAny converts protojson jx.Raw back to a protobuf Any.
func JSONToAny(r jx.Raw) (*anypb.Any, error) {
	if isJSONNull(r) {
		return nil, nil
	}
	v := &anypb.Any{}
	if err := protojson.Unmarshal(r, v); err != nil {
		return nil, err
	}
	return v, nil
}
