package logginggrpc

import (
	"context"
	"testing"

	logging "github.com/jwm1rr0rb10/go-logging/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func TestUnaryClientInterceptorPropagatesRequestID(t *testing.T) {
	var got []string
	invoker := func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
		md, _ := metadata.FromOutgoingContext(ctx)
		got = md.Get("x-request-id")
		return nil
	}
	ic := UnaryClientInterceptor()
	ctx := logging.ContextWithRequestID(context.Background(), "abc")
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
	ctx := logging.ContextWithRequestID(context.Background(), "s-1")
	_, _ = StreamClientInterceptor()(ctx, &grpc.StreamDesc{}, nil, "/svc/S", streamer)
	if len(got) != 1 || got[0] != "s-1" {
		t.Fatalf("got %v", got)
	}
}
