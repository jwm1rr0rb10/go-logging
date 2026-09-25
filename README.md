# go-logging

A thin, context-aware wrapper around Go's `log/slog` with HTTP middleware and
gRPC interceptors: request IDs, trace correlation, completion records,
sampling and panic logging. Built for high-throughput services: a
non-blocking async writer with a drop policy, log-storm rate limiting,
lazily built request loggers (6 allocations per request that does not log)
and request ID propagation to downstream HTTP/gRPC calls.

Every type is an alias of the `log/slog` type, so a `*logging.Logger` is a
`*slog.Logger` and works with the whole slog ecosystem.

## Installation

```bash
go get github.com/jwm1rr0rb10/go-logging
```

Requires Go 1.25+ (see `go.mod`).

## Quick start

```go
package main

import (
	"net/http"

	logging "github.com/jwm1rr0rb10/go-logging"
)

func main() {
	logger := logging.NewLogger() // JSON to stdout, level from LOG_LEVEL, becomes slog default

	mux := http.NewServeMux()
	mux.HandleFunc("/users", func(w http.ResponseWriter, r *http.Request) {
		// The returned context must be used, otherwise the attributes are lost.
		ctx := logging.ContextWithAttrs(r.Context(), logging.StringAttr("user_id", r.URL.Query().Get("id")))

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
| `WithLevel("debug")` | env / info | Level; wins over environment variables. Invalid values are reported and ignored. |
| `WithLevelVar(lv)` | — | Use a `*slog.LevelVar` to change the level at runtime with `lv.Set(...)`. |
| `WithDevMode(true)` | `false` | Text output, debug level, source locations (unless set explicitly). |
| `WithIsJSON(bool)` | `true` | JSON or text output. |
| `WithAddSource(bool)` | `false` | Add `file:line`. Costly on hot paths. |
| `WithSetDefault(bool)` | `true` | Call `slog.SetDefault`. |
| `WithWriter(w)` | `os.Stdout` | Output destination. |
| `WithReplaceAttr(fn)` | — | slog `ReplaceAttr` hook, e.g. to redact secrets. |
| `WithHandler(h)` | — | Use a custom `slog.Handler` (writer/format options are ignored). |
| `WithRateLimit(cfg)` | — | Limit records per (level, message) and interval, see [Log storms](#log-storms). |

**Level priority:** `WithLevel` → `LOG_LEVEL` / `SLOG_LEVEL` → debug in dev mode → info.
Accepted values: `debug`, `info`, `warn`/`warning`, `error` (case-insensitive).

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
- writes **one record per completed request** with `status`, `duration`, `bytes`:
  Error for 5xx and panics, Warn for 4xx and slow requests, Info otherwise;
- keeps `http.Flusher`, `http.Hijacker`, `io.ReaderFrom` and `Unwrap()` working,
  so SSE, WebSockets, `http.ResponseController` and sendfile are not broken;
- logs panics with a stack trace and re-raises them (or answers 500 with
  `WithRecoverPanics(true)`).

| Option | Default | Description |
|---|---|---|
| `WithMiddlewareLogger(l)` | ctx logger / slog default | Base logger. |
| `WithSampling(n)` | `1` | Log exactly every n-th successful request (deterministic); errors, panics and slow requests are always logged. |
| `WithSlowThreshold(d)` | off | Log slower requests at Warn with `"slow": true`. |
| `WithSkipPaths(p...)` | — | No completion record for these paths (context is still enriched). |
| `WithLogQuery(bool)` | `false` | Include the query string in `endpoint`. |
| `WithRecoverPanics(bool)` | `false` | Recover panics and answer 500 instead of re-raising. |
| `WithRequestIDHeader(h)` | `X-Request-ID` | Header name (gRPC: metadata key). |
| `WithTrustRequestID(bool)` | `true` | Reuse incoming IDs or always generate new ones. |
| `WithMaxRequestIDLength(n)` | `128` | Max accepted incoming ID length. |
| `WithLogCompletion(bool)` | `true` | Disable completion records, keep context enrichment. |

Place the middleware **after** `otelhttp` so trace IDs are available:

```go
handler := otelhttp.NewHandler(logging.NewMiddleware()(mux), "server")
```

Example record:

```json
{"time":"2026-09-24T10:00:00Z","level":"INFO","msg":"request completed","request_id":"9f86d081884c7d65","method":"GET","endpoint":"/users","remote_addr":"10.0.0.7:51234","trace_id":"4bf92f3577b34da6a3ce929d0e0e4736","span_id":"00f067aa0ba902b7","status":200,"duration":1843200,"bytes":512}
```

## gRPC

```go
srv := grpc.NewServer(
	grpc.StatsHandler(otelgrpc.NewServerHandler()),
	grpc.ChainUnaryInterceptor(logging.UnaryServerInterceptor(logging.WithMiddlewareLogger(logger))),
	grpc.ChainStreamInterceptor(logging.StreamServerInterceptor(logging.WithMiddlewareLogger(logger))),
)
```

The interceptors accept the same options as the HTTP middleware
(`WithSkipPaths` takes full method names like `/grpc.health.v1.Health/Check`).
Each call produces an `rpc completed` record with `grpc_method`, `grpc_code` and `duration`.
Server-side codes (`Internal`, `Unknown`, `DataLoss`, `Unavailable`,
`DeadlineExceeded`, `Unimplemented`) are logged at Error, other non-OK codes at Warn.

`WithTraceIDInLogger()` is deprecated; it only enriches the context logger.

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
	grpc.WithChainUnaryInterceptor(logging.UnaryClientInterceptor()),
	grpc.WithChainStreamInterceptor(logging.StreamClientInterceptor()),
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
- A quarter-full buffer is written immediately; otherwise at least every
  `flushInterval`. Memory: up to 2 × size.
- Export `w.Stats()` (`Accepted`, `Dropped`, `BytesWritten`, `WriteErrors`) to
  your metrics and alert on `Dropped`.
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

The counters are lock-free, allocation-free and shared by loggers derived
with `With`/`WithGroup`. `logging.NewRateLimitHandler(h, cfg)` wraps any
`slog.Handler`; `WithRateLimit` also applies to a handler passed with
`WithHandler`.

### Other tips

- **Keep `AddSource` off** in production (default).
- **Sample successful requests** with `WithSampling(n)` and skip health checks with `WithSkipPaths`.
- **Use `WithLevelVar`** to enable debug temporarily without a restart.
- Prefer `logger.LogAttrs(ctx, level, msg, attrs...)` on hot paths: it avoids boxing arguments into `[]any`.
- Need an even faster encoder? Any `slog.Handler` works with `WithHandler`
  (for example a zap- or zerolog-backed one); the middleware, rate limiting and
  context helpers stay the same.

### Benchmarks

`make bench`. Go 1.26, Intel Xeon 2.1 GHz, **1 vCPU**, output to `io.Discard`
unless noted. v1.1.0 is the previous version.

| Scenario | v1.1.0 | v1.2.0 |
|---|---|---|
| HTTP middleware, request logged | 2270 ns, 1448 B, 25 allocs | 1970 ns, 834 B, 7 allocs |
| HTTP middleware, request not logged (sampled out) | 1880 ns, 1448 B, 25 allocs | 620 ns, 754 B, 6 allocs |
| `L(ctx).LogAttrs` with a context logger | 500 ns, 0 allocs | 500 ns, 0 allocs |
| Error storm, record dropped by `WithRateLimit` | no limiter, every record written (465 ns) | 230 ns, 0 allocs |
| Destination stalls 20 ms per write, ~100k records/s: max time a log call blocks | 20–28 ms (`BufferedWriter`) | 0.3–2.5 ms, 0 % dropped (`AsyncWriter`) |

The remaining allocations per request are the request scope, the context
value, `r.WithContext`, the request ID string and the response header value.
Parallel benchmarks (`*Parallel`) are included; run them on a multi-core
machine to measure contention.

## Development

```bash
make tidy   # go mod tidy
make test   # tests with the race detector
make bench  # benchmarks (middleware, context, writers, rate limiting)
make lint   # golangci-lint
```

## License

See [LICENSE](LICENSE).
