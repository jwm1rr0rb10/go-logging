package logging

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

// countingHandler counts WithAttrs calls (handler clones).
type countingHandler struct {
	slog.Handler
	withAttrs *atomic.Int32
}

func (h countingHandler) WithAttrs(a []slog.Attr) slog.Handler {
	h.withAttrs.Add(1)
	return countingHandler{Handler: h.Handler.WithAttrs(a), withAttrs: h.withAttrs}
}

func TestMiddlewareDoesNotCloneHandlerWhenNotLogging(t *testing.T) {
	var buf bytes.Buffer
	var clones atomic.Int32
	base := slog.New(countingHandler{Handler: slog.NewJSONHandler(&buf, nil), withAttrs: &clones})

	mw := NewMiddleware(WithMiddlewareLogger(base), WithSampling(1000))
	for i := 0; i < 10; i++ {
		serve(t, mw, func(http.ResponseWriter, *http.Request) {}, httptest.NewRequest(http.MethodGet, "/", nil))
	}
	if clones.Load() != 0 {
		t.Fatalf("handler cloned %d times for requests that did not log", clones.Load())
	}

	// Logging from the handler materializes the request logger exactly once.
	serve(t, mw, func(_ http.ResponseWriter, r *http.Request) {
		L(r.Context()).Info("one")
		L(r.Context()).Info("two")
	}, httptest.NewRequest(http.MethodGet, "/", nil))
	if clones.Load() != 1 {
		t.Fatalf("want 1 clone, got %d", clones.Load())
	}
	for _, rec := range decodeLines(t, &buf) {
		if rec["request_id"] == nil || rec["method"] != "GET" {
			t.Fatalf("request attrs missing: %v", rec)
		}
	}
}

func TestLazyScopeConcurrentMaterialization(t *testing.T) {
	var out syncBuffer
	l := NewLogger(WithWriter(&out), WithSetDefault(false))
	ctx := ContextWithAttrs(ContextWithLogger(context.Background(), l), StringAttr("k", "v"))

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			L(ctx).Info("x")
		}()
	}
	wg.Wait()
	lines := decodeLines(t, &out.buf)
	if len(lines) != 16 {
		t.Fatalf("want 16 lines, got %d", len(lines))
	}
	for _, rec := range lines {
		if rec["k"] != "v" {
			t.Fatalf("attr missing: %v", rec)
		}
	}
}

func TestContextChainKeepsRequestIDAndAttrs(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(&buf)

	ctx := ContextWithLogger(context.Background(), l)
	ctx = ContextWithRequestID(ctx, "rid")
	ctx = ContextWithAttrs(ctx, StringAttr("a", "1"))
	ctx = ContextWithAttrs(ctx, StringAttr("b", "2"), StringAttr("c", "3"),
		StringAttr("d", "4"), StringAttr("e", "5"), StringAttr("f", "6"), StringAttr("g", "7"))

	if RequestIDFromContext(ctx) != "rid" {
		t.Fatal("request id lost")
	}
	L(ctx).Info("hi")
	rec := decodeLines(t, &buf)[0]
	for _, k := range []string{"a", "b", "g"} {
		if rec[k] == nil {
			t.Fatalf("attr %s missing: %v", k, rec)
		}
	}

	// Replacing the logger keeps the request id.
	ctx = ContextWithLogger(ctx, l)
	if RequestIDFromContext(ctx) != "rid" {
		t.Fatal("ContextWithLogger must preserve the request id")
	}
}
