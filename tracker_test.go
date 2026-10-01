package logging

import (
	"bytes"
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

func TestMiddlewareLogsRoutePattern(t *testing.T) {
	var buf bytes.Buffer
	mux := http.NewServeMux()
	mux.HandleFunc("GET /users/{id}", func(http.ResponseWriter, *http.Request) {})
	h := NewMiddleware(WithMiddlewareLogger(newTestLogger(&buf)))(mux)

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/users/42", nil))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/nope", nil))

	lines := decodeLines(t, &buf)
	if lines[0]["route"] != "GET /users/{id}" || lines[0]["endpoint"] != "/users/42" {
		t.Fatalf("route pattern missing: %v", lines[0])
	}
	if _, ok := lines[1]["route"]; ok || lines[1]["status"] != float64(404) {
		t.Fatalf("unmatched requests must have no route: %v", lines[1])
	}
}

func traceCtx(low uint64) context.Context {
	var id trace.TraceID
	id[0] = 1
	binary.BigEndian.PutUint64(id[8:], low)
	sc := trace.NewSpanContext(trace.SpanContextConfig{TraceID: id, SpanID: trace.SpanID{1}})
	return trace.ContextWithSpanContext(context.Background(), sc)
}

func TestTraceSamplingIsConsistentAcrossServices(t *testing.T) {
	var a, b bytes.Buffer
	svcA := NewMiddleware(WithMiddlewareLogger(newTestLogger(&a)), WithSampling(4))
	svcB := NewMiddleware(WithMiddlewareLogger(newTestLogger(&b)), WithSampling(4))
	ok := func(http.ResponseWriter, *http.Request) {}

	for _, low := range []uint64{1, 4, 7, 8, 9, 12} { // 4, 8 and 12 are sampled
		req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(traceCtx(low))
		serve(t, svcA, ok, req)
		serve(t, svcB, ok, req)
	}
	la, lb := decodeLines(t, &a), decodeLines(t, &b)
	if len(la) != 3 || len(lb) != 3 {
		t.Fatalf("want 3 sampled requests per service, got %d and %d", len(la), len(lb))
	}
	for i := range la {
		if la[i]["trace_id"] != lb[i]["trace_id"] {
			t.Fatalf("services sampled different traces: %v vs %v", la[i], lb[i])
		}
	}
}

func TestTraceSamplingDisabledUsesCounter(t *testing.T) {
	var buf bytes.Buffer
	mw := NewMiddleware(WithMiddlewareLogger(newTestLogger(&buf)), WithSampling(4), WithTraceSampling(false))
	for i := 0; i < 8; i++ {
		req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(traceCtx(1)) // never sampled by trace
		serve(t, mw, func(http.ResponseWriter, *http.Request) {}, req)
	}
	if n := len(decodeLines(t, &buf)); n != 2 {
		t.Fatalf("counter must log every 4th request, got %d", n)
	}
}

func TestRequestTrackerForCustomTransports(t *testing.T) {
	var buf bytes.Buffer
	tr := NewRequestTracker(WithMiddlewareLogger(newTestLogger(&buf)), WithRequestIDHeader("x-correlation-id"))
	if tr.RequestIDHeader() != "X-Correlation-Id" || tr.RecoverPanics() {
		t.Fatalf("unexpected config: %q %v", tr.RequestIDHeader(), tr.RecoverPanics())
	}

	ctx, req := tr.Start(context.Background(), "queue.orders", "msg-1", StringAttr("queue", "orders"))
	if req.ID() != "msg-1" || RequestIDFromContext(ctx) != "msg-1" {
		t.Fatalf("request id: %q / %q", req.ID(), RequestIDFromContext(ctx))
	}
	L(ctx).Info("processing")
	req.Finish(LevelWarn, "message handled", nil, StringAttr("result", "retry"))

	lines := decodeLines(t, &buf)
	if len(lines) != 2 {
		t.Fatalf("want 2 records, got %v", lines)
	}
	done := lines[1]
	if done["msg"] != "message handled" || done["level"] != "WARN" || done["queue"] != "orders" ||
		done["request_id"] != "msg-1" || done["result"] != "retry" || done["duration"] == nil {
		t.Fatalf("unexpected completion record: %v", done)
	}
}

func TestRequestTrackerSkipLogsOnlyPanics(t *testing.T) {
	var buf bytes.Buffer
	tr := NewRequestTracker(WithMiddlewareLogger(newTestLogger(&buf)), WithSkipPaths("health"))
	_, req := tr.Start(context.Background(), "health", "")
	req.Finish(LevelInfo, "done", nil)
	if buf.Len() != 0 {
		t.Fatalf("skipped request logged: %s", buf.String())
	}
	_, req = tr.Start(context.Background(), "health", "")
	req.Finish(LevelInfo, "done", "boom")
	if rec := decodeLines(t, &buf)[0]; rec["level"] != "ERROR" || rec["panic"] != "boom" {
		t.Fatalf("panic of a skipped request must be logged: %v", rec)
	}
}
