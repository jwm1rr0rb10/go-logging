package logging

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"go.opentelemetry.io/otel/trace"
)

// RequestTracker is the transport-agnostic core of NewMiddleware: request ID
// handling, the lazy request-scoped logger, sampling, slow request detection
// and the completion record. Use it to add logging to other transports; the
// gRPC interceptors in github.com/jwm1rr0rb10/go-logging/grpc/v2 are built
// on it.
//
// A RequestTracker is safe for concurrent use; create one per server.
type RequestTracker struct {
	cfg *middlewareConfig
}

// NewRequestTracker creates a tracker. It accepts the same options as
// NewMiddleware.
func NewRequestTracker(opts ...MiddlewareOption) *RequestTracker {
	return &RequestTracker{cfg: newMiddlewareConfig(opts)}
}

// RequestIDHeader returns the configured request ID header in canonical
// HTTP form (see WithRequestIDHeader). Lower-case it for gRPC metadata.
func (t *RequestTracker) RequestIDHeader() string { return t.cfg.requestIDHeader }

// RecoverPanics reports whether panics should be recovered instead of
// re-raised (see WithRecoverPanics).
func (t *RequestTracker) RecoverPanics() bool { return t.cfg.recoverPanics }

// Start begins tracking a request and returns the context with the
// request-scoped logger and request ID.
//
// name identifies the endpoint for WithSkipPaths (an HTTP path or a gRPC
// full method name). incomingID is the request ID received from the caller,
// or "" if there is none; it is validated and replaced with a generated one
// if needed. attrs are added to the request-scoped logger after request_id;
// trace_id and span_id follow if ctx carries a valid span.
func (t *RequestTracker) Start(ctx context.Context, name, incomingID string, attrs ...Attr) (context.Context, *TrackedRequest) {
	r := &TrackedRequest{}
	return t.cfg.start(ctx, r, name, incomingID, attrs...), r
}

// TrackedRequest is a request started with RequestTracker.Start.
type TrackedRequest struct {
	scope   scope
	cfg     *middlewareConfig
	ctx     context.Context
	start   time.Time
	traceID trace.TraceID
	logDone bool
}

func (c *middlewareConfig) start(ctx context.Context, r *TrackedRequest, name, incomingID string, attrs ...slog.Attr) context.Context {
	r.cfg = c
	r.start = time.Now()
	sc := &r.scope
	c.newRequestScope(ctx, sc, c.requestID(incomingID))

	all := append(append(sc.inline[:0], slog.String(requestIDLogKey, sc.id)), attrs...)
	span := trace.SpanContextFromContext(ctx)
	sc.attrs = appendTraceAttrs(all, span)
	if span.IsValid() {
		r.traceID = span.TraceID()
	}
	r.logDone = c.logCompletion && !c.skipped(name)
	r.ctx = withScope(ctx, sc)
	return r.ctx
}

// ID returns the request ID (incoming or generated).
func (r *TrackedRequest) ID() string { return r.scope.id }

// Finish writes the completion record.
//
// level is derived from the outcome by the caller (Info for success). It is
// raised to Warn for slow requests (see WithSlowThreshold) and to Error if
// panicVal is not nil. Records below Warn are subject to WithSampling.
// Requests excluded with WithSkipPaths or WithLogCompletion(false) are
// logged only if they panicked.
//
// Finish appends duration and, where applicable, slow, panic and stack to
// attrs. Call it once, after the request has been handled.
func (r *TrackedRequest) Finish(level Level, msg string, panicVal any, attrs ...Attr) {
	if !r.logDone && panicVal == nil {
		return
	}
	cfg := r.cfg
	duration := time.Since(r.start)
	slow := cfg.slowThreshold > 0 && duration >= cfg.slowThreshold

	if panicVal != nil {
		level = max(level, slog.LevelError)
	}
	if slow {
		level = max(level, slog.LevelWarn)
	}
	if level < slog.LevelWarn && !cfg.sampled(r.traceID) {
		return
	}
	sc := &r.scope
	if !sc.enabled(r.ctx, level) {
		return
	}

	var buf [10]slog.Attr
	all := append(append(buf[:0], attrs...), slog.Duration("duration", duration))
	if slow {
		all = append(all, slog.Bool("slow", true))
	}
	if panicVal != nil {
		all = append(all,
			slog.String("panic", fmt.Sprint(panicVal)),
			slog.String("stack", string(debug.Stack())),
		)
	}
	sc.logAttrs(r.ctx, level, msg, all...)
}
