package logging

import (
	"context"
	"net/http"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// NewTransport returns an http.RoundTripper that propagates the request ID
// from the request context (see RequestIDFromContext) to outgoing HTTP
// requests, so logs of downstream services can be correlated. A header that
// is already set is left untouched. base == nil uses http.DefaultTransport.
//
// Accepts WithRequestIDHeader; other options are ignored.
//
//	client := &http.Client{Transport: logging.NewTransport(nil)}
//	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
func NewTransport(base http.RoundTripper, opts ...MiddlewareOption) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	cfg := newMiddlewareConfig(opts)
	return &requestIDTransport{base: base, header: cfg.requestIDHeader}
}

type requestIDTransport struct {
	base   http.RoundTripper
	header string
}

func (t *requestIDTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	id := RequestIDFromContext(r.Context())
	if id == "" || len(r.Header[t.header]) > 0 {
		return t.base.RoundTrip(r)
	}
	// RoundTrippers must not modify the caller's request: shallow-copy it
	// and clone only the headers.
	r2 := new(http.Request)
	*r2 = *r
	r2.Header = r.Header.Clone()
	if r2.Header == nil {
		r2.Header = make(http.Header, 1)
	}
	r2.Header[t.header] = []string{id}
	return t.base.RoundTrip(r2)
}

// UnaryClientInterceptor returns a gRPC client interceptor that propagates
// the request ID from the context to outgoing metadata.
// Accepts WithRequestIDHeader; other options are ignored.
func UnaryClientInterceptor(opts ...MiddlewareOption) grpc.UnaryClientInterceptor {
	key := newMiddlewareConfig(opts).requestIDMDKey
	return func(ctx context.Context, method string, req, reply any,
		cc *grpc.ClientConn, invoker grpc.UnaryInvoker, callOpts ...grpc.CallOption,
	) error {
		return invoker(outgoingWithRequestID(ctx, key), method, req, reply, cc, callOpts...)
	}
}

// StreamClientInterceptor is the streaming counterpart of
// UnaryClientInterceptor.
func StreamClientInterceptor(opts ...MiddlewareOption) grpc.StreamClientInterceptor {
	key := newMiddlewareConfig(opts).requestIDMDKey
	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn,
		method string, streamer grpc.Streamer, callOpts ...grpc.CallOption,
	) (grpc.ClientStream, error) {
		return streamer(outgoingWithRequestID(ctx, key), desc, cc, method, callOpts...)
	}
}

func outgoingWithRequestID(ctx context.Context, key string) context.Context {
	id := RequestIDFromContext(ctx)
	if id == "" {
		return ctx
	}
	if md, ok := metadata.FromOutgoingContext(ctx); ok && len(md.Get(key)) > 0 {
		return ctx // set explicitly by the caller
	}
	return metadata.AppendToOutgoingContext(ctx, key, id)
}
