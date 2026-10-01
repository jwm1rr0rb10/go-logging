// Package logginggrpc provides gRPC server and client interceptors for
// github.com/jwm1rr0rb10/go-logging/v2: request IDs, trace correlation,
// completion records, sampling and panic logging.
//
// It is a separate module, so HTTP-only services do not depend on gRPC.
// The interceptors accept the options of the root package
// (logging.WithMiddlewareLogger, logging.WithSampling, ...).
package logginggrpc

import (
	"context"
	"log/slog"
	"strings"

	logging "github.com/jwm1rr0rb10/go-logging/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// UnaryServerInterceptor returns a gRPC unary interceptor that puts a
// request-scoped logger (request_id, grpc_method, trace_id, span_id) and the
// request ID into the context and logs one record per completed call with
// the status code and duration.
//
// It accepts the same options as logging.NewMiddleware; WithSkipPaths takes
// full method names such as "/grpc.health.v1.Health/Check".
func UnaryServerInterceptor(opts ...logging.MiddlewareOption) grpc.UnaryServerInterceptor {
	t := logging.NewRequestTracker(opts...)
	key := metadataKey(t)
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (resp any, err error) {
		ctx, tr := start(ctx, t, key, info.FullMethod)

		defer func() {
			if p := recover(); p != nil {
				finish(tr, codes.Internal, p)
				if !t.RecoverPanics() {
					panic(p)
				}
				resp, err = nil, status.Error(codes.Internal, "internal error")
				return
			}
			finish(tr, status.Code(err), nil)
		}()

		return handler(ctx, req)
	}
}

// StreamServerInterceptor is the streaming counterpart of
// UnaryServerInterceptor. The call is logged when the stream handler returns.
func StreamServerInterceptor(opts ...logging.MiddlewareOption) grpc.StreamServerInterceptor {
	t := logging.NewRequestTracker(opts...)
	key := metadataKey(t)
	return func(
		srv any,
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) (err error) {
		ctx, tr := start(ss.Context(), t, key, info.FullMethod)

		defer func() {
			if p := recover(); p != nil {
				finish(tr, codes.Internal, p)
				if !t.RecoverPanics() {
					panic(p)
				}
				err = status.Error(codes.Internal, "internal error")
				return
			}
			finish(tr, status.Code(err), nil)
		}()

		return handler(srv, &wrappedServerStream{ServerStream: ss, ctx: ctx})
	}
}

func metadataKey(t *logging.RequestTracker) string {
	return strings.ToLower(t.RequestIDHeader())
}

func start(ctx context.Context, t *logging.RequestTracker, key, fullMethod string) (context.Context, *logging.TrackedRequest) {
	var incoming string
	// ValueFromIncomingContext does not copy the whole metadata map.
	if v := metadata.ValueFromIncomingContext(ctx, key); len(v) > 0 {
		incoming = v[0]
	}
	return t.Start(ctx, fullMethod, incoming, slog.String("grpc_method", fullMethod))
}

func finish(tr *logging.TrackedRequest, code codes.Code, panicVal any) {
	tr.Finish(codeLevel(code), "rpc completed", panicVal, slog.String("grpc_code", code.String()))
}

// codeLevel maps status codes to levels: server-side failures are errors,
// client-side problems are warnings.
func codeLevel(code codes.Code) slog.Level {
	switch code {
	case codes.OK:
		return slog.LevelInfo
	case codes.Unknown, codes.Internal, codes.DataLoss, codes.Unavailable,
		codes.DeadlineExceeded, codes.Unimplemented:
		return slog.LevelError
	default:
		// Canceled, InvalidArgument, NotFound, AlreadyExists, PermissionDenied,
		// ResourceExhausted, FailedPrecondition, Aborted, OutOfRange,
		// Unauthenticated.
		return slog.LevelWarn
	}
}

// wrappedServerStream overrides Context so handlers see the enriched context.
type wrappedServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrappedServerStream) Context() context.Context { return w.ctx }
