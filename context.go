package logging

import (
	"context"
	"log/slog"
)

// ctxLogger is the key used to store the logger in the context.
type ctxLogger struct{}

// ContextWithLogger adds a logger to the context for request-scoped logging.
func ContextWithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxLogger{}, l)
}

// loggerFromContext retrieves the logger from the context or returns the default.
func loggerFromContext(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(ctxLogger{}).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}
