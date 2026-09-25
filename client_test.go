package logging

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTransportPropagatesRequestID(t *testing.T) {
	var got string
	tr := NewTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		got = r.Header.Get("X-Request-ID")
		return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
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
		return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
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

func TestUnaryClientInterceptorPropagatesRequestID(t *testing.T) {
	var got []string
	invoker := func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
		md, _ := metadata.FromOutgoingContext(ctx)
		got = md.Get("x-request-id")
		return nil
	}
	ic := UnaryClientInterceptor()
	ctx := ContextWithRequestID(context.Background(), "abc")
	_ = ic(ctx, "/svc/M", nil, nil, nil, invoker)
	if len(got) != 1 || got[0] != "abc" {
		t.Fatalf("got %v", got)
	}

	ctx = metadata.AppendToOutgoingContext(ctx, "x-request-id", "explicit")
	_ = ic(ctx, "/svc/M", nil, nil, nil, invoker)
	if len(got) != 1 || got[0] != "explicit" {
		t.Fatalf("explicit metadata must win, got %v", got)
	}

	_ = ic(context.Background(), "/svc/M", nil, nil, nil, invoker)
	if len(got) != 0 {
		t.Fatalf("no id in ctx must not add metadata, got %v", got)
	}
}

func TestStreamClientInterceptorPropagatesRequestID(t *testing.T) {
	var got []string
	streamer := func(ctx context.Context, _ *grpc.StreamDesc, _ *grpc.ClientConn, _ string, _ ...grpc.CallOption) (grpc.ClientStream, error) {
		md, _ := metadata.FromOutgoingContext(ctx)
		got = md.Get("x-request-id")
		return nil, nil
	}
	ctx := ContextWithRequestID(context.Background(), "s-1")
	_, _ = StreamClientInterceptor()(ctx, &grpc.StreamDesc{}, nil, "/svc/S", streamer)
	if len(got) != 1 || got[0] != "s-1" {
		t.Fatalf("got %v", got)
	}
}
