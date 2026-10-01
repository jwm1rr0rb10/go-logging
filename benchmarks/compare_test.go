// Package benchmarks compares go-logging with zap and zerolog. It is a
// separate module so the library does not depend on them. Run: make compare.
package benchmarks

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	logging "github.com/jwm1rr0rb10/go-logging/v2"
)

const msg = "request completed"

func newLogging() *logging.Logger {
	return logging.NewLogger(logging.WithWriter(io.Discard))
}

func newZap() *zap.Logger {
	enc := zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig())
	return zap.New(zapcore.NewCore(enc, zapcore.AddSync(io.Discard), zapcore.InfoLevel))
}

func newZerolog() zerolog.Logger {
	return zerolog.New(io.Discard).With().Timestamp().Logger().Level(zerolog.InfoLevel)
}

// Request attributes: what the middleware puts into the request logger.
var (
	reqID    = "9f86d081884c7d65"
	method   = "GET"
	endpoint = "/users/42"
	remote   = "10.0.0.7:51234"
)

func run(b *testing.B, f func()) {
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			f()
		}
	})
}

// A completion-style record with 4 fields.
func BenchmarkRecord(b *testing.B) {
	ctx := context.Background()
	b.Run("go-logging", func(b *testing.B) {
		l := newLogging()
		run(b, func() {
			l.LogAttrs(ctx, logging.LevelInfo, msg,
				logging.IntAttr("status", 200), logging.Int64Attr("bytes", 512),
				logging.DurationAttr("duration", time.Millisecond), logging.StringAttr("route", "GET /users/{id}"))
		})
	})
	b.Run("zap", func(b *testing.B) {
		l := newZap()
		run(b, func() {
			l.Info(msg, zap.Int("status", 200), zap.Int64("bytes", 512),
				zap.Duration("duration", time.Millisecond), zap.String("route", "GET /users/{id}"))
		})
	})
	b.Run("zerolog", func(b *testing.B) {
		l := newZerolog()
		run(b, func() {
			l.Info().Int("status", 200).Int64("bytes", 512).
				Dur("duration", time.Millisecond).Str("route", "GET /users/{id}").Msg(msg)
		})
	})
}

// A record from a logger that carries 4 request attributes (pre-serialized).
func BenchmarkRecordWithContextFields(b *testing.B) {
	ctx := context.Background()
	b.Run("go-logging", func(b *testing.B) {
		ctx := logging.ContextWithAttrs(logging.ContextWithLogger(ctx, newLogging()),
			logging.StringAttr("request_id", reqID), logging.StringAttr("method", method),
			logging.StringAttr("endpoint", endpoint), logging.StringAttr("remote_addr", remote))
		l := logging.L(ctx)
		run(b, func() { l.LogAttrs(ctx, logging.LevelInfo, msg, logging.IntAttr("status", 200)) })
	})
	b.Run("zap", func(b *testing.B) {
		l := newZap().With(zap.String("request_id", reqID), zap.String("method", method),
			zap.String("endpoint", endpoint), zap.String("remote_addr", remote))
		run(b, func() { l.Info(msg, zap.Int("status", 200)) })
	})
	b.Run("zerolog", func(b *testing.B) {
		l := newZerolog().With().Str("request_id", reqID).Str("method", method).
			Str("endpoint", endpoint).Str("remote_addr", remote).Logger()
		run(b, func() { l.Info().Int("status", 200).Msg(msg) })
	})
}

// A Debug record when the level is Info.
func BenchmarkDisabled(b *testing.B) {
	ctx := context.Background()
	b.Run("go-logging", func(b *testing.B) {
		l := newLogging()
		run(b, func() { l.LogAttrs(ctx, logging.LevelDebug, msg, logging.IntAttr("status", 200)) })
	})
	b.Run("zap", func(b *testing.B) {
		l := newZap()
		run(b, func() { l.Debug(msg, zap.Int("status", 200)) })
	})
	b.Run("zerolog", func(b *testing.B) {
		l := newZerolog()
		run(b, func() { l.Debug().Int("status", 200).Msg(msg) })
	})
}

// Building a request logger with 4 fields and writing one record: the cost
// a middleware pays per logged request without go-logging's lazy scope.
func BenchmarkNewRequestLoggerAndLog(b *testing.B) {
	ctx := context.Background()
	b.Run("go-logging", func(b *testing.B) {
		base := logging.ContextWithLogger(ctx, newLogging())
		run(b, func() {
			ctx := logging.ContextWithAttrs(base,
				logging.StringAttr("request_id", reqID), logging.StringAttr("method", method),
				logging.StringAttr("endpoint", endpoint), logging.StringAttr("remote_addr", remote))
			logging.L(ctx).LogAttrs(ctx, logging.LevelInfo, msg, logging.IntAttr("status", 200))
		})
	})
	b.Run("zap", func(b *testing.B) {
		base := newZap()
		run(b, func() {
			base.With(zap.String("request_id", reqID), zap.String("method", method),
				zap.String("endpoint", endpoint), zap.String("remote_addr", remote)).
				Info(msg, zap.Int("status", 200))
		})
	})
	b.Run("zerolog", func(b *testing.B) {
		base := newZerolog()
		run(b, func() {
			l := base.With().Str("request_id", reqID).Str("method", method).
				Str("endpoint", endpoint).Str("remote_addr", remote).Logger()
			l.Info().Int("status", 200).Msg(msg)
		})
	})
}
