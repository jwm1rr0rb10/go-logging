package logging

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"
)

const (
	rateLimitBuckets = 1024
	rateLimitLevels  = 4 // debug, info, warn, error+
)

// RateLimitConfig configures NewRateLimitHandler.
//
// Within every Tick, for each (level, message) key the first First records
// are written, then only every Thereafter-th. With Thereafter == 0 all
// records after the first First are dropped until the next tick.
type RateLimitConfig struct {
	Tick       time.Duration // default 1s
	First      uint64        // default 100
	Thereafter uint64        // 0 drops everything after First
}

// RateLimitHandler is a slog.Handler that limits the number of records per
// (level, message) key and time interval, protecting the service and the
// log pipeline from log storms (e.g. every request failing during an
// incident, where the HTTP middleware logs every 5xx).
//
// Records are keyed by level and message only, not by attributes, so
// "request completed" at Error level from all requests shares one budget.
// Different messages are counted independently (hash collisions between
// messages are possible and harmless: they share a budget).
//
// The counters are lock-free and allocation-free. Loggers derived with
// With/WithGroup share the same counters.
type RateLimitHandler struct {
	next slog.Handler
	s    *rateLimiter
}

type rateLimiter struct {
	tick       int64
	first      uint64
	thereafter uint64
	dropped    atomic.Uint64
	counters   [rateLimitLevels][rateLimitBuckets]rateCounter
}

type rateCounter struct {
	resetAt atomic.Int64
	count   atomic.Uint64
}

// incCheckReset increments the counter, resetting it when the tick expired.
func (c *rateCounter) incCheckReset(now, tick int64) uint64 {
	resetAt := c.resetAt.Load()
	if resetAt > now {
		return c.count.Add(1)
	}
	c.count.Store(1)
	if !c.resetAt.CompareAndSwap(resetAt, now+tick) {
		// Another goroutine reset it concurrently.
		return c.count.Add(1)
	}
	return 1
}

// NewRateLimitHandler wraps next with rate limiting.
func NewRateLimitHandler(next slog.Handler, cfg RateLimitConfig) *RateLimitHandler {
	if cfg.Tick <= 0 {
		cfg.Tick = time.Second
	}
	if cfg.First == 0 {
		cfg.First = 100
	}
	return &RateLimitHandler{
		next: next,
		s: &rateLimiter{
			tick:       int64(cfg.Tick),
			first:      cfg.First,
			thereafter: cfg.Thereafter,
		},
	}
}

// Enabled implements slog.Handler.
func (h *RateLimitHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

// Handle implements slog.Handler.
func (h *RateLimitHandler) Handle(ctx context.Context, r slog.Record) error {
	if !h.s.allow(r) {
		h.s.dropped.Add(1)
		return nil
	}
	return h.next.Handle(ctx, r)
}

// WithAttrs implements slog.Handler.
func (h *RateLimitHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &RateLimitHandler{next: h.next.WithAttrs(attrs), s: h.s}
}

// WithGroup implements slog.Handler.
func (h *RateLimitHandler) WithGroup(name string) slog.Handler {
	return &RateLimitHandler{next: h.next.WithGroup(name), s: h.s}
}

// Dropped returns the number of records dropped so far.
func (h *RateLimitHandler) Dropped() uint64 { return h.s.dropped.Load() }

// Unwrap returns the wrapped handler.
func (h *RateLimitHandler) Unwrap() slog.Handler { return h.next }

func (s *rateLimiter) allow(r slog.Record) bool {
	now := r.Time
	if now.IsZero() {
		now = time.Now()
	}
	c := &s.counters[levelIndex(r.Level)][fnv32a(r.Message)%rateLimitBuckets]
	n := c.incCheckReset(now.UnixNano(), s.tick)
	if n <= s.first {
		return true
	}
	return s.thereafter > 0 && (n-s.first)%s.thereafter == 0
}

func levelIndex(l slog.Level) int {
	switch {
	case l < slog.LevelInfo:
		return 0
	case l < slog.LevelWarn:
		return 1
	case l < slog.LevelError:
		return 2
	default:
		return 3
	}
}

// fnv32a hashes s without allocating.
func fnv32a(s string) uint32 {
	const (
		offset32 = 2166136261
		prime32  = 16777619
	)
	h := uint32(offset32)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= prime32
	}
	return h
}

// RateLimitStats returns the number of records dropped by the rate limiter
// of l, and false if l has no rate limiter (see WithRateLimit).
func RateLimitStats(l *slog.Logger) (dropped uint64, ok bool) {
	if l == nil {
		return 0, false
	}
	if h, isRL := l.Handler().(*RateLimitHandler); isRL {
		return h.Dropped(), true
	}
	return 0, false
}
