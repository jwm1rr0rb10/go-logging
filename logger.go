package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
)

// Default logger configuration constants.
const (
	defaultLevel      = LevelInfo
	defaultAddSource  = true
	defaultIsJSON     = true
	defaultSetDefault = true
)

// NewLogger creates a configurable logger.
func NewLogger(opts ...LoggerOption) *Logger {
	config := &LoggerOptions{
		Level:      defaultLevel,
		AddSource:  defaultAddSource,
		IsJSON:     defaultIsJSON,
		SetDefault: defaultSetDefault,
		Writer:     os.Stdout,
		DevMode:    false,
	}

	for _, opt := range opts {
		opt(config)
	}

	// Load level from environment variable by default (LOG_LEVEL or SLOG_LEVEL)
	if config.Level == defaultLevel {
		for _, key := range []string{"LOG_LEVEL", "SLOG_LEVEL"} {
			if v := os.Getenv(key); v != "" {
				var l Level
				if err := l.UnmarshalText([]byte(v)); err == nil {
					config.Level = l
					break
				} else {
					fmt.Fprintf(os.Stderr, "invalid %s %q, using info\n", key, v)
				}
			}
		}
	}

	if config.DevMode {
		config.IsJSON = false
		if config.Level == defaultLevel {
			config.Level = LevelDebug // dev mode defaults to debug
		}
	}

	options := &HandlerOptions{
		AddSource: config.AddSource,
		Level:     config.Level,
	}

	var h Handler
	if config.IsJSON {
		h = NewJSONHandler(config.Writer, options)
	} else {
		h = NewTextHandler(config.Writer, options)
	}

	logger := New(h)
	if config.SetDefault {
		SetDefault(logger)
	}

	return logger
}

// LoggerOptions holds configuration.
type LoggerOptions struct {
	Level      Level
	AddSource  bool
	IsJSON     bool
	SetDefault bool
	Writer     io.Writer
	DevMode    bool
}

// LoggerOption functional options.
type LoggerOption func(*LoggerOptions)

// WithLevel sets the log level (still overrides env).
func WithLevel(level string) LoggerOption {
	return func(o *LoggerOptions) {
		var l Level
		if err := l.UnmarshalText([]byte(level)); err != nil {
			fmt.Fprintf(os.Stderr, "failed to parse log level %q, using info\n", level)
			l = LevelInfo
		}
		o.Level = l
	}
}

// WithDevMode enables pretty text output + debug level (great for local development).
func WithDevMode(enabled bool) LoggerOption {
	return func(o *LoggerOptions) {
		o.DevMode = enabled
	}
}

// WithAddSource enables/disables source file logging.
func WithAddSource(addSource bool) LoggerOption {
	return func(o *LoggerOptions) {
		o.AddSource = addSource
	}
}

// WithIsJSON sets JSON vs text output.
func WithIsJSON(isJSON bool) LoggerOption {
	return func(o *LoggerOptions) {
		o.IsJSON = isJSON
	}
}

// WithSetDefault sets the logger as the default.
func WithSetDefault(setDefault bool) LoggerOption {
	return func(o *LoggerOptions) {
		o.SetDefault = setDefault
	}
}

// WithWriter allows custom output.
func WithWriter(w io.Writer) LoggerOption {
	return func(o *LoggerOptions) {
		if w != nil {
			o.Writer = w
		}
	}
}

// WithAttrs adds attributes and returns a new logger.
func WithAttrs(ctx context.Context, attrs ...Attr) *Logger {
	logger := L(ctx)
	for _, attr := range attrs {
		logger = logger.With(attr)
	}
	return logger
}

// ContextWithAttrs adds attributes and updates the context in one call.
// Самая удобная функция для большинства случаев.
func ContextWithAttrs(ctx context.Context, attrs ...Attr) context.Context {
	return ContextWithLogger(ctx, WithAttrs(ctx, attrs...))
}

// L retrieves the logger from the context.
func L(ctx context.Context) *Logger {
	return loggerFromContext(ctx)
}

// Default returns the global default logger.
func Default() *Logger {
	return slog.Default()
}
