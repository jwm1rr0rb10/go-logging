package logging

import (
	"bytes"
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestUnaryServerInterceptor(t *testing.T) {
	var buf bytes.Buffer
	ic := UnaryServerInterceptor(WithMiddlewareLogger(newTestLogger(&buf)))
	info := &grpc.UnaryServerInfo{FullMethod: "/svc.Users/Get"}

	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-request-id", "req-1"))
	var seen string
	_, err := ic(ctx, nil, info, func(ctx context.Context, _ any) (any, error) {
		seen = RequestIDFromContext(ctx)
		return nil, status.Error(codes.NotFound, "no user")
	})
	if status.Code(err) != codes.NotFound || seen != "req-1" {
		t.Fatalf("err=%v seen=%q", err, seen)
	}
	rec := decodeLines(t, &buf)[0]
	if rec["msg"] != "rpc completed" || rec["grpc_code"] != "NotFound" ||
		rec["level"] != "WARN" || rec["request_id"] != "req-1" || rec["grpc_method"] != "/svc.Users/Get" {
		t.Fatalf("unexpected record: %v", rec)
	}
}

func TestUnaryServerInterceptorRecover(t *testing.T) {
	var buf bytes.Buffer
	ic := UnaryServerInterceptor(WithMiddlewareLogger(newTestLogger(&buf)), WithRecoverPanics(true))
	_, err := ic(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/svc/M"},
		func(context.Context, any) (any, error) { panic("boom") })
	if status.Code(err) != codes.Internal {
		t.Fatalf("want Internal, got %v", err)
	}
	if rec := decodeLines(t, &buf)[0]; rec["level"] != "ERROR" || rec["panic"] != "boom" {
		t.Fatalf("panic not logged: %v", rec)
	}
}

func TestLegacyWithTraceIDInLoggerDoesNotLogCompletion(t *testing.T) {
	var buf bytes.Buffer
	prev := Default()
	SetDefault(newTestLogger(&buf))
	defer SetDefault(prev)

	_, _ = WithTraceIDInLogger()(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/svc/M"},
		func(ctx context.Context, _ any) (any, error) {
			L(ctx).Info("inside")
			return nil, nil
		})
	lines := decodeLines(t, &buf)
	if len(lines) != 1 || lines[0]["grpc_method"] != "/svc/M" {
		t.Fatalf("unexpected output: %v", lines)
	}
}

type fakeStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (f *fakeStream) Context() context.Context { return f.ctx }

func TestStreamServerInterceptor(t *testing.T) {
	var buf bytes.Buffer
	ic := StreamServerInterceptor(WithMiddlewareLogger(newTestLogger(&buf)))
	var seen string
	err := ic(nil, &fakeStream{ctx: context.Background()}, &grpc.StreamServerInfo{FullMethod: "/svc/Watch"},
		func(_ any, ss grpc.ServerStream) error {
			seen = RequestIDFromContext(ss.Context())
			return nil
		})
	if err != nil || seen == "" {
		t.Fatalf("err=%v, request id not propagated to stream context", err)
	}
	if rec := decodeLines(t, &buf)[0]; rec["grpc_code"] != "OK" || rec["level"] != "INFO" {
		t.Fatalf("unexpected record: %v", rec)
	}
}
