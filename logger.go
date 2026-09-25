package logging

import (
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"strings"
)

// Environment variables checked (in this order) for the log level.
var levelEnvKeys = []string{"LOG_LEVEL", "SLOG_LEVEL"}

// levelNotSet marks LoggerOptions.Level as not configured explicitly.
const levelNotSet = Level(math.MinInt32)

// LoggerOptions holds the logger configuration.
// Use the With* functional options instead of filling it directly.
type LoggerOptions struct {
	// Level is the explicitly configured level. NewLogger initializes it to
	// an internal "not set" sentinel; any other value counts as explicit.
	Level Level
	// LevelVar, if set, is used as the dynamic level of the logger.
	LevelVar *slog.LevelVar
	// AddSource adds file:line to every record. Costly on hot paths.
	AddSource bool
	// IsJSON selects JSON output (true) or text output (false).
	IsJSON bool
	// SetDefault makes the logger the global slog default.
	SetDefault bool
	// Writer is the output destination. Defaults to os.Stdout.
	Writer io.Writer
	// DevMode enables text output, debug level and source locations.
	DevMode bool
	// ReplaceAttr is passed to the slog handler (redaction, renaming).
	ReplaceAttr func(groups []string, a Attr) Attr
	// Handler, if set, is used as is; Writer, IsJSON, AddSource and
	// ReplaceAttr are ignored.
	Handler Handler
	// RateLimit, if set, wraps the handler with NewRateLimitHandler.
	RateLimit *RateLimitConfig

	addSourceSet bool
	isJSONSet    bool
}

// LoggerOption configures NewLogger.
type LoggerOption func(*LoggerOptions)

// NewLogger creates a logger.
//
// Level priority, from highest to lowest:
//  1. WithLevel (or the current value of WithLevelVar's LevelVar if WithLevel is not used)
//  2. LOG_LEVEL / SLOG_LEVEL environment variables
//  3. debug in DevMode, info otherwise
//
// Invalid level strings are reported to stderr and ignored.
func NewLogger(opts ...LoggerOption) *Logger {
	cfg := &LoggerOptions{
		Level:      levelNotSet,
		IsJSON:     true,
		SetDefault: true,
		Writer:     os.Stdout,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}

	if cfg.DevMode {
		if !cfg.isJSONSet {
			cfg.IsJSON = false
		}
		if !cfg.addSourceSet {
			cfg.AddSource = true
		}
	}

	lv := cfg.LevelVar
	if lv == nil {
		lv = new(slog.LevelVar)
		lv.Set(resolveLevel(cfg))
	} else if cfg.Level != levelNotSet {
		lv.Set(resolveLevel(cfg))
	}

	h := cfg.Handler
	if h == nil {
		ho := &slog.HandlerOptions{
			AddSource:   cfg.AddSource,
			Level:       lv,
			ReplaceAttr: cfg.ReplaceAttr,
		}
		if cfg.IsJSON {
			h = slog.NewJSONHandler(cfg.Writer, ho)
		} else {
			h = slog.NewTextHandler(cfg.Writer, ho)
		}
	}

	if cfg.RateLimit != nil {
		h = NewRateLimitHandler(h, *cfg.RateLimit)
	}

	logger := slog.New(h)
	if cfg.SetDefault {
		slog.SetDefault(logger)
	}
	return logger
}

func resolveLevel(cfg *LoggerOptions) Level {
	if cfg.Level != levelNotSet {
		return cfg.Level
	}
	for _, key := range levelEnvKeys {
		v := os.Getenv(key)
		if v == "" {
			continue
		}
		if l, err := ParseLevel(v); err == nil {
			return l
		}
		fmt.Fprintf(os.Stderr, "logging: invalid %s=%q, ignoring\n", key, v)
	}
	if cfg.DevMode {
		return LevelDebug
	}
	return LevelInfo
}

// ParseLevel parses "debug", "info", "warn"/"warning", "error"
// (case-insensitive, optionally with an offset like "info+2").
func ParseLevel(s string) (Level, error) {
	s = strings.TrimSpace(s)
	if strings.EqualFold(s, "warning") {
		return LevelWarn, nil
	}
	var l Level
	if err := l.UnmarshalText([]byte(s)); err != nil {
		return LevelInfo, err
	}
	return l, nil
}

// WithLevel sets the log level ("debug", "info", "warn", "error").
// It takes priority over the LOG_LEVEL / SLOG_LEVEL environment variables.
// An invalid value is reported to stderr and ignored.
func WithLevel(level string) LoggerOption {
	return func(o *LoggerOptions) {
		l, err := ParseLevel(level)
		if err != nil {
			fmt.Fprintf(os.Stderr, "logging: invalid level %q passed to WithLevel, ignoring\n", level)
			return
		}
		o.Level = l
	}
}

// WithLevelVar makes the logger use lv as its level, so the level can be
// changed at runtime with lv.Set(...) without recreating the logger.
// If WithLevel is not used, lv keeps its current value and the
// environment variables are not consulted.
func WithLevelVar(lv *slog.LevelVar) LoggerOption {
	return func(o *LoggerOptions) { o.LevelVar = lv }
}

// WithDevMode enables text output, debug level and source locations,
// unless they are set explicitly by other options.
func WithDevMode(enabled bool) LoggerOption {
	return func(o *LoggerOptions) { o.DevMode = enabled }
}

// WithAddSource enables or disables file:line in records.
// Disabled by default: it calls runtime.Callers on every record.
func WithAddSource(addSource bool) LoggerOption {
	return func(o *LoggerOptions) {
		o.AddSource = addSource
		o.addSourceSet = true
	}
}

// WithIsJSON selects JSON (true, default) or text (false) output.
func WithIsJSON(isJSON bool) LoggerOption {
	return func(o *LoggerOptions) {
		o.IsJSON = isJSON
		o.isJSONSet = true
	}
}

// WithSetDefault controls whether the logger becomes the global slog
// default (true by default).
func WithSetDefault(setDefault bool) LoggerOption {
	return func(o *LoggerOptions) { o.SetDefault = setDefault }
}

// WithWriter sets the output destination. A nil writer is ignored.
// For high-throughput services use NewAsyncWriter.
func WithWriter(w io.Writer) LoggerOption {
	return func(o *LoggerOptions) {
		if w != nil {
			o.Writer = w
		}
	}
}

// WithReplaceAttr sets slog's ReplaceAttr hook, e.g. to redact secrets.
func WithReplaceAttr(fn func(groups []string, a Attr) Attr) LoggerOption {
	return func(o *LoggerOptions) { o.ReplaceAttr = fn }
}

// WithHandler uses a custom slog.Handler. Writer, IsJSON, AddSource and
// ReplaceAttr are ignored when a handler is provided.
func WithHandler(h Handler) LoggerOption {
	return func(o *LoggerOptions) { o.Handler = h }
}

// WithRateLimit limits records per (level, message) and interval to
// protect the service from log storms; see RateLimitConfig. It also applies
// to a handler passed with WithHandler. Read the drop counter with
// RateLimitStats(logger).
func WithRateLimit(cfg RateLimitConfig) LoggerOption {
	return func(o *LoggerOptions) { o.RateLimit = &cfg }
}

// Default returns the global default logger.
func Default() *Logger {
	return slog.Default()
}
