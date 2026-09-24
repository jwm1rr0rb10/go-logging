package logging

import (
	"context"
	"log/slog"
)

type (
	ctxLoggerKey    struct{}
	ctxRequestIDKey struct{}
)

// ContextWithLogger returns a copy of ctx that carries l.
func ContextWithLogger(ctx context.Context, l *slog.Logger) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, ctxLoggerKey{}, l)
}

// L returns the logger stored in ctx, or the global default logger
// if ctx is nil or carries no logger.
func L(ctx context.Context) *slog.Logger {
	if ctx != nil {
		if l, ok := ctx.Value(ctxLoggerKey{}).(*slog.Logger); ok && l != nil {
			return l
		}
	}
	return slog.Default()
}

// ContextWithRequestID returns a copy of ctx that carries the request ID.
func ContextWithRequestID(ctx context.Context, id string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, ctxRequestIDKey{}, id)
}

// RequestIDFromContext returns the request ID set by the HTTP middleware or
// gRPC interceptors, or "" if there is none. Use it to propagate the ID to
// downstream calls.
func RequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(ctxRequestIDKey{}).(string)
	return id
}

// WithAttrs returns the logger from ctx enriched with attrs.
func WithAttrs(ctx context.Context, attrs ...Attr) *Logger {
	logger := L(ctx)
	if len(attrs) == 0 {
		return logger
	}
	args := make([]any, len(attrs))
	for i, a := range attrs {
		args[i] = a
	}
	return logger.With(args...) // a single With call: one handler clone
}

// ContextWithAttrs enriches the logger in ctx with attrs and returns the
// updated context. The returned context must be used, e.g.:
//
//	ctx = logging.ContextWithAttrs(ctx, logging.StringAttr("user_id", id))
func ContextWithAttrs(ctx context.Context, attrs ...Attr) context.Context {
	if len(attrs) == 0 {
		if ctx == nil {
			return context.Background()
		}
		return ctx
	}
	return ContextWithLogger(ctx, WithAttrs(ctx, attrs...))
}
