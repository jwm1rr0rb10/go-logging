package logging

import (
	"context"
	"log/slog"
	"sync/atomic"
)

// priorityHandler routes records at or above threshold to high and the rest
// to low. NewLogger uses it to send Warn and Error records through
// AsyncWriter.Priority. Both handlers must have the same options.
//
// Attributes and groups are applied to low eagerly and to high lazily, on
// the first high-level record: high-level records are rare, and a request
// logger built with With should not pay twice for pre-serializing its
// attributes.
type priorityHandler struct {
	low       slog.Handler
	threshold slog.Level

	parent *priorityHandler // nil for the root
	attrs  []slog.Attr      // applied to parent's high, or
	group  string           // the group opened on parent's high

	high atomic.Pointer[slog.Handler] // set for the root, built lazily below
}

func newPriorityHandler(low, high slog.Handler, threshold slog.Level) *priorityHandler {
	h := &priorityHandler{low: low, threshold: threshold}
	h.high.Store(&high)
	return h
}

// Enabled implements slog.Handler. low and high share the options, so low
// answers for both.
func (h *priorityHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.low.Enabled(ctx, level)
}

// Handle implements slog.Handler.
func (h *priorityHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Level >= h.threshold {
		return h.highHandler().Handle(ctx, r)
	}
	return h.low.Handle(ctx, r)
}

// WithAttrs implements slog.Handler.
func (h *priorityHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	return &priorityHandler{low: h.low.WithAttrs(attrs), threshold: h.threshold, parent: h, attrs: attrs}
}

// WithGroup implements slog.Handler.
func (h *priorityHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return &priorityHandler{low: h.low.WithGroup(name), threshold: h.threshold, parent: h, group: name}
}

// highHandler returns the high handler with this handler's attributes and
// groups, building it on first use. A rare race builds two equivalent
// handlers and one of them wins.
func (h *priorityHandler) highHandler() slog.Handler {
	if p := h.high.Load(); p != nil {
		return *p
	}
	hh := h.parent.highHandler()
	if h.group != "" {
		hh = hh.WithGroup(h.group)
	} else {
		hh = hh.WithAttrs(h.attrs)
	}
	h.high.CompareAndSwap(nil, &hh)
	return *h.high.Load()
}
