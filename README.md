# go-logging

A thin, context-aware wrapper around Go's `log/slog` with HTTP middleware and
gRPC interceptors: request IDs, trace correlation, completion records,
sampling and panic logging.

Every type is an alias of the `log/slog` type, so a `*logging.Logger` is a
`*slog.Logger` and works with the whole slog ecosystem.

## Installation

```bash
go get github.com/jwm1rr0rb10/go-logging
```

Requires Go 1.21+ (the exact minimum is defined by the gRPC and OpenTelemetry dependencies).

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
- writes **one record per completed request** with `status`, `duration`, `bytes`:
  Error for 5xx and panics, Warn for 4xx and slow requests, Info otherwise;
- keeps `http.Flusher`, `http.Hijacker`, `io.ReaderFrom` and `Unwrap()` working,
  so SSE, WebSockets, `http.ResponseController` and sendfile are not broken;
- logs panics with a stack trace and re-raises them (or answers 500 with
  `WithRecoverPanics(true)`).

| Option | Default | Description |
|---|---|---|
| `WithMiddlewareLogger(l)` | ctx logger / slog default | Base logger. |
| `WithSampling(n)` | `1` | Log every n-th successful request; errors, panics and slow requests are always logged. |
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

## High-throughput services

- **Keep `AddSource` off** in production (default).
- **Buffer the output.** Unbuffered stdout means one write syscall per record under
  the handler's mutex:

  ```go
  w := logging.NewBufferedWriter(os.Stdout, 256<<10, time.Second)
  defer w.Close() // flushes remaining records; call it on graceful shutdown

  logger := logging.NewLogger(logging.WithWriter(w))
  ```

  Records still in the buffer are lost if the process crashes.
- **Sample successful requests** with `WithSampling(n)` and skip health checks with `WithSkipPaths`.
- **Use `WithLevelVar`** to enable debug temporarily without a restart.
- Prefer `logger.LogAttrs(ctx, level, msg, attrs...)` on hot paths: it avoids boxing arguments into `[]any`.

## Development

```bash
make tidy   # go mod tidy
make test   # tests with the race detector
make bench  # benchmarks
make lint   # golangci-lint
```

## License

See [LICENSE](LICENSE).
