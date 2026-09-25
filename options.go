package logging

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/trace"
)

const (
	requestIDLogKey = "request_id"
	traceIDLogKey   = "trace_id"
	spanIDLogKey    = "span_id"

	defaultRequestIDHeader = "X-Request-ID"
	defaultMaxRequestIDLen = 128
)

// MiddlewareOption configures the HTTP middleware and the gRPC interceptors.
type MiddlewareOption func(*middlewareConfig)

type middlewareConfig struct {
	logger          *slog.Logger
	requestIDHeader string // canonical HTTP header key
	requestIDMDKey  string // lower-cased gRPC metadata key
	trustRequestID  bool
	maxRequestIDLen int
	sampleEvery     uint64
	slowThreshold   time.Duration
	skipPaths       map[string]struct{}
	logQuery        bool
	recoverPanics   bool
	logCompletion   bool

	counter atomic.Uint64
}

func newMiddlewareConfig(opts []MiddlewareOption) *middlewareConfig {
	cfg := &middlewareConfig{
		requestIDHeader: defaultRequestIDHeader,
		trustRequestID:  true,
		maxRequestIDLen: defaultMaxRequestIDLen,
		sampleEvery:     1,
		logCompletion:   true,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}
	// Canonicalize once: Header.Get/Set would otherwise allocate on every
	// request for non-canonical names such as "X-Request-ID".
	cfg.requestIDMDKey = strings.ToLower(cfg.requestIDHeader)
	cfg.requestIDHeader = http.CanonicalHeaderKey(cfg.requestIDHeader)
	return cfg
}

// WithMiddlewareLogger sets the base logger. By default the logger from the
// request context is used, falling back to the global default logger.
func WithMiddlewareLogger(l *slog.Logger) MiddlewareOption {
	return func(c *middlewareConfig) { c.logger = l }
}

// newRequestScope creates the request scope on top of the incoming context.
func (c *middlewareConfig) newRequestScope(ctx context.Context, s *scope, reqID string) {
	s.id = reqID
	s.base = c.logger
	if s.base == nil {
		s.parent = scopeFrom(ctx)
	}
}

// WithRequestIDHeader sets the request ID header (HTTP) or metadata key
// (gRPC, lower-cased automatically). Default: X-Request-ID.
// Also used by NewTransport and the gRPC client interceptors.
func WithRequestIDHeader(name string) MiddlewareOption {
	return func(c *middlewareConfig) {
		if name != "" {
			c.requestIDHeader = name
		}
	}
}

// WithTrustRequestID controls whether an incoming request ID is reused
// (true, default) or a new one is always generated. Disable it for
// public-facing services behind no trusted proxy.
func WithTrustRequestID(trust bool) MiddlewareOption {
	return func(c *middlewareConfig) { c.trustRequestID = trust }
}

// WithMaxRequestIDLength limits the accepted incoming request ID length
// (default 128). Longer IDs are replaced with a generated one.
func WithMaxRequestIDLength(n int) MiddlewareOption {
	return func(c *middlewareConfig) {
		if n > 0 {
			c.maxRequestIDLen = n
		}
	}
}

// WithSampling logs only every n-th successful request. Client errors,
// server errors, panics and slow requests are always logged.
// n <= 1 logs every request (default).
func WithSampling(n uint64) MiddlewareOption {
	return func(c *middlewareConfig) {
		if n < 1 {
			n = 1
		}
		c.sampleEvery = n
	}
}

// WithSlowThreshold logs requests slower than d at Warn level, bypassing
// sampling. Zero disables it (default).
func WithSlowThreshold(d time.Duration) MiddlewareOption {
	return func(c *middlewareConfig) { c.slowThreshold = d }
}

// WithSkipPaths disables completion logs for the given HTTP paths or gRPC
// full method names (e.g. "/healthz", "/grpc.health.v1.Health/Check").
// The request-scoped logger is still put into the context.
func WithSkipPaths(paths ...string) MiddlewareOption {
	return func(c *middlewareConfig) {
		if c.skipPaths == nil {
			c.skipPaths = make(map[string]struct{}, len(paths))
		}
		for _, p := range paths {
			c.skipPaths[p] = struct{}{}
		}
	}
}

// WithLogQuery includes the URL query string in the "endpoint" field.
// Disabled by default: query strings often contain tokens or personal data.
func WithLogQuery(enabled bool) MiddlewareOption {
	return func(c *middlewareConfig) { c.logQuery = enabled }
}

// WithRecoverPanics makes the middleware recover panics and answer with
// 500 (HTTP) or codes.Internal (gRPC). By default panics are logged and
// re-raised, so an outer recovery middleware keeps working.
func WithRecoverPanics(enabled bool) MiddlewareOption {
	return func(c *middlewareConfig) { c.recoverPanics = enabled }
}

// WithLogCompletion controls whether a record is written when a request
// completes (true by default). With false the middleware only enriches the
// context logger.
func WithLogCompletion(enabled bool) MiddlewareOption {
	return func(c *middlewareConfig) { c.logCompletion = enabled }
}

func (c *middlewareConfig) skipped(path string) bool {
	if c.skipPaths == nil {
		return false
	}
	_, ok := c.skipPaths[path]
	return ok
}

// sampled reports whether a successful, fast request should be logged.
// The counter is deterministic (exactly every n-th request) and is touched
// only for successful requests when sampling is enabled.
func (c *middlewareConfig) sampled() bool {
	if c.sampleEvery <= 1 {
		return true
	}
	return c.counter.Add(1)%c.sampleEvery == 1
}

// requestID returns a valid incoming ID or a newly generated one.
func (c *middlewareConfig) requestID(incoming string) string {
	if c.trustRequestID && validRequestID(incoming, c.maxRequestIDLen) {
		return incoming
	}
	return generateRequestID()
}

// validRequestID accepts non-empty IDs of printable, log-safe characters.
func validRequestID(id string, maxLen int) bool {
	if id == "" || len(id) > maxLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		ch := id[i]
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9':
		case ch == '-', ch == '_', ch == '.', ch == ':', ch == '/', ch == '+', ch == '=':
		default:
			return false
		}
	}
	return true
}

var fallbackIDCounter atomic.Uint64

// generateRequestID returns 16 random hex characters (one allocation).
func generateRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Practically unreachable; keep IDs unique within the process anyway.
		return strconv.FormatInt(time.Now().UnixNano(), 36) + "-" +
			strconv.FormatUint(fallbackIDCounter.Add(1), 36)
	}
	var h [16]byte
	hex.Encode(h[:], b[:])
	return string(h[:])
}

// appendTraceAttrs appends trace_id/span_id if ctx carries a valid span.
func appendTraceAttrs(attrs []slog.Attr, sc trace.SpanContext) []slog.Attr {
	if !sc.IsValid() {
		return attrs
	}
	return append(attrs,
		slog.String(traceIDLogKey, sc.TraceID().String()),
		slog.String(spanIDLogKey, sc.SpanID().String()),
	)
}
