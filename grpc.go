package logging

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
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
		ctx, logger := enrichGRPCContext(ctx, cfg, info.FullMethod)

		defer func() {
			if p := recover(); p != nil {
				logGRPCCompletion(ctx, logger, cfg, info.FullMethod, codes.Internal, time.Since(start), p)
				if !cfg.recoverPanics {
					panic(p)
				}
				resp, err = nil, status.Error(codes.Internal, "internal error")
				return
			}
			if cfg.logCompletion && !cfg.skipped(info.FullMethod) {
				logGRPCCompletion(ctx, logger, cfg, info.FullMethod, status.Code(err), time.Since(start), nil)
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
		ctx, logger := enrichGRPCContext(ss.Context(), cfg, info.FullMethod)

		defer func() {
			if p := recover(); p != nil {
				logGRPCCompletion(ctx, logger, cfg, info.FullMethod, codes.Internal, time.Since(start), p)
				if !cfg.recoverPanics {
					panic(p)
				}
				err = status.Error(codes.Internal, "internal error")
				return
			}
			if cfg.logCompletion && !cfg.skipped(info.FullMethod) {
				logGRPCCompletion(ctx, logger, cfg, info.FullMethod, status.Code(err), time.Since(start), nil)
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

func enrichGRPCContext(ctx context.Context, cfg *middlewareConfig, fullMethod string) (context.Context, *slog.Logger) {
	var incoming string
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if v := md.Get(strings.ToLower(cfg.requestIDHeader)); len(v) > 0 {
			incoming = v[0]
		}
	}
	reqID := cfg.requestID(incoming)

	attrs := make([]any, 0, 4)
	attrs = append(attrs,
		slog.String(requestIDLogKey, reqID),
		slog.String("grpc_method", fullMethod),
	)
	attrs = appendTraceAttrs(attrs, trace.SpanContextFromContext(ctx))
	logger := cfg.baseLogger(L(ctx)).With(attrs...)

	ctx = ContextWithLogger(ctx, logger)
	ctx = ContextWithRequestID(ctx, reqID)
	return ctx, logger
}

func logGRPCCompletion(ctx context.Context, logger *slog.Logger, cfg *middlewareConfig,
	fullMethod string, code codes.Code, duration time.Duration, panicVal any,
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
	if !logger.Enabled(ctx, level) {
		return
	}

	attrs := make([]slog.Attr, 0, 5)
	attrs = append(attrs,
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
	logger.LogAttrs(ctx, level, "rpc completed", attrs...)
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
