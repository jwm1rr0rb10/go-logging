package logging

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"go.opentelemetry.io/otel/trace"
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
// It accepts the same options as NewMiddleware; WithSkipPaths takes full
// method names such as "/grpc.health.v1.Health/Check".
func UnaryServerInterceptor(opts ...MiddlewareOption) grpc.UnaryServerInterceptor {
	cfg := newMiddlewareConfig(opts)
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (resp any, err error) {
		start := time.Now()
		ctx, sc := enrichGRPCContext(ctx, cfg, info.FullMethod)

		defer func() {
			if p := recover(); p != nil {
				logGRPCCompletion(ctx, sc, cfg, codes.Internal, time.Since(start), p)
				if !cfg.recoverPanics {
					panic(p)
				}
				resp, err = nil, status.Error(codes.Internal, "internal error")
				return
			}
			if cfg.logCompletion && !cfg.skipped(info.FullMethod) {
				logGRPCCompletion(ctx, sc, cfg, status.Code(err), time.Since(start), nil)
			}
		}()

		return handler(ctx, req)
	}
}

// StreamServerInterceptor is the streaming counterpart of
// UnaryServerInterceptor. The call is logged when the stream handler returns.
func StreamServerInterceptor(opts ...MiddlewareOption) grpc.StreamServerInterceptor {
	cfg := newMiddlewareConfig(opts)
	return func(
		srv any,
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) (err error) {
		start := time.Now()
		ctx, sc := enrichGRPCContext(ss.Context(), cfg, info.FullMethod)

		defer func() {
			if p := recover(); p != nil {
				logGRPCCompletion(ctx, sc, cfg, codes.Internal, time.Since(start), p)
				if !cfg.recoverPanics {
					panic(p)
				}
				err = status.Error(codes.Internal, "internal error")
				return
			}
			if cfg.logCompletion && !cfg.skipped(info.FullMethod) {
				logGRPCCompletion(ctx, sc, cfg, status.Code(err), time.Since(start), nil)
			}
		}()

		return handler(srv, &wrappedServerStream{ServerStream: ss, ctx: ctx})
	}
}

// WithTraceIDInLogger returns a unary interceptor that only enriches the
// context logger, without completion logs.
//
// Deprecated: use UnaryServerInterceptor, which also logs completed calls
// and supports streaming via StreamServerInterceptor.
func WithTraceIDInLogger() grpc.UnaryServerInterceptor {
	return UnaryServerInterceptor(WithLogCompletion(false))
}

func enrichGRPCContext(ctx context.Context, cfg *middlewareConfig, fullMethod string) (context.Context, *scope) {
	var incoming string
	// ValueFromIncomingContext does not copy the whole metadata map.
	if v := metadata.ValueFromIncomingContext(ctx, cfg.requestIDMDKey); len(v) > 0 {
		incoming = v[0]
	}
	reqID := cfg.requestID(incoming)

	sc := &scope{}
	cfg.newRequestScope(ctx, sc, reqID)
	attrs := append(sc.inline[:0],
		slog.String(requestIDLogKey, reqID),
		slog.String("grpc_method", fullMethod),
	)
	sc.attrs = appendTraceAttrs(attrs, trace.SpanContextFromContext(ctx))

	return withScope(ctx, sc), sc
}

func logGRPCCompletion(ctx context.Context, sc *scope, cfg *middlewareConfig,
	code codes.Code, duration time.Duration, panicVal any,
) {
	slow := cfg.slowThreshold > 0 && duration >= cfg.slowThreshold

	level := grpcCodeLevel(code)
	if panicVal != nil {
		level = slog.LevelError
	}
	if slow && level < slog.LevelWarn {
		level = slog.LevelWarn
	}
	if level == slog.LevelInfo && !cfg.sampled() {
		return
	}
	if !sc.enabled(ctx, level) {
		return
	}

	var buf [5]slog.Attr
	attrs := append(buf[:0],
		slog.String("grpc_code", code.String()),
		slog.Duration("duration", duration),
	)
	if slow {
		attrs = append(attrs, slog.Bool("slow", true))
	}
	if panicVal != nil {
		attrs = append(attrs,
			slog.String("panic", fmt.Sprint(panicVal)),
			slog.String("stack", string(debug.Stack())),
		)
	}
	sc.logAttrs(ctx, level, "rpc completed", attrs...)
}

// grpcCodeLevel maps status codes to levels: server-side failures are
// errors, client-side problems are warnings.
func grpcCodeLevel(code codes.Code) slog.Level {
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
