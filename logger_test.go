package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

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

func newTestLogger(buf *bytes.Buffer, opts ...LoggerOption) *Logger {
	base := []LoggerOption{WithWriter(buf), WithSetDefault(false)}
	return NewLogger(append(base, opts...)...)
}

func TestNewLoggerDefaults(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(&buf)

	l.Debug("hidden")
	l.Info("visible", StringAttr("k", "v"))

	lines := decodeLines(t, &buf)
	if len(lines) != 1 {
		t.Fatalf("want 1 line (debug filtered), got %d", len(lines))
	}
	if lines[0]["msg"] != "visible" || lines[0]["k"] != "v" {
		t.Fatalf("unexpected record: %v", lines[0])
	}
	if _, ok := lines[0]["source"]; ok {
		t.Fatal("source must be disabled by default")
	}
}

func TestLevelPriority(t *testing.T) {
	tests := []struct {
		name string
		env  string
		opts []LoggerOption
		want Level
	}{
		{"default", "", nil, LevelInfo},
		{"env", "warn", nil, LevelWarn},
		{"explicit info beats env", "debug", []LoggerOption{WithLevel("info")}, LevelInfo},
		{"explicit beats env", "debug", []LoggerOption{WithLevel("error")}, LevelError},
		{"dev mode default", "", []LoggerOption{WithDevMode(true)}, LevelDebug},
		{"explicit info in dev mode", "", []LoggerOption{WithDevMode(true), WithLevel("info")}, LevelInfo},
		{"env in dev mode", "error", []LoggerOption{WithDevMode(true)}, LevelError},
		{"invalid explicit falls back to env", "warn", []LoggerOption{WithLevel("loud")}, LevelWarn},
		{"invalid env ignored", "loud", nil, LevelInfo},
		{"warning alias", "warning", nil, LevelWarn},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("LOG_LEVEL", tt.env)
			t.Setenv("SLOG_LEVEL", "")
			var buf bytes.Buffer
			l := newTestLogger(&buf, tt.opts...)
			ctx := context.Background()
			if !l.Enabled(ctx, tt.want) {
				t.Fatalf("level %v must be enabled", tt.want)
			}
			if l.Enabled(ctx, tt.want-1) {
				t.Fatalf("level %v must be disabled", tt.want-1)
			}
		})
	}
}

func TestLevelVarRuntimeChange(t *testing.T) {
	var buf bytes.Buffer
	lv := new(slog.LevelVar)
	l := newTestLogger(&buf, WithLevelVar(lv))

	l.Debug("hidden")
	lv.Set(LevelDebug)
	l.Debug("shown")

	lines := decodeLines(t, &buf)
	if len(lines) != 1 || lines[0]["msg"] != "shown" {
		t.Fatalf("unexpected output: %v", lines)
	}
}

func TestDevModeRespectsExplicitOptions(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(&buf, WithDevMode(true), WithIsJSON(true), WithAddSource(false))
	l.Debug("dbg")
	lines := decodeLines(t, &buf) // fails if output is not JSON
	if _, ok := lines[0]["source"]; ok {
		t.Fatal("explicit WithAddSource(false) must win over dev mode")
	}
}

func TestReplaceAttrRedaction(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(&buf, WithReplaceAttr(func(_ []string, a Attr) Attr {
		if a.Key == "password" {
			return StringAttr("password", "***")
		}
		return a
	}))
	l.Info("login", StringAttr("password", "secret"))
	if got := decodeLines(t, &buf)[0]["password"]; got != "***" {
		t.Fatalf("password not redacted: %v", got)
	}
}

func TestContextHelpers(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(&buf)

	ctx := ContextWithLogger(context.Background(), l)
	ctx = ContextWithAttrs(ctx, StringAttr("user_id", "42"), IntAttr("n", 1))
	L(ctx).Info("hello")

	rec := decodeLines(t, &buf)[0]
	if rec["user_id"] != "42" || rec["n"] != float64(1) {
		t.Fatalf("attrs missing: %v", rec)
	}

	//nolint:staticcheck // nil context must be handled gracefully
	if L(nil) == nil {
		t.Fatal("L(nil) must return the default logger")
	}
	if got := ContextWithAttrs(ctx); got != ctx {
		t.Fatal("ContextWithAttrs without attrs must return ctx unchanged")
	}
}
