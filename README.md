# go-logging

[Русская версия](READMEru.md)

A thin, context-aware wrapper around Go's `log/slog` with HTTP middleware and
gRPC interceptors: request IDs, trace correlation, completion records,
sampling and panic logging. Built for high-throughput services: a
non-blocking async writer with a drop policy and a reserve for Warn/Error
records, log storm rate limiting with summary records, trace-consistent
sampling across services, lazily built request loggers (6 allocations per
request that does not log) and request ID propagation to downstream HTTP/gRPC
calls.

Every type is an alias of the `log/slog` type, so a `*logging.Logger` is a
`*slog.Logger` and works with the whole slog ecosystem.

## Installation

```bash
go get github.com/jwm1rr0rb10/go-logging/v2

# gRPC interceptors (a separate module, so HTTP-only services do not depend on gRPC)
go get github.com/jwm1rr0rb10/go-logging/grpc/v2
```

Requires Go 1.25+ (see `go.mod`). The only dependency of the core module is
`go.opentelemetry.io/otel/trace`.

## Migrating from v1

| v1 | v2 |
|---|---|
| `import "github.com/jwm1rr0rb10/go-logging"` | `import logging "github.com/jwm1rr0rb10/go-logging/v2"` |
| `NewLogger()` called `slog.SetDefault` | It does not by default. Add `logging.WithSetDefault(true)` in `main` if code that uses `slog.Info(...)` or `L(ctx)` without a context logger should use it. |
| `logging.UnaryServerInterceptor`, `StreamServerInterceptor`, `UnaryClientInterceptor`, `StreamClientInterceptor` | Same names in `logginggrpc "github.com/jwm1rr0rb10/go-logging/grpc/v2"`; the options are still `logging.With...`. |
| `logging.WithTraceIDInLogger()` (deprecated) | Removed: `logginggrpc.UnaryServerInterceptor(logging.WithLogCompletion(false))`. |
| Completion record: `status`, `duration`, `bytes` | Adds `route` (the `http.ServeMux` pattern) and puts `duration` last: `route`, `status`, `bytes`, `duration`. |
| `WithSampling(n)`: every n-th request | Requests with a trace ID are sampled by the trace ID (consistent across services); `WithTraceSampling(false)` restores the counter. |

## Quick start

```go
package main

import (
	"net/http"

	logging "github.com/jwm1rr0rb10/go-logging/v2"
)

func main() {
	// JSON to stdout, level from LOG_LEVEL; also the slog default for code that uses slog.Info.
	logger := logging.NewLogger(logging.WithSetDefault(true))

	mux := http.NewServeMux()
	mux.HandleFunc("GET /users/{id}", func(w http.ResponseWriter, r *http.Request) {
		// The returned context must be used, otherwise the attributes are lost.
		ctx := logging.ContextWithAttrs(r.Context(), logging.StringAttr("user_id", r.PathValue("id")))

		logging.L(ctx).Info("loading user") // carries request_id, method, endpoint, user_id...
		w.WriteHeader(http.StatusOK)
	})

	handler := logging.NewMiddleware(logging.WithMiddlewareLogger(logger))(mux)
	_ = http.ListenAndServe(":8080", handler)
}
```

## Logger configuration

| Option | Default | Description |
|---|---|---|
| `WithLevel("debug")` | env / info | Level; wins over environment variables. An invalid value is reported to stderr and ignored. |
| `WithLevelVar(lv)` | — | Use a `*slog.LevelVar` to change the level at runtime with `lv.Set(...)`. |
| `WithDevMode(true)` | `false` | Text output, debug level, source locations (unless set explicitly). |
| `WithIsJSON(bool)` | `true` | JSON or text output. |
| `WithAddSource(bool)` | `false` | Add `file:line`. Costly on hot paths. |
| `WithSetDefault(bool)` | `false` | Call `slog.SetDefault`. A library must not change global state implicitly, so enable it in `main`. |
| `WithWriter(w)` | `os.Stdout` | Output destination. With an `*AsyncWriter`, Warn/Error records use its reserve, see [Async writer](#async-writer-recommended). |
| `WithReplaceAttr(fn)` | — | slog `ReplaceAttr` hook, e.g. to redact secrets. |
| `WithHandler(h)` | — | Use a custom `slog.Handler` (writer and format options are ignored). |
| `WithRateLimit(cfg)` | — | Limit records per (level, message) and interval, see [Log storms](#log-storms). |

**Level priority:** `WithLevel` → `LOG_LEVEL` / `SLOG_LEVEL` → debug in dev mode → info.
Accepted values: `debug`, `info`, `warn`/`warning`, `error` (case-insensitive),
optionally with an offset such as `info+2`.

### Changing the level at runtime

```go
lv := new(slog.LevelVar)
logger := logging.NewLogger(logging.WithLevelVar(lv))

lv.Set(logging.LevelDebug) // e.g. from an admin endpoint or a config reload
```

### Redacting secrets

```go
logger := logging.NewLogger(logging.WithReplaceAttr(func(_ []string, a logging.Attr) logging.Attr {
	switch a.Key {
	case "password", "token", "authorization":
		return logging.StringAttr(a.Key, "***")
	}
	return a
}))
```

## Context helpers

```go
l := logging.L(ctx)                                  // logger from ctx or slog default
ctx = logging.ContextWithLogger(ctx, l)              // put a logger into ctx
ctx = logging.ContextWithAttrs(ctx, attrs...)        // enrich the ctx logger (use the result!)
l = logging.WithAttrs(ctx, attrs...)                 // enriched logger without changing ctx
ctx = logging.ContextWithRequestID(ctx, id)          // set the request ID (the logger is unchanged)
id := logging.RequestIDFromContext(ctx)              // request ID set by middleware/interceptors
```

Loggers in the context are built **lazily**. `ContextWithAttrs` and the
middleware only record the attributes; the `*slog.Logger` (which clones the
handler and pre-serializes the attributes) is created on the first `L(ctx)`
call and cached. Requests that never log do not pay for it. `L(ctx)` is safe
for concurrent use.

Attribute helpers: `StringAttr`, `BoolAttr`, `IntAttr`, `Int32Attr`, `Int64Attr`,
`Uint64Attr`, `UInt32Attr`, `Float32Attr`, `Float64Attr`, `DurationAttr`,
`TimeAttr`, `AnyAttr`, `ErrAttr`, `Group`, `GroupValue`.

## HTTP middleware

```go
handler := logging.NewMiddleware(
	logging.WithMiddlewareLogger(logger),
	logging.WithSampling(100),                    // log 1 of 100 successful requests
	logging.WithSlowThreshold(500*time.Millisecond),
	logging.WithSkipPaths("/healthz", "/metrics"),
)(mux)
```

`logging.Middleware(next)` is the same with default options.

What it does:
- reads `X-Request-ID` (validated: max 128 chars, `[A-Za-z0-9-_.:/+=]`) or generates
  a new one, puts it into the context and the response header;
- puts a request-scoped logger into the context with `request_id`, `method`,
  `endpoint` (path only), `remote_addr`, and `trace_id` / `span_id` if an
  OpenTelemetry span is present;
- does it lazily: if the request does not log (e.g. dropped by sampling), no
  logger is built, and the completion record passes the request attributes with
  the record instead of cloning the handler;
- writes **one record per completed request** with `route`, `status`, `bytes`
  and `duration`: Error for 5xx and panics, Warn for 4xx and slow requests,
  Info otherwise. `route` is the `http.ServeMux` pattern (`GET /users/{id}`),
  convenient for aggregation, unlike `endpoint` (`/users/42`); it is omitted if
  no pattern matched or another router is used;
- keeps `http.Flusher`, `http.Hijacker`, `io.ReaderFrom` and `Unwrap()` working,
  so SSE, WebSockets, `http.ResponseController` and sendfile are not broken;
- logs panics with a stack trace and re-raises them (or answers 500 with
  `WithRecoverPanics(true)`); `http.ErrAbortHandler` is passed through without
  a record.

| Option | Default | Description |
|---|---|---|
| `WithMiddlewareLogger(l)` | ctx logger / slog default | Base logger. |
| `WithSampling(n)` | `1` | Log 1 of n successful requests; errors, panics and slow requests are always logged. |
| `WithTraceSampling(bool)` | `true` | Sample requests with a trace ID by the trace ID (see below); otherwise exactly every n-th request. |
| `WithSlowThreshold(d)` | off | Log slower requests at Warn with `"slow": true`. |
| `WithSkipPaths(p...)` | — | No completion record for these paths, except for panics (the context is still enriched). |
| `WithLogQuery(bool)` | `false` | Include the query string in `endpoint`. Off: query strings often contain tokens and personal data. |
| `WithRecoverPanics(bool)` | `false` | Recover panics and answer 500 instead of re-raising. |
| `WithRequestIDHeader(h)` | `X-Request-ID` | Header name (gRPC: metadata key, lower-cased). |
| `WithTrustRequestID(bool)` | `true` | Reuse incoming IDs or always generate new ones. Disable it for public services without a trusted proxy. |
| `WithMaxRequestIDLength(n)` | `128` | Max accepted incoming ID length. |
| `WithLogCompletion(bool)` | `true` | Disable completion records, keep context enrichment. |

Place the middleware **after** `otelhttp` so trace IDs are available:

```go
handler := otelhttp.NewHandler(logging.NewMiddleware()(mux), "server")
```

Example record:

```json
{"time":"2026-10-01T10:00:00Z","level":"INFO","msg":"request completed","request_id":"9f86d081884c7d65","method":"GET","endpoint":"/users/42","remote_addr":"10.0.0.7:51234","trace_id":"4bf92f3577b34da6a3ce929d0e0e4736","span_id":"00f067aa0ba902b7","route":"GET /users/{id}","status":200,"bytes":512,"duration":1843200}
```

### Sampling across services

With `WithSampling(n)`, a request that carries a valid trace ID is logged if
the low 8 bytes of the trace ID are divisible by n. The decision depends only
on the trace ID, so all services with the same n log **the same traces**,
and you see the whole request path, not random pieces of it. A trace logged
by a service with n = 1000 is also logged by services with n = 100 or 10
(any divisor of n), so choose powers of ten. Requests without a trace ID use
a counter (exactly every n-th request).

### Other transports

`RequestTracker` is the transport-agnostic core of the middleware (request
ID, lazy logger, sampling, slow requests, completion record). The gRPC
interceptors are built on it; use it for message queues or custom protocols:

```go
var tracker = logging.NewRequestTracker(logging.WithMiddlewareLogger(logger), logging.WithSampling(100))

func handle(msg *kafka.Message) {
	ctx, req := tracker.Start(context.Background(), msg.Topic, header(msg, "x-request-id"),
		logging.StringAttr("topic", msg.Topic))
	err := process(ctx, msg) // logging.L(ctx) carries request_id and topic
	level := logging.LevelInfo
	if err != nil {
		level = logging.LevelError
	}
	req.Finish(level, "message handled", nil, logging.ErrAttr(err))
}
```

## gRPC

```go
import logginggrpc "github.com/jwm1rr0rb10/go-logging/grpc/v2"

srv := grpc.NewServer(
	grpc.StatsHandler(otelgrpc.NewServerHandler()),
	grpc.ChainUnaryInterceptor(logginggrpc.UnaryServerInterceptor(logging.WithMiddlewareLogger(logger))),
	grpc.ChainStreamInterceptor(logginggrpc.StreamServerInterceptor(logging.WithMiddlewareLogger(logger))),
)
```

The interceptors accept the same options as the HTTP middleware
(`WithSkipPaths` takes full method names like `/grpc.health.v1.Health/Check`).
Each call produces an `rpc completed` record with `grpc_method`, `grpc_code` and `duration`.
Server-side codes (`Internal`, `Unknown`, `DataLoss`, `Unavailable`,
`DeadlineExceeded`, `Unimplemented`) are logged at Error, other non-OK codes at Warn.

To only enrich the context logger without completion records, use
`logginggrpc.UnaryServerInterceptor(logging.WithLogCompletion(false))`.

## Request ID propagation

Pass the request ID to downstream services so their logs can be correlated
with yours. A header or metadata value that is already set is left untouched.

```go
// HTTP client
client := &http.Client{Transport: logging.NewTransport(http.DefaultTransport)}
req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://users/api", nil)
resp, err := client.Do(req) // carries X-Request-ID from ctx

// gRPC client
conn, err := grpc.NewClient(addr,
	grpc.WithChainUnaryInterceptor(logginggrpc.UnaryClientInterceptor()),
	grpc.WithChainStreamInterceptor(logginggrpc.StreamClientInterceptor()),
)
```

Both accept `WithRequestIDHeader` to use a custom header / metadata key.

## High-throughput services

### Async writer (recommended)

Unbuffered stdout means one write syscall per record under the handler's
mutex; if stdout blocks (a full pipe, a slow container log driver), every
goroutine that logs blocks too, and request latency grows. `AsyncWriter`
decouples logging from output:

```go
w := logging.NewAsyncWriter(os.Stdout, 4<<20, 200*time.Millisecond)
logger := logging.NewLogger(logging.WithWriter(w))

// graceful shutdown: write the remaining records, but do not hang forever
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()
_ = w.CloseContext(ctx)
```

- `Write` only copies the record into memory under a short lock; a background
  goroutine writes batches outside the lock. Request goroutines never wait for
  the destination.
- When pending data reaches the buffer size, new records are **dropped and
  counted** instead of blocking (`ErrBufferFull`, ignored by slog). Logs must
  never slow down or take down the service.
- **Warn and Error records are protected.** The last eighth of the buffer is a
  reserve that only `w.Priority()` may use, and `NewLogger` sends Warn/Error
  records through it automatically. A burst of Info/Debug records is dropped
  first, while errors still get through. Records keep their order. With a
  custom handler, write important records to `w.Priority()` yourself.
- A quarter-full buffer is written immediately; otherwise at least every
  `flushInterval`. Memory: up to 2 × size.
- Export `w.Stats()` (`Accepted`, `Dropped`, `DroppedPriority`, `BytesWritten`,
  `WriteErrors`) to your metrics; alert on `Dropped` and, more urgently, on
  `DroppedPriority` (even the reserve was exhausted).
- Records still in the buffer are lost if the process crashes.

`NewBufferedWriter(w, size, interval)` is still available: it never drops,
but the write syscall happens under its lock, so a stalled destination
blocks the goroutines that log.

### Log storms

The middleware always logs 5xx, panics and slow requests. During an incident
that can mean a record for every request, and the logging itself adds load.
`WithRateLimit` limits records per (level, message) key and interval, like
zap's sampler:

```go
logger := logging.NewLogger(
	logging.WithWriter(w),
	logging.WithRateLimit(logging.RateLimitConfig{
		Tick:       time.Second, // per interval and per (level, message):
		First:      100,         // the first 100 records are written,
		Thereafter: 100,         // then every 100th; 0 drops the rest
	}),
)

dropped, _ := logging.RateLimitStats(logger) // export to metrics
```

Drops are visible in the logs, too: when a key that had records dropped
appears again in a new interval, a summary record with the same level is
written first (without the attributes of a single request):

```json
{"level":"ERROR","msg":"log records suppressed","suppressed_msg":"db timeout","suppressed":9900,"interval":1000000000}
```

The summary is written when the key reappears, so after the storm ends the
last interval is reported only by `RateLimitStats`.

The counters are lock-free, allocation-free and shared by loggers derived
with `With`/`WithGroup`. `logging.NewRateLimitHandler(h, cfg)` wraps any
`slog.Handler`; `WithRateLimit` also applies to a handler passed with
`WithHandler`.

### Other tips

- **Keep `AddSource` off** in production (default).
- **Sample successful requests** with `WithSampling(n)` and skip health checks with `WithSkipPaths`.
- **Use `WithLevelVar`** to enable debug temporarily without a restart.
- Prefer `logger.LogAttrs(ctx, level, msg, attrs...)` on hot paths: it avoids boxing arguments into `[]any`.
- Need a faster encoder? `slog.JSONHandler` is the main cost (see the
  comparison below). Any `slog.Handler` works with `WithHandler` (for example
  a zap-backed one via `go.uber.org/zap/exp/zapslog`); the middleware, rate
  limiting and context helpers stay the same.

## Benchmarks

Go 1.27, Intel Core Ultra 5 225H, `-cpu=1`, output to `io.Discard` unless
noted. Run `make bench`.

| Scenario | Result |
|---|---|
| HTTP middleware, request logged | 2230 ns, 898 B, 7 allocs |
| HTTP middleware, request not logged (sampled out) | 680 ns, 818 B, 6 allocs |
| `L(ctx).LogAttrs` with a context logger | 640 ns, 0 allocs |
| Error storm: record dropped by `WithRateLimit` / no limiter | 310 ns / 650 ns, 0 allocs |
| Destination stalls 20 ms per write, ~100k records/s: max time a log call blocks | `BufferedWriter` 20.6 ms; `AsyncWriter` 0.04 ms, 0 % dropped |

The remaining allocations per request are the request state, the context
value, `r.WithContext`, the request ID string and the response header value.
Parallel benchmarks (`*Parallel`) measure contention on multi-core machines.

### Compared with zap and zerolog

`make compare` (a separate module in `benchmarks/`). Same machine, ns/op on
1 core / 8 cores, allocations per op. Single runs, so treat the numbers as
indicative.

| Scenario | go-logging (slog JSON) | zap | zerolog |
|---|---|---|---|
| Record with 4 fields | 771 / 173 ns, 0 allocs | 655 / 95 ns, 1 alloc | 205 / 50 ns, 0 allocs |
| Record from a logger with 4 request fields | 663 / 191 ns, 0 allocs | 370 / 51 ns, 1 alloc | 177 / 56 ns, 0 allocs |
| Disabled level (Debug at Info) | 43 / 5 ns, 0 allocs | 53 / 35 ns, 1 alloc | 5 / 0.6 ns, 0 allocs |
| New request logger (4 fields) + one record | 1143 / 356 ns, 10 allocs | 769 / 642 ns, 7 allocs | 278 / 233 ns, 1 alloc |

zerolog's encoder is 3–4× faster than `slog.JSONHandler`, zap is in between.
For typical services (thousands to tens of thousands of records per second
per instance) the difference is negligible; for higher volumes plug in a
faster handler with `WithHandler`. The middleware's lazy logger means most
requests never build a request logger at all.

## Development

```bash
make tidy     # go mod tidy in all modules
make test     # tests of the library and the gRPC module (with -race if cgo is available)
make cover    # coverage of the library
make bench    # benchmarks (middleware, context, writers, rate limiting)
make compare  # comparison with zap and zerolog
make fuzz     # fuzz tests, FUZZTIME=5m make fuzz for longer runs
make lint     # golangci-lint (.golangci.yml)
make tags     # tag vX.Y.Z and grpc/vX.Y.Z from ./version
```

The repository has three modules: the library (`.`), the gRPC integration
(`grpc/`, depends on the library through a local `replace` during
development) and the comparison benchmarks (`benchmarks/`, not released).
Before `make tags`, make sure `grpc/go.mod` requires the library version that
is being released. CI (`.github/workflows/ci.yml`) runs lint, tests with
`-race` on the minimum and latest Go, fuzzing and a benchmark smoke run.

## License

See [LICENSE](LICENSE).
