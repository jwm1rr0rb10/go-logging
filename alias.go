// Package logging is a thin, context-aware wrapper around log/slog for
// high-throughput services: HTTP middleware with request IDs, trace
// correlation, completion records and sampling; a non-blocking async writer;
// log storm rate limiting; and request ID propagation to downstream calls.
//
// Every type is an alias of the log/slog type, so a *logging.Logger is a
// *slog.Logger. gRPC interceptors live in the separate module
// github.com/jwm1rr0rb10/go-logging/grpc/v2.
package logging

import "log/slog"

// Log levels (aliases of slog levels).
const (
	LevelDebug = slog.LevelDebug
	LevelInfo  = slog.LevelInfo
	LevelWarn  = slog.LevelWarn
	LevelError = slog.LevelError
)

// Type aliases for slog types, so values from this package and from
// log/slog are fully interchangeable.
type (
	Logger         = slog.Logger
	Attr           = slog.Attr
	Level          = slog.Level
	LevelVar       = slog.LevelVar
	Leveler        = slog.Leveler
	Handler        = slog.Handler
	Value          = slog.Value
	HandlerOptions = slog.HandlerOptions
	LogValuer      = slog.LogValuer
)

// Handler constructors and global functions (aliases of slog functions).
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
	TimeAttr     = slog.Time

	GroupValue = slog.GroupValue
	Group      = slog.Group
)

// Float32Attr returns an Attr for a float32 value.
func Float32Attr(key string, val float32) Attr {
	return slog.Float64(key, float64(val))
}

// UInt32Attr returns an Attr for a uint32 value.
func UInt32Attr(key string, val uint32) Attr {
	return slog.Uint64(key, uint64(val))
}

// Int32Attr returns an Attr for an int32 value.
func Int32Attr(key string, val int32) Attr {
	return slog.Int64(key, int64(val))
}

// ErrAttr returns an Attr with the key "error".
func ErrAttr(err error) Attr {
	return slog.Any("error", err)
}
