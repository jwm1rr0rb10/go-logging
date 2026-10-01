package logginggrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	logging "github.com/jwm1rr0rb10/go-logging/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func newTestLogger(buf *bytes.Buffer) *logging.Logger {
	return logging.NewLogger(logging.WithWriter(buf))
}

func decodeLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		m := map[string]any{}
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("invalid JSON line %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func TestUnaryServerInterceptor(t *testing.T) {
	var buf bytes.Buffer
	ic := UnaryServerInterceptor(logging.WithMiddlewareLogger(newTestLogger(&buf)))
	info := &grpc.UnaryServerInfo{FullMethod: "/svc.Users/Get"}

	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-request-id", "req-1"))
	var seen string
	_, err := ic(ctx, nil, info, func(ctx context.Context, _ any) (any, error) {
		seen = logging.RequestIDFromContext(ctx)
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
	if _, ok := rec["duration"]; !ok {
		t.Fatalf("duration missing: %v", rec)
	}
}

func TestUnaryServerInterceptorCustomMetadataKey(t *testing.T) {
	var buf bytes.Buffer
	ic := UnaryServerInterceptor(logging.WithMiddlewareLogger(newTestLogger(&buf)),
		logging.WithRequestIDHeader("X-Correlation-ID"))
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-correlation-id", "c-1"))
	var seen string
	_, _ = ic(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/svc/M"}, func(ctx context.Context, _ any) (any, error) {
		seen = logging.RequestIDFromContext(ctx)
		return nil, nil
	})
	if seen != "c-1" {
		t.Fatalf("custom metadata key not used, got %q", seen)
	}
}

func TestUnaryServerInterceptorRecover(t *testing.T) {
	var buf bytes.Buffer
	ic := UnaryServerInterceptor(logging.WithMiddlewareLogger(newTestLogger(&buf)), logging.WithRecoverPanics(true))
	_, err := ic(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/svc/M"},
		func(context.Context, any) (any, error) { panic("boom") })
	if status.Code(err) != codes.Internal {
		t.Fatalf("want Internal, got %v", err)
	}
	if rec := decodeLines(t, &buf)[0]; rec["level"] != "ERROR" || rec["panic"] != "boom" {
		t.Fatalf("panic not logged: %v", rec)
	}
}

func TestUnaryServerInterceptorRePanicsByDefault(t *testing.T) {
	var buf bytes.Buffer
	ic := UnaryServerInterceptor(logging.WithMiddlewareLogger(newTestLogger(&buf)))
	defer func() {
		if recover() == nil {
			t.Fatal("panic must be re-raised")
		}
		if rec := decodeLines(t, &buf)[0]; rec["panic"] != "boom" {
			t.Fatalf("panic not logged: %v", rec)
		}
	}()
	_, _ = ic(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/svc/M"},
		func(context.Context, any) (any, error) { panic("boom") })
}

func TestWithoutCompletionOnlyEnrichesContext(t *testing.T) {
	var buf bytes.Buffer
	ic := UnaryServerInterceptor(logging.WithMiddlewareLogger(newTestLogger(&buf)), logging.WithLogCompletion(false))
	_, _ = ic(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/svc/M"},
		func(ctx context.Context, _ any) (any, error) {
			logging.L(ctx).Info("inside")
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
	ic := StreamServerInterceptor(logging.WithMiddlewareLogger(newTestLogger(&buf)))
	var seen string
	err := ic(nil, &fakeStream{ctx: context.Background()}, &grpc.StreamServerInfo{FullMethod: "/svc/Watch"},
		func(_ any, ss grpc.ServerStream) error {
			seen = logging.RequestIDFromContext(ss.Context())
			return nil
		})
	if err != nil || seen == "" {
		t.Fatalf("err=%v, request id not propagated to stream context", err)
	}
	if rec := decodeLines(t, &buf)[0]; rec["grpc_code"] != "OK" || rec["level"] != "INFO" {
		t.Fatalf("unexpected record: %v", rec)
	}
}

func TestCodeLevel(t *testing.T) {
	for code, want := range map[codes.Code]logging.Level{
		codes.OK:               logging.LevelInfo,
		codes.NotFound:         logging.LevelWarn,
		codes.Canceled:         logging.LevelWarn,
		codes.Internal:         logging.LevelError,
		codes.DeadlineExceeded: logging.LevelError,
	} {
		if got := codeLevel(code); got != want {
			t.Errorf("%v: got %v, want %v", code, got, want)
		}
	}
}
