package logging

import (
	"bufio"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
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
//   - logs one record per completed request with route, status, bytes and
//     duration (Error for 5xx and panics, Warn for 4xx and slow requests,
//     Info otherwise).
//
// Place it after the OpenTelemetry middleware (otelhttp) so that trace IDs
// are available.
func NewMiddleware(opts ...MiddlewareOption) func(http.Handler) http.Handler {
	cfg := newMiddlewareConfig(opts)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var incoming string
			if v := r.Header[cfg.requestIDHeader]; len(v) > 0 {
				incoming = v[0]
			}

			endpoint := r.URL.Path
			if cfg.logQuery && r.URL.RawQuery != "" {
				endpoint += "?" + r.URL.RawQuery
			}

			// One allocation holds both the tracked request (with its lazy
			// logger scope) and the response recorder.
			st := &httpRequestState{rec: responseRecorder{ResponseWriter: w, status: http.StatusOK}}
			ctx := cfg.start(r.Context(), &st.req, r.URL.Path, incoming,
				slog.String("method", r.Method),
				slog.String("endpoint", endpoint),
				slog.String("remote_addr", r.RemoteAddr),
			)
			w.Header()[cfg.requestIDHeader] = []string{st.req.ID()}

			rec := &st.rec
			r2 := r.WithContext(ctx)

			defer func() {
				p := recover()
				if err, ok := p.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					// Deliberate abort, not a bug: pass through without a stack.
					panic(p)
				}
				if p != nil && !rec.wroteHeader {
					rec.status = http.StatusInternalServerError
				}
				// http.ServeMux sets r2.Pattern ("GET /users/{id}") in place.
				logHTTPCompletion(&st.req, rec, r2.Pattern, p)
				if p == nil {
					return
				}
				if !cfg.recoverPanics {
					panic(p)
				}
				if !rec.wroteHeader {
					http.Error(rec, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
				}
			}()

			next.ServeHTTP(rec, r2)
		})
	}
}

// httpRequestState groups per-request allocations into one.
type httpRequestState struct {
	req TrackedRequest
	rec responseRecorder
}

func logHTTPCompletion(req *TrackedRequest, rec *responseRecorder, route string, panicVal any) {
	level := slog.LevelInfo
	switch {
	case panicVal != nil || rec.status >= 500:
		level = slog.LevelError
	case rec.status >= 400:
		level = slog.LevelWarn
	}
	status := slog.Int("status", rec.status)
	bytes := slog.Int64("bytes", rec.written)
	if route == "" {
		req.Finish(level, "request completed", panicVal, status, bytes)
		return
	}
	req.Finish(level, "request completed", panicVal, slog.String("route", route), status, bytes)
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
