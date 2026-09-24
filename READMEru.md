# go-logging

Тонкая обёртка над стандартным `log/slog` с поддержкой контекста, HTTP-middleware
и gRPC-интерцепторами: request ID, корреляция с трейсами, итоговая запись по
каждому запросу, сэмплирование и логирование паник.

Все типы — алиасы типов `log/slog`, поэтому `*logging.Logger` — это `*slog.Logger`,
и он работает со всей экосистемой slog.

## Установка

```bash
go get github.com/jwm1rr0rb10/go-logging
```

Нужен Go 1.21+ (точный минимум определяется зависимостями gRPC и OpenTelemetry).

## Быстрый старт

```go
package main

import (
	"net/http"

	logging "github.com/jwm1rr0rb10/go-logging"
)

func main() {
	logger := logging.NewLogger() // JSON в stdout, уровень из LOG_LEVEL, становится slog default

	mux := http.NewServeMux()
	mux.HandleFunc("/users", func(w http.ResponseWriter, r *http.Request) {
		// Возвращённый контекст нужно использовать, иначе атрибуты потеряются.
		ctx := logging.ContextWithAttrs(r.Context(), logging.StringAttr("user_id", r.URL.Query().Get("id")))

		logging.L(ctx).Info("loading user") // содержит request_id, method, endpoint, user_id...
		w.WriteHeader(http.StatusOK)
	})

	handler := logging.NewMiddleware(logging.WithMiddlewareLogger(logger))(mux)
	_ = http.ListenAndServe(":8080", handler)
}
```

## Настройка логгера

| Опция | По умолчанию | Описание |
|---|---|---|
| `WithLevel("debug")` | env / info | Уровень; приоритетнее переменных окружения. Невалидное значение выводится в stderr и игнорируется. |
| `WithLevelVar(lv)` | — | `*slog.LevelVar` для смены уровня на лету через `lv.Set(...)`. |
| `WithDevMode(true)` | `false` | Текстовый вывод, уровень debug, file:line (если не заданы явно). |
| `WithIsJSON(bool)` | `true` | JSON или текстовый вывод. |
| `WithAddSource(bool)` | `false` | Добавлять `file:line`. Дорого на горячих путях. |
| `WithSetDefault(bool)` | `true` | Вызвать `slog.SetDefault`. |
| `WithWriter(w)` | `os.Stdout` | Куда писать. |
| `WithReplaceAttr(fn)` | — | Хук `ReplaceAttr` из slog, например для маскирования секретов. |
| `WithHandler(h)` | — | Свой `slog.Handler` (опции вывода и формата игнорируются). |

**Приоритет уровня:** `WithLevel` → `LOG_LEVEL` / `SLOG_LEVEL` → debug в dev-режиме → info.
Допустимые значения: `debug`, `info`, `warn`/`warning`, `error` (без учёта регистра).

### Смена уровня на лету

```go
lv := new(slog.LevelVar)
logger := logging.NewLogger(logging.WithLevelVar(lv))

lv.Set(logging.LevelDebug) // например, из админ-эндпоинта или при перечитывании конфига
```

### Маскирование секретов

```go
logger := logging.NewLogger(logging.WithReplaceAttr(func(_ []string, a logging.Attr) logging.Attr {
	switch a.Key {
	case "password", "token", "authorization":
		return logging.StringAttr(a.Key, "***")
	}
	return a
}))
```

## Работа с контекстом

```go
l := logging.L(ctx)                                  // логгер из ctx или slog default
ctx = logging.ContextWithLogger(ctx, l)              // положить логгер в ctx
ctx = logging.ContextWithAttrs(ctx, attrs...)        // обогатить логгер в ctx (используйте результат!)
l = logging.WithAttrs(ctx, attrs...)                 // обогащённый логгер без изменения ctx
id := logging.RequestIDFromContext(ctx)              // request ID из middleware/интерцепторов
```

Хелперы атрибутов: `StringAttr`, `BoolAttr`, `IntAttr`, `Int32Attr`, `Int64Attr`,
`Uint64Attr`, `UInt32Attr`, `Float32Attr`, `Float64Attr`, `DurationAttr`,
`TimeAttr`, `AnyAttr`, `ErrAttr`, `Group`, `GroupValue`.

## HTTP middleware

```go
handler := logging.NewMiddleware(
	logging.WithMiddlewareLogger(logger),
	logging.WithSampling(100),                    // логировать 1 из 100 успешных запросов
	logging.WithSlowThreshold(500*time.Millisecond),
	logging.WithSkipPaths("/healthz", "/metrics"),
)(mux)
```

`logging.Middleware(next)` — то же самое с опциями по умолчанию.

Что делает middleware:
- читает `X-Request-ID` (с проверкой: до 128 символов, `[A-Za-z0-9-_.:/+=]`) или
  генерирует новый, кладёт его в контекст и в заголовок ответа;
- кладёт в контекст логгер запроса с полями `request_id`, `method`, `endpoint`
  (только путь), `remote_addr`, а также `trace_id` / `span_id`, если есть
  спан OpenTelemetry;
- пишет **одну запись на завершённый запрос** с `status`, `duration`, `bytes`:
  Error для 5xx и паник, Warn для 4xx и медленных запросов, Info для остальных;
- сохраняет `http.Flusher`, `http.Hijacker`, `io.ReaderFrom` и `Unwrap()`, поэтому
  SSE, WebSocket, `http.ResponseController` и sendfile продолжают работать;
- логирует паники со стектрейсом и пробрасывает их дальше (или отвечает 500
  при `WithRecoverPanics(true)`).

| Опция | По умолчанию | Описание |
|---|---|---|
| `WithMiddlewareLogger(l)` | логгер из ctx / slog default | Базовый логгер. |
| `WithSampling(n)` | `1` | Логировать каждый n-й успешный запрос; ошибки, паники и медленные запросы логируются всегда. |
| `WithSlowThreshold(d)` | выкл. | Медленные запросы — на уровне Warn с `"slow": true`. |
| `WithSkipPaths(p...)` | — | Без итоговой записи для этих путей (контекст всё равно обогащается). |
| `WithLogQuery(bool)` | `false` | Включать query string в `endpoint`. |
| `WithRecoverPanics(bool)` | `false` | Перехватывать паники и отвечать 500 вместо проброса. |
| `WithRequestIDHeader(h)` | `X-Request-ID` | Имя заголовка (для gRPC — ключ metadata). |
| `WithTrustRequestID(bool)` | `true` | Использовать входящий ID или всегда генерировать новый. |
| `WithMaxRequestIDLength(n)` | `128` | Максимальная длина входящего ID. |
| `WithLogCompletion(bool)` | `true` | Отключить итоговые записи, оставив обогащение контекста. |

Ставьте middleware **после** `otelhttp`, чтобы были доступны trace ID:

```go
handler := otelhttp.NewHandler(logging.NewMiddleware()(mux), "server")
```

Пример записи:

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

Интерцепторы принимают те же опции, что и HTTP-middleware
(`WithSkipPaths` принимает полные имена методов, например `/grpc.health.v1.Health/Check`).
Каждый вызов даёт запись `rpc completed` с полями `grpc_method`, `grpc_code` и `duration`.
Серверные коды (`Internal`, `Unknown`, `DataLoss`, `Unavailable`,
`DeadlineExceeded`, `Unimplemented`) логируются как Error, остальные не-OK коды — как Warn.

`WithTraceIDInLogger()` устарел: он только обогащает логгер в контексте.

## Высоконагруженные сервисы

- **Не включайте `AddSource`** в проде (выключен по умолчанию).
- **Буферизуйте вывод.** Небуферизованный stdout — это один системный вызов на
  каждую запись под мьютексом хендлера:

  ```go
  w := logging.NewBufferedWriter(os.Stdout, 256<<10, time.Second)
  defer w.Close() // дописывает оставшиеся записи; вызывайте при graceful shutdown

  logger := logging.NewLogger(logging.WithWriter(w))
  ```

  Записи, оставшиеся в буфере, теряются при падении процесса.
- **Сэмплируйте успешные запросы** через `WithSampling(n)` и исключайте health-check'и через `WithSkipPaths`.
- **Используйте `WithLevelVar`**, чтобы временно включить debug без рестарта.
- На горячих путях предпочитайте `logger.LogAttrs(ctx, level, msg, attrs...)`: он не упаковывает аргументы в `[]any`.

## Разработка

```bash
make tidy   # go mod tidy
make test   # тесты с race detector
make bench  # бенчмарки
make lint   # golangci-lint
```

## Лицензия

См. [LICENSE](LICENSE).
