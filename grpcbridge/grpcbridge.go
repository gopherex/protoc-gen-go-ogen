// Package grpcbridge provides runtime helpers used by protoc-gen-ogen generated
// ogen<->gRPC adapters: HTTP status mapping, metadata propagation, and error
// detail extraction.
package grpcbridge

import (
	"context"
	"fmt"
	"net/http"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// HTTPStatus maps a gRPC status code to an HTTP status code, following the
// grpc-gateway convention.
func HTTPStatus(c codes.Code) int {
	switch c {
	case codes.OK:
		return http.StatusOK
	case codes.Canceled:
		return 499
	case codes.InvalidArgument, codes.FailedPrecondition, codes.OutOfRange:
		return http.StatusBadRequest
	case codes.DeadlineExceeded:
		return http.StatusGatewayTimeout
	case codes.NotFound:
		return http.StatusNotFound
	case codes.AlreadyExists, codes.Aborted:
		return http.StatusConflict
	case codes.PermissionDenied:
		return http.StatusForbidden
	case codes.Unauthenticated:
		return http.StatusUnauthorized
	case codes.ResourceExhausted:
		return http.StatusTooManyRequests
	case codes.Unimplemented:
		return http.StatusNotImplemented
	case codes.Unavailable:
		return http.StatusServiceUnavailable
	case codes.Internal, codes.Unknown, codes.DataLoss:
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}

// Status returns the *status.Status for an error (codes.Unknown when err is not
// a gRPC status error).
func Status(err error) *status.Status {
	return status.Convert(err)
}

// AppendIncomingMD appends key/value pairs to the incoming gRPC metadata in ctx
// so the wrapped gRPC implementation can read them via metadata.FromIncomingContext.
func AppendIncomingMD(ctx context.Context, kv ...string) context.Context {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		md = md.Copy()
	} else {
		md = metadata.MD{}
	}
	for i := 0; i+1 < len(kv); i += 2 {
		md.Append(kv[i], kv[i+1])
	}
	return metadata.NewIncomingContext(ctx, md)
}

// Details renders a status' detail messages as strings (best-effort), for error
// schemas that expose a details field.
func Details(st *status.Status) []string {
	details := st.Details()
	if len(details) == 0 {
		return nil
	}
	out := make([]string, 0, len(details))
	for _, d := range details {
		out = append(out, fmt.Sprintf("%v", d))
	}
	return out
}
