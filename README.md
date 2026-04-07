# logging

**A lightweight, production-ready, context-aware wrapper around Go's `log/slog` with built-in observability, request/response logging, and excellent developer experience.**

This library provides a clean, opinionated, and highly configurable logging solution that works seamlessly with OpenTelemetry, HTTP servers, and gRPC services. It eliminates boilerplate while giving you structured, traceable, and beautiful logs out of the box.

---

## ✨ Features

- **Zero-magic `slog` wrapper** – full compatibility with standard `log/slog`
- **Context-aware logging** – request-scoped loggers via `L(ctx)` and `ContextWithAttrs`
- **Environment-driven configuration** – `LOG_LEVEL` or `SLOG_LEVEL` read automatically
- **DevMode** – beautiful colored text output + automatic debug level for local development
- **Full HTTP request/response middleware**:
    - Automatic `X-Request-ID` generation and propagation
    - Logs method, endpoint, remote address, trace/span IDs
    - Captures status code, duration, and bytes written
    - Smart log level: `Error` for 5xx, `Warn` for 4xx, `Info` otherwise
- **gRPC unary interceptor** with trace/span correlation
- **Rich attribute helpers** (`ErrAttr`, `TimeAttr`, `UInt32Attr`, etc.)
- **Functional options** for clean configuration
- **Custom writer support** (files, buffers, multi-writers, etc.)
- **No external dependencies** beyond `go.opentelemetry.io/otel` (already used in most modern backends)
- **Fully tested** and production-ready

---

## 📦 Installation

```bash
go get -u github.com/jwm1rr0rb10/go-logging
```

---

## 🚀 Quick Start

```go
package main

import (
	"context"
	"net/http"
	"os"

	"github.com/jwm1rr0rb10/go-logging"
)

func main() {
	// Create logger (reads LOG_LEVEL automatically)
	logger := logging.NewLogger(
		logging.WithDevMode(true), // beautiful text output for local dev
	)

	// Make it the default
	logging.SetDefault(logger)

	// Example handler
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		// Context-aware logging with attributes
		logging.ContextWithAttrs(ctx,
			logging.StringAttr("user_id", "12345"),
			logging.ErrAttr(nil),
		)

		logging.L(ctx).Info("health check successful")
		w.WriteHeader(http.StatusOK)
	})

	// Wrap with powerful middleware
	http.ListenAndServe(":8080", logging.Middleware(http.DefaultServeMux))
}
```

---

## ⚙️ Configuration
### `NewLogger` Options


| Option                                                      | Type       | Default   | Description                                                                  |
|:------------------------------------------------------------|:-----------|:----------|:-----------------------------------------------------------------------------|
| `WithLevel(level)`                                          | string     | from env  | "Log level (debug, info, warn, error)"                                       |
| `WithDevMode(true)`                                         | bool       | false     | Text output + debug level + source (ideal for local)                         |
| `WithIsJSON(true)`                                          | bool       | true      | JSON vs human-readable text                                                  |
| `WithAddSource(true)`                                       | bool       | true      | Include file:line in logs                                                    |
| `WithSetDefault(true)`                                      | bool       | true      | Call slog.SetDefault                                                         |
| `WithWriter(w)`                                             | io.Writer  | os.Stdout | Custom output destination                                                    |

### Environment Variables

`LOG_LEVEL` or `SLOG_LEVEL` – overrides default level (e.g. `debug`, `info`)

---

## 📝 Core API
### Logger Retrieval

```go
l := logging.L(ctx)           // context-aware logger
l = logging.Default()         // global default logger
```

### Context Helpers
```go
// Add attributes and return new logger
l := logging.WithAttrs(ctx, logging.StringAttr("key", "value"))

// Most convenient: add attrs + update context in one call
ctx = logging.ContextWithAttrs(ctx,
    logging.StringAttr("user_id", "123"),
    logging.ErrAttr(err),
)
```

### Attribute Helpers (from `alias.go`)

```go
logging.StringAttr, BoolAttr, IntAttr, Int64Attr, Uint64Attr,
Float64Attr, Float32Attr, DurationAttr, TimeAttr, AnyAttr,
ErrAttr(err), Group, GroupValue...
```

---

## 🌐 HTTP Middleware

`logging.Middleware` is the star of the library.
**What it does automatically:**

- Generates or reads `X-Request-ID`
- Adds to response header
- Enriches logger with: `request_id`, `method`, `endpoint`, `remote_addr`, `trace_id`, `span_id`
- Logs incoming request context
- Captures status, duration, and response size
- Logs completed request with smart level

**Usage:**
```go
mux := http.NewServeMux()
// ... register handlers
http.ListenAndServe(":8080", logging.Middleware(mux))
```

**Example log output (DevMode):**

```text
INFO  2025-04-05T12:34:56.123Z method=GET endpoint=/api/users request_id=7f8e9d... remote_addr=127.0.0.1 trace_id=... span_id=...
INFO  2025-04-05T12:34:56.189Z request completed request_id=7f8e9d... status=200 duration=66ms bytes=1243
```

---

## 🔌 gRPC Support
```go
import "google.golang.org/grpc"

server := grpc.NewServer(
    grpc.UnaryInterceptor(logging.WithTraceIDInLogger()),
)
```

The interceptor automatically adds `method`, `trace_id`, and `span_id` to the logger for every RPC call.

---

## 🧪 Testing
The library includes comprehensive tests:

```bash
go test ./... -v
```

See `logger_test.go` for examples of testing both `NewLogger` and `Middleware`.

---

## 🛠️ Best Practices

1. **Always use context-aware logging** – `logging.L(ctx)` or `ContextWithAttrs`
2. **Use** `ContextWithAttrs` early in your handlers/middlewares
3. **Enable** `WithDevMode(true)` in development / local environments
4. **Keep** `WithAddSource(true)` in production (great for debugging)
5. **Use structured attributes** instead of `fmt.Sprintf`
6. **Let middleware handle request IDs and tracing** – don’t do it manually

---

## 🤝 Contributing
Contributions are welcome! Feel free to open issues or PRs for:

- Additional middleware features
- Log sampling
- Request/response body logging (with size limits)
- Colored output improvements
- More examples

---

## 📄 License
[MIT License](https://github.com/jwm1rr0rb10/libraries/blob/main/backend/golang/LICENSE) – © Raman Zaitsau [@jwm1rrr0rb10](https://github.com/jwm1rr0rb10)

Made with ❤️ for Go teams who want clean, observable, and delightful logs.