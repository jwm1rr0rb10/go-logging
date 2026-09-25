package logging

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"testing"
	"time"
)

// nopResponseWriter keeps httptest.ResponseRecorder allocations out of the numbers.
type nopResponseWriter struct{ h http.Header }

func (w *nopResponseWriter) Header() http.Header         { return w.h }
func (w *nopResponseWriter) Write(b []byte) (int, error) { return len(b), nil }
func (w *nopResponseWriter) WriteHeader(int)             {}

func benchMiddleware(b *testing.B, parallel bool, opts ...MiddlewareOption) {
	l := NewLogger(WithWriter(io.Discard), WithSetDefault(false))
	opts = append([]MiddlewareOption{WithMiddlewareLogger(l)}, opts...)
	h := NewMiddleware(opts...)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	req := httptest.NewRequest(http.MethodGet, "/bench", nil)
	b.ReportAllocs()
	b.ResetTimer()
	if !parallel {
		w := &nopResponseWriter{h: http.Header{}}
		for i := 0; i < b.N; i++ {
			h.ServeHTTP(w, req)
		}
		return
	}
	b.RunParallel(func(pb *testing.PB) {
		w := &nopResponseWriter{h: http.Header{}}
		for pb.Next() {
			h.ServeHTTP(w, req)
		}
	})
}

func BenchmarkMiddlewareLogged(b *testing.B)         { benchMiddleware(b, false) }
func BenchmarkMiddlewareSampledOut(b *testing.B)     { benchMiddleware(b, false, WithSampling(1_000_000)) }
func BenchmarkMiddlewareLoggedParallel(b *testing.B) { benchMiddleware(b, true) }
func BenchmarkMiddlewareSampledOutParallel(b *testing.B) {
	benchMiddleware(b, true, WithSampling(1_000_000))
}

func BenchmarkContextLoggerInfo(b *testing.B) {
	l := NewLogger(WithWriter(io.Discard), WithSetDefault(false))
	ctx := ContextWithAttrs(ContextWithLogger(context.Background(), l), StringAttr("request_id", "abc"))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		L(ctx).LogAttrs(ctx, LevelInfo, "hello", IntAttr("i", i))
	}
}

func devNull(b *testing.B) *os.File {
	f, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = f.Close() })
	return f
}

func benchWriter(b *testing.B, w io.Writer) {
	l := NewLogger(WithWriter(w), WithSetDefault(false))
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			l.LogAttrs(context.Background(), LevelInfo, "request completed",
				StringAttr("request_id", "9f86d081884c7d65"), IntAttr("status", 200),
				DurationAttr("duration", time.Millisecond))
		}
	})
}

func BenchmarkWriterUnbufferedParallel(b *testing.B) { benchWriter(b, devNull(b)) }
func BenchmarkWriterBufferedParallel(b *testing.B) {
	w := NewBufferedWriter(devNull(b), 256<<10, time.Second)
	defer w.Close()
	benchWriter(b, w)
}

func BenchmarkWriterAsyncParallel(b *testing.B) {
	w := NewAsyncWriter(devNull(b), 0, 0)
	defer w.Close()
	benchWriter(b, w)
}

// stallWriter simulates a destination that periodically stalls (a full
// pipe, a slow container log driver): every Write blocks for 20ms.
type stallWriter struct{}

func (stallWriter) Write(p []byte) (int, error) {
	time.Sleep(20 * time.Millisecond)
	return len(p), nil
}

// benchStall measures how long the calling goroutine is blocked by a log
// call when the destination stalls. Producers are throttled to ~100k
// records/s (a busy service, not a tight loop).
func benchStall(b *testing.B, w io.Writer, stats func() AsyncWriterStats) {
	l := NewLogger(WithWriter(w), WithSetDefault(false))
	ctx := context.Background()
	lat := make([]time.Duration, 0, b.N)
	b.ResetTimer()
	next := time.Now()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		l.LogAttrs(ctx, LevelInfo, "request completed",
			StringAttr("request_id", "9f86d081884c7d65"), IntAttr("status", 200),
			DurationAttr("duration", time.Millisecond))
		lat = append(lat, time.Since(start))
		next = next.Add(10 * time.Microsecond)
		for time.Now().Before(next) {
		}
	}
	b.StopTimer()
	slices.Sort(lat)
	b.ReportMetric(float64(lat[len(lat)*99/100].Microseconds()), "p99-µs")
	b.ReportMetric(float64(lat[len(lat)-1].Microseconds()), "max-µs")
	if stats != nil {
		b.ReportMetric(float64(stats().Dropped)/float64(b.N)*100, "%dropped")
	}
}

func BenchmarkStalledDestBuffered(b *testing.B) {
	w := NewBufferedWriter(stallWriter{}, 256<<10, time.Second)
	defer w.Close()
	benchStall(b, w, nil)
}

func BenchmarkStalledDestAsync(b *testing.B) {
	w := NewAsyncWriter(stallWriter{}, 4<<20, 0)
	defer w.Close()
	benchStall(b, w, w.Stats)
}

func BenchmarkRateLimitedStorm(b *testing.B) {
	l := NewLogger(WithWriter(io.Discard), WithSetDefault(false),
		WithRateLimit(RateLimitConfig{First: 100, Thereafter: 1000}))
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		l.LogAttrs(ctx, LevelError, "request completed", IntAttr("status", 500))
	}
}

func BenchmarkNoRateLimitStorm(b *testing.B) {
	l := NewLogger(WithWriter(io.Discard), WithSetDefault(false))
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		l.LogAttrs(ctx, LevelError, "request completed", IntAttr("status", 500))
	}
}
