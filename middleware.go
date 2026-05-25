package logging

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"

	tracing "github.com/jwm1rr0rb10/go-tracing"
)

const (
	requestIDLogKey = "request_id"
	traceIDLogKey   = "trace_id"
	spanIDLogKey    = "span_id"
)

// responseRecorder wraps http.ResponseWriter to capture status code and written bytes.
type responseRecorder struct {
	http.ResponseWriter
	status  int
	written int
}

func (r *responseRecorder) WriteHeader(statusCode int) {
	r.status = statusCode
	r.ResponseWriter.WriteHeader(statusCode)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	n, err := r.ResponseWriter.Write(b)
	r.written += n
	return n, err
}

// generateRequestID creates a secure random ID when none is provided.
func generateRequestID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ctx := r.Context()

		// Automatic X-Request-ID handling
		reqID := r.Header.Get("X-Request-ID")
		if reqID == "" {
			reqID = generateRequestID()
		}

		mLogger := L(ctx).With(
			slog.String(requestIDLogKey, reqID),
			slog.String("method", r.Method),
			slog.String("endpoint", r.URL.RequestURI()),
			slog.String("remote_addr", r.RemoteAddr),
		)

		// Add trace/span if present
		if span := trace.SpanContextFromContext(ctx); span.IsValid() {
			traceID := span.TraceID().String()
			spanID := span.SpanID().String()
			mLogger = mLogger.With(
				slog.String(traceIDLogKey, traceID),
				slog.String(spanIDLogKey, spanID),
			)
			tracing.TraceValue(ctx, traceIDLogKey, traceID)
		}

		// Update context and response writer
		ctx = ContextWithLogger(ctx, mLogger)
		rec := &responseRecorder{ResponseWriter: w, status: http.StatusOK}
		w.Header().Set("X-Request-ID", reqID) // propagate ID to client

		// Call next handler
		next.ServeHTTP(rec, r.WithContext(ctx))

		// Response logging.
		duration := time.Since(start)
		logAttrs := []any{
			slog.String(requestIDLogKey, reqID),
			slog.Int("status", rec.status),
			slog.Duration("duration", duration),
			slog.Int("bytes", rec.written),
		}

		// Choose log level based on status.
		switch {
		case rec.status >= 500:
			mLogger.Error("request completed", logAttrs...)
		case rec.status >= 400:
			mLogger.Warn("request completed", logAttrs...)
		default:
			mLogger.Info("request completed", logAttrs...)
		}
	})
}

// WithTraceIDInLogger (gRPC unchanged but kept consistent)
func WithTraceIDInLogger() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (resp interface{}, err error) {
		mLogger := L(ctx).With(slog.String("method", info.FullMethod))

		if span := trace.SpanContextFromContext(ctx); span.IsValid() {
			traceID := span.TraceID().String()
			spanID := span.SpanID().String()
			mLogger = mLogger.With(
				slog.String(traceIDLogKey, traceID),
				slog.String(spanIDLogKey, spanID),
			)
			tracing.TraceValue(ctx, traceIDLogKey, traceID)
		}

		ctx = ContextWithLogger(ctx, mLogger)
		return handler(ctx, req)
	}
}
