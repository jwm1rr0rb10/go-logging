package logging

import "net/http"

// NewTransport returns an http.RoundTripper that propagates the request ID
// from the request context (see RequestIDFromContext) to outgoing HTTP
// requests, so logs of downstream services can be correlated. A header that
// is already set is left untouched. base == nil uses http.DefaultTransport.
//
// Accepts WithRequestIDHeader; other options are ignored. For gRPC clients
// see the interceptors in github.com/jwm1rr0rb10/go-logging/grpc/v2.
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
