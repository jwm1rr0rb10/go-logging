package logging

import (
	"log/slog"
	"time"
)

// Constants for log levels (aliases from slog).
const (
	LevelInfo  = slog.LevelInfo
	LevelWarn  = slog.LevelWarn
	LevelError = slog.LevelError
	LevelDebug = slog.LevelDebug
)

// Type aliases for slog types.
type (
	Logger         = slog.Logger
	Attr           = slog.Attr
	Level          = slog.Level
	Handler        = slog.Handler
	Value          = slog.Value
	HandlerOptions = slog.HandlerOptions
	LogValuer      = slog.LogValuer
)

// Handler constructors and global functions (aliases from slog).
var (
	NewTextHandler = slog.NewTextHandler
	NewJSONHandler = slog.NewJSONHandler
	New            = slog.New
	SetDefault     = slog.SetDefault

	StringAttr   = slog.String
	BoolAttr     = slog.Bool
	Float64Attr  = slog.Float64
	AnyAttr      = slog.Any
	DurationAttr = slog.Duration
	IntAttr      = slog.Int
	Int64Attr    = slog.Int64
	Uint64Attr   = slog.Uint64

	GroupValue = slog.GroupValue
	Group      = slog.Group
)

// Float32Attr converts float32 to slog.Float64 attribute.
func Float32Attr(key string, val float32) Attr {
	return slog.Float64(key, float64(val))
}

// UInt32Attr is now safe.
func UInt32Attr(key string, val uint32) Attr {
	return slog.Uint64(key, uint64(val))
}

// Int32Attr is unchanged.
func Int32Attr(key string, val int32) Attr {
	return slog.Int(key, int(val))
}

// TimeAttr now uses slog.Time.
func TimeAttr(key string, t time.Time) Attr {
	return slog.Time(key, t)
}

// ErrAttr uses slog.Any (modern standard, handles nil).
func ErrAttr(err error) Attr {
	return slog.Any("error", err)
}
