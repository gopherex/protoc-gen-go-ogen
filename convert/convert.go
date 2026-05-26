// Package convert provides runtime helpers used by protoc-gen-ogen generated
// proto<->ogen converters. Generated code imports these so the per-message
// converter functions stay small.
package convert

import (
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
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
