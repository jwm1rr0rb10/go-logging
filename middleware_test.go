package logging

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/trace"
)

func serve(t *testing.T, mw func(http.Handler) http.Handler, h http.HandlerFunc, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	mw(h).ServeHTTP(w, req)
	return w
}

func TestMiddlewareCompletionRecord(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(&buf)
	mw := NewMiddleware(WithMiddlewareLogger(l))

	var ctxReqID string
	w := serve(t, mw, func(w http.ResponseWriter, r *http.Request) {
		ctxReqID = RequestIDFromContext(r.Context())
		L(r.Context()).Info("inside handler")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("hello"))
	}, httptest.NewRequest(http.MethodPost, "/items?token=secret", nil))

	reqID := w.Header().Get("X-Request-ID")
	if len(reqID) != 16 || reqID != ctxReqID {
		t.Fatalf("bad request id: header=%q ctx=%q", reqID, ctxReqID)
	}

	lines := decodeLines(t, &buf)
	if len(lines) != 2 {
		t.Fatalf("want 2 records, got %d", len(lines))
	}
	if lines[0]["request_id"] != reqID {
		t.Fatal("handler logs must carry request_id")
	}
	done := lines[1]
	if done["msg"] != "request completed" || done["level"] != "INFO" ||
		done["status"] != float64(201) || done["bytes"] != float64(5) {
		t.Fatalf("unexpected completion record: %v", done)
	}
	if done["endpoint"] != "/items" {
		t.Fatalf("query string must not be logged by default: %v", done["endpoint"])
	}
	if raw := buf.String(); strings.Count(strings.Split(raw, "\n")[1], `"request_id"`) != 1 {
		t.Fatalf("request_id must appear once per record: %s", raw)
	}
}

func TestMiddlewareLevels(t *testing.T) {
	for code, want := range map[int]string{200: "INFO", 404: "WARN", 503: "ERROR"} {
		var buf bytes.Buffer
		mw := NewMiddleware(WithMiddlewareLogger(newTestLogger(&buf)))
		serve(t, mw, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) },
			httptest.NewRequest(http.MethodGet, "/", nil))
		if got := decodeLines(t, &buf)[0]["level"]; got != want {
			t.Fatalf("status %d: want %s, got %v", code, want, got)
		}
	}
}

func TestMiddlewareRequestIDValidation(t *testing.T) {
	var buf bytes.Buffer
	mw := NewMiddleware(WithMiddlewareLogger(newTestLogger(&buf)), WithMaxRequestIDLength(32))
	ok := func(w http.ResponseWriter, _ *http.Request) {}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-ID", "abc-123")
	if got := serve(t, mw, ok, req).Header().Get("X-Request-ID"); got != "abc-123" {
		t.Fatalf("valid incoming id must be reused, got %q", got)
	}

	for _, bad := range []string{strings.Repeat("a", 33), "has space", `quote"`, "tab\t"} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-Request-ID", bad)
		if got := serve(t, mw, ok, req).Header().Get("X-Request-ID"); got == bad {
			t.Fatalf("invalid id %q must be replaced", bad)
		}
	}

	untrusted := NewMiddleware(WithMiddlewareLogger(newTestLogger(&buf)), WithTrustRequestID(false))
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-ID", "abc-123")
	if got := serve(t, untrusted, ok, req).Header().Get("X-Request-ID"); got == "abc-123" {
		t.Fatal("incoming id must be ignored when not trusted")
	}
}

func TestMiddlewareSamplingAndSlow(t *testing.T) {
	var buf bytes.Buffer
	mw := NewMiddleware(
		WithMiddlewareLogger(newTestLogger(&buf)),
		WithSampling(10),
		WithSlowThreshold(20*time.Millisecond),
	)
	for i := 0; i < 20; i++ {
		serve(t, mw, func(http.ResponseWriter, *http.Request) {}, httptest.NewRequest(http.MethodGet, "/", nil))
	}
	serve(t, mw, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) },
		httptest.NewRequest(http.MethodGet, "/", nil))
	serve(t, mw, func(http.ResponseWriter, *http.Request) { time.Sleep(25 * time.Millisecond) },
		httptest.NewRequest(http.MethodGet, "/", nil))

	lines := decodeLines(t, &buf)
	if len(lines) != 4 { // 2 sampled successes + error + slow
		t.Fatalf("want 4 records, got %d", len(lines))
	}
	if lines[3]["slow"] != true || lines[3]["level"] != "WARN" {
		t.Fatalf("slow request must be logged at WARN: %v", lines[3])
	}
}

func TestMiddlewareSkipPaths(t *testing.T) {
	var buf bytes.Buffer
	mw := NewMiddleware(WithMiddlewareLogger(newTestLogger(&buf)), WithSkipPaths("/healthz"))
	var hasLogger bool
	serve(t, mw, func(_ http.ResponseWriter, r *http.Request) {
		hasLogger = RequestIDFromContext(r.Context()) != ""
	}, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if buf.Len() != 0 || !hasLogger {
		t.Fatalf("skipped path must not be logged but must be enriched; out=%q", buf.String())
	}
}

func TestMiddlewareTraceIDs(t *testing.T) {
	var buf bytes.Buffer
	mw := NewMiddleware(WithMiddlewareLogger(newTestLogger(&buf)))
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		SpanID:     trace.SpanID{1, 2, 3, 4, 5, 6, 7, 8},
		TraceFlags: trace.FlagsSampled,
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(trace.ContextWithSpanContext(req.Context(), sc))
	serve(t, mw, func(http.ResponseWriter, *http.Request) {}, req)

	rec := decodeLines(t, &buf)[0]
	if rec["trace_id"] != sc.TraceID().String() || rec["span_id"] != sc.SpanID().String() {
		t.Fatalf("trace ids missing: %v", rec)
	}
}

func TestMiddlewarePanic(t *testing.T) {
	t.Run("re-panics by default", func(t *testing.T) {
		var buf bytes.Buffer
		mw := NewMiddleware(WithMiddlewareLogger(newTestLogger(&buf)))
		defer func() {
			if recover() == nil {
				t.Fatal("panic must be re-raised")
			}
			rec := decodeLines(t, &buf)[0]
			if rec["level"] != "ERROR" || rec["panic"] != "boom" || rec["status"] != float64(500) {
				t.Fatalf("panic not logged properly: %v", rec)
			}
		}()
		serve(t, mw, func(http.ResponseWriter, *http.Request) { panic("boom") },
			httptest.NewRequest(http.MethodGet, "/", nil))
	})

	t.Run("recovers when enabled", func(t *testing.T) {
		var buf bytes.Buffer
		mw := NewMiddleware(WithMiddlewareLogger(newTestLogger(&buf)), WithRecoverPanics(true))
		w := serve(t, mw, func(http.ResponseWriter, *http.Request) { panic("boom") },
			httptest.NewRequest(http.MethodGet, "/", nil))
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("want 500, got %d", w.Code)
		}
	})
}

func TestResponseRecorderKeepsInterfaces(t *testing.T) {
	var buf bytes.Buffer
	mw := NewMiddleware(WithMiddlewareLogger(newTestLogger(&buf)))

	w := serve(t, mw, func(w http.ResponseWriter, _ *http.Request) {
		if _, ok := w.(http.Flusher); !ok {
			t.Error("http.Flusher must be preserved")
		}
		_, _ = w.Write([]byte("chunk"))
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("ResponseController.Flush failed: %v", err)
		}
		if rf, ok := w.(io.ReaderFrom); ok {
			_, _ = rf.ReadFrom(strings.NewReader("-more"))
		}
		w.WriteHeader(http.StatusTeapot) // superfluous, must be ignored
	}, httptest.NewRequest(http.MethodGet, "/", nil))

	if !w.Flushed {
		t.Fatal("flush did not reach the underlying writer")
	}
	rec := decodeLines(t, &buf)[0]
	if rec["status"] != float64(200) || rec["bytes"] != float64(len("chunk-more")) {
		t.Fatalf("status/bytes wrong: %v", rec)
	}
}

func TestLegacyMiddlewareUsesContextLogger(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(&buf)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(ContextWithLogger(context.Background(), l))
	w := httptest.NewRecorder()
	Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(w, req)
	if len(decodeLines(t, &buf)) != 1 {
		t.Fatal("legacy Middleware must log via the context logger")
	}
}

func BenchmarkMiddleware(b *testing.B) {
	l := NewLogger(WithWriter(io.Discard), WithSetDefault(false))
	h := NewMiddleware(WithMiddlewareLogger(l))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	req := httptest.NewRequest(http.MethodGet, "/bench", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
}
