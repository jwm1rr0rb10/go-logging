package logging

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTransportPropagatesRequestID(t *testing.T) {
	var got string
	tr := NewTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		got = r.Header.Get("X-Request-ID")
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	}))

	ctx := ContextWithRequestID(context.Background(), "abc-123")
	req := httptest.NewRequest(http.MethodGet, "http://example.com", nil).WithContext(ctx)
	if _, err := tr.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if got != "abc-123" {
		t.Fatalf("request id not propagated: %q", got)
	}
	if req.Header.Get("X-Request-ID") != "" {
		t.Fatal("caller's request must not be modified")
	}

	req.Header.Set("X-Request-ID", "explicit")
	_, _ = tr.RoundTrip(req)
	if got != "explicit" {
		t.Fatalf("explicit header must win, got %q", got)
	}
}

func TestMiddlewareToTransportEndToEnd(t *testing.T) {
	var downstream string
	tr := NewTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		downstream = r.Header.Get("X-Request-ID")
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	}))
	h := NewMiddleware(WithLogCompletion(false))(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		out, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, "http://svc", nil)
		_, _ = tr.RoundTrip(out)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-ID", "req-42")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if downstream != "req-42" {
		t.Fatalf("got %q", downstream)
	}
}
