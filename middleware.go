package logging

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"time"

	"go.opentelemetry.io/otel/trace"
)

// Middleware is the HTTP logging middleware with default options.
// See NewMiddleware for configuration.
func Middleware(next http.Handler) http.Handler {
	return NewMiddleware()(next)
}

// NewMiddleware returns an HTTP middleware that:
//   - reads or generates a request ID and returns it in the response header;
//   - puts a request-scoped logger (request_id, method, endpoint,
//     remote_addr, trace_id, span_id) and the request ID into the context;
//   - logs one record per completed request with status, duration and bytes
//     (Error for 5xx and panics, Warn for 4xx and slow requests, Info otherwise).
//
// Place it after the OpenTelemetry middleware (otelhttp) so that trace IDs
// are available.
func NewMiddleware(opts ...MiddlewareOption) func(http.Handler) http.Handler {
	cfg := newMiddlewareConfig(opts)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ctx := r.Context()

			var incoming string
			if v := r.Header[cfg.requestIDHeader]; len(v) > 0 {
				incoming = v[0]
			}
			reqID := cfg.requestID(incoming)

			endpoint := r.URL.Path
			if cfg.logQuery && r.URL.RawQuery != "" {
				endpoint += "?" + r.URL.RawQuery
			}

			// One allocation holds both the lazy logger scope and the
			// response recorder.
			st := &httpRequestState{rec: responseRecorder{ResponseWriter: w, status: http.StatusOK}}
			sc := &st.scope
			cfg.newRequestScope(ctx, sc, reqID)
			attrs := append(sc.inline[:0],
				slog.String(requestIDLogKey, reqID),
				slog.String("method", r.Method),
				slog.String("endpoint", endpoint),
				slog.String("remote_addr", r.RemoteAddr),
			)
			sc.attrs = appendTraceAttrs(attrs, trace.SpanContextFromContext(ctx))

			ctx = withScope(ctx, sc)
			w.Header()[cfg.requestIDHeader] = []string{reqID}

			rec := &st.rec
			logDone := cfg.logCompletion && !cfg.skipped(r.URL.Path)

			defer func() {
				p := recover()
				if p == nil {
					if logDone {
						logHTTPCompletion(ctx, sc, cfg, rec, time.Since(start), nil)
					}
					return
				}
				if p == http.ErrAbortHandler {
					// Deliberate abort, not a bug: pass through without a stack.
					panic(p)
				}
				if !rec.wroteHeader {
					rec.status = http.StatusInternalServerError
				}
				logHTTPCompletion(ctx, sc, cfg, rec, time.Since(start), p)
				if !cfg.recoverPanics {
					panic(p)
				}
				if !rec.wroteHeader {
					http.Error(rec, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
				}
			}()

			next.ServeHTTP(rec, r.WithContext(ctx))
		})
	}
}

// httpRequestState groups per-request allocations into one.
type httpRequestState struct {
	scope scope
	rec   responseRecorder
}

func logHTTPCompletion(ctx context.Context, sc *scope, cfg *middlewareConfig,
	rec *responseRecorder, duration time.Duration, panicVal any,
) {
	slow := cfg.slowThreshold > 0 && duration >= cfg.slowThreshold

	var level slog.Level
	switch {
	case panicVal != nil || rec.status >= 500:
		level = slog.LevelError
	case rec.status >= 400 || slow:
		level = slog.LevelWarn
	default:
		if !cfg.sampled() {
			return
		}
		level = slog.LevelInfo
	}

	if !sc.enabled(ctx, level) {
		return
	}

	var buf [6]slog.Attr
	attrs := append(buf[:0],
		slog.Int("status", rec.status),
		slog.Duration("duration", duration),
		slog.Int64("bytes", rec.written),
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
	sc.logAttrs(ctx, level, "request completed", attrs...)
}

// responseRecorder captures the status code and response size while keeping
// optional interfaces (Flusher, Hijacker, ReaderFrom) and Unwrap working, so
// streaming, SSE, WebSockets and http.ResponseController are not broken.
type responseRecorder struct {
	http.ResponseWriter
	status      int
	written     int64
	wroteHeader bool
}

func (r *responseRecorder) WriteHeader(code int) {
	if r.wroteHeader {
		return // superfluous call; net/http would ignore it too
	}
	if code >= 100 && code < 200 && code != http.StatusSwitchingProtocols {
		// Informational responses (e.g. 103 Early Hints) are not final.
		r.ResponseWriter.WriteHeader(code)
		return
	}
	r.status = code
	r.wroteHeader = true
	r.ResponseWriter.WriteHeader(code)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.wroteHeader = true // implicit 200
	}
	n, err := r.ResponseWriter.Write(b)
	r.written += int64(n)
	return n, err
}

// ReadFrom keeps the sendfile/splice fast path of http.ServeContent.
func (r *responseRecorder) ReadFrom(src io.Reader) (int64, error) {
	if !r.wroteHeader {
		r.wroteHeader = true
	}
	var n int64
	var err error
	if rf, ok := r.ResponseWriter.(io.ReaderFrom); ok {
		n, err = rf.ReadFrom(src)
	} else {
		n, err = io.Copy(struct{ io.Writer }{r.ResponseWriter}, src)
	}
	r.written += n
	return n, err
}

// Flush implements http.Flusher. It is a no-op if the underlying writer
// cannot flush.
func (r *responseRecorder) Flush() {
	if !r.wroteHeader {
		r.wroteHeader = true
	}
	_ = http.NewResponseController(r.ResponseWriter).Flush()
}

// Hijack implements http.Hijacker (needed for WebSockets).
func (r *responseRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(r.ResponseWriter).Hijack()
	if err == nil && !r.wroteHeader {
		r.status = http.StatusSwitchingProtocols
		r.wroteHeader = true
	}
	return conn, rw, err
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (r *responseRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}
