package logginggrpc

import (
	"context"

	logging "github.com/jwm1rr0rb10/go-logging/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// UnaryClientInterceptor returns a gRPC client interceptor that propagates
// the request ID from the context (see logging.RequestIDFromContext) to
// outgoing metadata. A value that is already set is left untouched.
// Accepts logging.WithRequestIDHeader; other options are ignored.
func UnaryClientInterceptor(opts ...logging.MiddlewareOption) grpc.UnaryClientInterceptor {
	key := metadataKey(logging.NewRequestTracker(opts...))
	return func(ctx context.Context, method string, req, reply any,
		cc *grpc.ClientConn, invoker grpc.UnaryInvoker, callOpts ...grpc.CallOption,
	) error {
		return invoker(outgoingWithRequestID(ctx, key), method, req, reply, cc, callOpts...)
	}
}

// StreamClientInterceptor is the streaming counterpart of
// UnaryClientInterceptor.
func StreamClientInterceptor(opts ...logging.MiddlewareOption) grpc.StreamClientInterceptor {
	key := metadataKey(logging.NewRequestTracker(opts...))
	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn,
		method string, streamer grpc.Streamer, callOpts ...grpc.CallOption,
	) (grpc.ClientStream, error) {
		return streamer(outgoingWithRequestID(ctx, key), desc, cc, method, callOpts...)
	}
}

func outgoingWithRequestID(ctx context.Context, key string) context.Context {
	id := logging.RequestIDFromContext(ctx)
	if id == "" {
		return ctx
	}
	if md, ok := metadata.FromOutgoingContext(ctx); ok && len(md.Get(key)) > 0 {
		return ctx // set explicitly by the caller
	}
	return metadata.AppendToOutgoingContext(ctx, key, id)
}
