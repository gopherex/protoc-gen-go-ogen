// Package grpcbridge provides runtime helpers used by protoc-gen-ogen generated
// ogen<->gRPC adapters: HTTP status mapping, metadata propagation, and error
// detail extraction.
package grpcbridge

import (
	"bytes"
	"context"
	"io"
	"net/http"

	"github.com/go-faster/jx"
	ht "github.com/ogen-go/ogen/http"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// ReadMultipart reads an uploaded multipart file into a byte slice.
func ReadMultipart(f ht.MultipartFile) ([]byte, error) {
	if f.File == nil {
		return nil, nil
	}
	return io.ReadAll(f.File)
}

// BytesMultipart wraps raw bytes as a multipart file for outgoing requests.
func BytesMultipart(b []byte) ht.MultipartFile {
	return ht.MultipartFile{File: bytes.NewReader(b), Size: int64(len(b))}
}

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

// Details renders a status' detail messages as protojson values, each wrapped in
// a google.protobuf.Any so the "@type" URL is preserved. Non-message or
// unmarshalable details are skipped. The result feeds an error schema's details
// array (ogen []jx.Raw).
func Details(st *status.Status) []jx.Raw {
	details := st.Details()
	if len(details) == 0 {
		return nil
	}
	out := make([]jx.Raw, 0, len(details))
	for _, d := range details {
		m, ok := d.(proto.Message)
		if !ok {
			continue
		}
		any, err := anypb.New(m)
		if err != nil {
			continue
		}
		b, err := protojson.Marshal(any)
		if err != nil {
			continue
		}
		out = append(out, jx.Raw(b))
	}
	return out
}
