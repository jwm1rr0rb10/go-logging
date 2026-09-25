package logging

import (
	"context"
	"log/slog"
	"sync/atomic"
)

// ctxScopeKey is the single context key used by this package. The logger,
// the request ID and pending attributes live in one *scope value, so a
// request costs one context.WithValue instead of several.
type ctxScopeKey struct{}

// inlineAttrs is the number of attributes a scope stores without a separate
// allocation. The HTTP middleware needs at most 6 (request_id, method,
// endpoint, remote_addr, trace_id, span_id).
const inlineAttrs = 6

// scope is a lazily materialized logger.
//
// Creating a scope is cheap: it only records the base logger (or a parent
// scope) and attributes. The *slog.Logger with those attributes (which makes
// the handler clone and pre-serialize them) is built only when somebody
// actually asks for it via L(ctx). Requests that never log, e.g. successful
// requests dropped by sampling, never pay for logger.With.
type scope struct {
	parent *scope       // enclosing scope, used when base is nil
	base   *slog.Logger // explicit base logger; nil means parent or slog.Default
	id     string       // request ID, inherited from the parent by default
	attrs  []slog.Attr  // attributes added by this scope

	logger atomic.Pointer[slog.Logger] // materialized logger, set once

	inline [inlineAttrs]slog.Attr // backing storage for small attrs
}

func scopeFrom(ctx context.Context) *scope {
	if ctx == nil {
		return nil
	}
	s, _ := ctx.Value(ctxScopeKey{}).(*scope)
	return s
}

// resolveBase returns the logger this scope's attributes are applied to.
func (s *scope) resolveBase() *slog.Logger {
	if s.base != nil {
		return s.base
	}
	if s.parent != nil {
		return s.parent.get()
	}
	return slog.Default()
}

// get returns the materialized logger, building it on first use.
// It is safe for concurrent use; a rare race builds two equivalent loggers
// and one of them wins.
func (s *scope) get() *slog.Logger {
	if l := s.logger.Load(); l != nil {
		return l
	}
	l := s.resolveBase()
	if len(s.attrs) > 0 {
		l = slog.New(l.Handler().WithAttrs(s.attrs))
	}
	if s.logger.CompareAndSwap(nil, l) {
		return l
	}
	return s.logger.Load()
}

// logAttrs writes a record with the scope attributes followed by extra.
// If the logger has not been materialized yet, the attributes are passed
// with the record instead of cloning the handler, which is cheaper for a
// single record (the common case for completion records).
func (s *scope) logAttrs(ctx context.Context, level slog.Level, msg string, extra ...slog.Attr) {
	if l := s.logger.Load(); l != nil {
		if l.Enabled(ctx, level) {
			l.LogAttrs(ctx, level, msg, extra...)
		}
		return
	}
	base := s.resolveBase()
	if !base.Enabled(ctx, level) {
		return
	}
	var buf [16]slog.Attr
	all := append(append(buf[:0], s.attrs...), extra...)
	base.LogAttrs(ctx, level, msg, all...)
}

// enabled reports whether a record at level would be written.
func (s *scope) enabled(ctx context.Context, level slog.Level) bool {
	if l := s.logger.Load(); l != nil {
		return l.Enabled(ctx, level)
	}
	return s.resolveBase().Enabled(ctx, level)
}

func withScope(ctx context.Context, s *scope) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, ctxScopeKey{}, s)
}

// ContextWithLogger returns a copy of ctx that carries l.
// A request ID already present in ctx is preserved.
func ContextWithLogger(ctx context.Context, l *slog.Logger) context.Context {
	s := &scope{base: l}
	if p := scopeFrom(ctx); p != nil {
		s.id = p.id
		if l == nil {
			s.parent = p
		}
	}
	if l != nil {
		s.logger.Store(l)
	}
	return withScope(ctx, s)
}

// L returns the logger stored in ctx, or the global default logger
// if ctx is nil or carries no logger.
func L(ctx context.Context) *slog.Logger {
	if s := scopeFrom(ctx); s != nil {
		return s.get()
	}
	return slog.Default()
}

// ContextWithRequestID returns a copy of ctx that carries the request ID.
// The logger in ctx is kept as is (the ID is not added to it).
func ContextWithRequestID(ctx context.Context, id string) context.Context {
	return withScope(ctx, &scope{parent: scopeFrom(ctx), id: id})
}

// RequestIDFromContext returns the request ID set by the HTTP middleware or
// gRPC interceptors, or "" if there is none. Use it to propagate the ID to
// downstream calls (NewTransport and the gRPC client interceptors do this).
func RequestIDFromContext(ctx context.Context) string {
	if s := scopeFrom(ctx); s != nil {
		return s.id
	}
	return ""
}

// WithAttrs returns the logger from ctx enriched with attrs.
func WithAttrs(ctx context.Context, attrs ...Attr) *Logger {
	logger := L(ctx)
	if len(attrs) == 0 {
		return logger
	}
	return slog.New(logger.Handler().WithAttrs(attrs))
}

// ContextWithAttrs enriches the logger in ctx with attrs and returns the
// updated context. The returned context must be used, e.g.:
//
//	ctx = logging.ContextWithAttrs(ctx, logging.StringAttr("user_id", id))
//
// The enriched logger is built lazily, on the first L(ctx) call.
func ContextWithAttrs(ctx context.Context, attrs ...Attr) context.Context {
	if len(attrs) == 0 {
		if ctx == nil {
			return context.Background()
		}
		return ctx
	}
	p := scopeFrom(ctx)
	s := &scope{parent: p}
	if p != nil {
		s.id = p.id
	}
	if len(attrs) <= inlineAttrs {
		s.attrs = append(s.inline[:0], attrs...)
	} else {
		s.attrs = append([]slog.Attr(nil), attrs...)
	}
	return withScope(ctx, s)
}
