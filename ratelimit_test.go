package logging

import (
	"bytes"
	"sync"
	"testing"
	"time"
)

func TestRateLimitFirstThereafter(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(&buf, WithRateLimit(RateLimitConfig{Tick: time.Hour, First: 3, Thereafter: 5}))

	for i := 0; i < 23; i++ {
		l.Error("request completed")
	}
	l.Error("other message")    // separate budget
	l.Warn("request completed") // separate level

	// 3 first + records #8, #13, #18, #23 + 2 others
	if n := len(decodeLines(t, &buf)); n != 3+4+2 {
		t.Fatalf("want 9 records, got %d", n)
	}
	dropped, ok := RateLimitStats(l)
	if !ok || dropped != 23-7 {
		t.Fatalf("want 16 dropped, got %d (ok=%v)", dropped, ok)
	}
}

func TestRateLimitResetsEveryTick(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(&buf, WithRateLimit(RateLimitConfig{Tick: 20 * time.Millisecond, First: 1}))
	l.Info("x")
	l.Info("x") // dropped, Thereafter == 0
	time.Sleep(30 * time.Millisecond)
	l.Info("x")
	if n := len(decodeLines(t, &buf)); n != 2 {
		t.Fatalf("want 2 records, got %d", n)
	}
}

func TestRateLimitSharedAcrossWith(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(&buf, WithRateLimit(RateLimitConfig{Tick: time.Hour, First: 2}))
	l.With("a", 1).Info("m")
	l.With("b", 2).Info("m")
	l.WithGroup("g").Info("m")
	if n := len(decodeLines(t, &buf)); n != 2 {
		t.Fatalf("derived loggers must share the budget, got %d records", n)
	}
	if _, ok := RateLimitStats(newTestLogger(&buf)); ok {
		t.Fatal("logger without rate limit must report ok=false")
	}
}

func TestRateLimitConcurrent(t *testing.T) {
	var out syncBuffer
	l := NewLogger(WithWriter(&out), WithSetDefault(false),
		WithRateLimit(RateLimitConfig{Tick: time.Hour, First: 100}))
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				l.Error("storm")
			}
		}()
	}
	wg.Wait()
	if n := len(decodeLines(t, &out.buf)); n != 100 {
		t.Fatalf("want exactly 100 records, got %d", n)
	}
}
