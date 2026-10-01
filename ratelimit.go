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
// When a key that had records dropped is logged again in a new interval, a
// summary record is written first, so the drops are visible in the logs and
// not only in RateLimitStats:
//
//	{"level":"ERROR","msg":"log records suppressed","suppressed_msg":"request completed","suppressed":9900,"interval":1000000000}
//
// The summary has the level of the suppressed records and none of their
// attributes. It is written when the key reappears, so after the storm ends
// the last interval is reported only by the counter.
//
// The counters are lock-free and allocation-free. Loggers derived with
// With/WithGroup share the same counters. Counting is approximate under
// heavy concurrency around an interval boundary (a few extra records may
// pass), which is fine for its purpose.
type RateLimitHandler struct {
	next slog.Handler
	s    *rateLimiter
}

type rateLimiter struct {
	root       slog.Handler // handler without attrs/groups, for summaries
	tick       int64
	first      uint64
	thereafter uint64
	dropped    atomic.Uint64
	counters   [rateLimitLevels][rateLimitBuckets]rateCounter
}

type rateCounter struct {
	resetAt atomic.Int64
	count   atomic.Uint64
	dropped atomic.Uint64 // dropped in the current interval
}

// incCheckReset increments the counter, resetting it when the tick expired.
// It reports whether this call started a new interval.
func (c *rateCounter) incCheckReset(now, tick int64) (n uint64, reset bool) {
	resetAt := c.resetAt.Load()
	if resetAt > now {
		return c.count.Add(1), false
	}
	if !c.resetAt.CompareAndSwap(resetAt, now+tick) {
		// Another goroutine reset it concurrently.
		return c.count.Add(1), false
	}
	c.count.Store(1)
	return 1, true
}

// NewRateLimitHandler wraps next with rate limiting.
func NewRateLimitHandler(next slog.Handler, cfg RateLimitConfig) *RateLimitHandler {
	if cfg.Tick <= 0 {
		cfg.Tick = time.Second
	}
	if cfg.First == 0 {
		cfg.First = 100
	}
	s := &rateLimiter{
		root:       next,
		tick:       int64(cfg.Tick),
		first:      cfg.First,
		thereafter: cfg.Thereafter,
	}
	// Start the first interval now, so concurrent first records of a key
	// do not race to reset its counter.
	resetAt := time.Now().UnixNano() + s.tick
	for i := range s.counters {
		for j := range s.counters[i] {
			s.counters[i][j].resetAt.Store(resetAt)
		}
	}
	return &RateLimitHandler{next: next, s: s}
}

// Enabled implements slog.Handler.
func (h *RateLimitHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

// Handle implements slog.Handler.
func (h *RateLimitHandler) Handle(ctx context.Context, r slog.Record) error {
	ok, suppressed := h.s.allow(r)
	if suppressed > 0 {
		h.s.reportSuppressed(ctx, r, suppressed)
	}
	if !ok {
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

// allow reports whether r may be written and, if r starts a new interval,
// how many records of its key were dropped in the previous one.
func (s *rateLimiter) allow(r slog.Record) (ok bool, suppressed uint64) {
	now := r.Time
	if now.IsZero() {
		now = time.Now()
	}
	c := &s.counters[levelIndex(r.Level)][fnv32a(r.Message)%rateLimitBuckets]
	n, reset := c.incCheckReset(now.UnixNano(), s.tick)
	if reset {
		suppressed = c.dropped.Swap(0)
	}
	if n <= s.first || (s.thereafter > 0 && (n-s.first)%s.thereafter == 0) {
		return true, suppressed
	}
	c.dropped.Add(1)
	s.dropped.Add(1)
	return false, suppressed
}

// reportSuppressed writes the summary for the previous interval of r's key.
// It goes to the root handler: the attributes of r (a single request) would
// be misleading for an aggregate.
func (s *rateLimiter) reportSuppressed(ctx context.Context, r slog.Record, n uint64) {
	sum := slog.NewRecord(r.Time, r.Level, "log records suppressed", 0)
	sum.AddAttrs(
		slog.String("suppressed_msg", r.Message),
		slog.Uint64("suppressed", n),
		slog.Duration("interval", time.Duration(s.tick)),
	)
	_ = s.root.Handle(ctx, sum)
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
