# go-logging

Тонкая обёртка над стандартным `log/slog` с поддержкой контекста, HTTP-middleware
и gRPC-интерцепторами: request ID, корреляция с трейсами, итоговая запись по
каждому запросу, сэмплирование и логирование паник. Рассчитана на
высоконагруженные сервисы: неблокирующий асинхронный writer с политикой
сброса, ограничение «штормов» логов, ленивое построение логгера запроса
(6 аллокаций на запрос, который ничего не логирует) и проброс request ID
в исходящие HTTP- и gRPC-вызовы.

Все типы — алиасы типов `log/slog`, поэтому `*logging.Logger` — это `*slog.Logger`,
и он работает со всей экосистемой slog.

## Установка

```bash
go get github.com/jwm1rr0rb10/go-logging
```

Нужен Go 1.25+ (см. `go.mod`).

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
| `WithRateLimit(cfg)` | — | Ограничение числа записей на пару (уровень, сообщение) за интервал, см. [Штормы логов](#штормы-логов). |

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

Логгеры в контексте строятся **лениво**. `ContextWithAttrs` и middleware только
запоминают атрибуты. Сам `*slog.Logger` (а это клонирование хендлера и
пред-сериализация атрибутов) создаётся при первом вызове `L(ctx)` и
кешируется. Запросы, которые ничего не логируют, за это не платят. `L(ctx)`
безопасен для конкурентного использования.

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
- делает всё это лениво: если запрос ничего не логирует (например, отсеян
  сэмплированием), логгер не создаётся, а итоговая запись передаёт атрибуты
  запроса вместе с записью, без клонирования хендлера;
- пишет **одну запись на завершённый запрос** с `status`, `duration`, `bytes`:
  Error для 5xx и паник, Warn для 4xx и медленных запросов, Info для остальных;
- сохраняет `http.Flusher`, `http.Hijacker`, `io.ReaderFrom` и `Unwrap()`, поэтому
  SSE, WebSocket, `http.ResponseController` и sendfile продолжают работать;
- логирует паники со стектрейсом и пробрасывает их дальше (или отвечает 500
  при `WithRecoverPanics(true)`).

| Опция | По умолчанию | Описание |
|---|---|---|
| `WithMiddlewareLogger(l)` | логгер из ctx / slog default | Базовый логгер. |
| `WithSampling(n)` | `1` | Логировать ровно каждый n-й успешный запрос (детерминированно); ошибки, паники и медленные запросы логируются всегда. |
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

## Проброс request ID

Передавайте request ID в нижестоящие сервисы, чтобы их логи связывались с
вашими. Уже заданный заголовок или значение metadata не перезаписывается.

```go
// HTTP-клиент
client := &http.Client{Transport: logging.NewTransport(http.DefaultTransport)}
req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://users/api", nil)
resp, err := client.Do(req) // содержит X-Request-ID из ctx

// gRPC-клиент
conn, err := grpc.NewClient(addr,
	grpc.WithChainUnaryInterceptor(logging.UnaryClientInterceptor()),
	grpc.WithChainStreamInterceptor(logging.StreamClientInterceptor()),
)
```

Оба принимают `WithRequestIDHeader` для своего имени заголовка или ключа metadata.

## Высоконагруженные сервисы

### Асинхронный writer (рекомендуется)

Небуферизованный stdout — это один системный вызов на каждую запись под
мьютексом хендлера. Если stdout заблокировался (переполнен pipe, медленный
log driver контейнера), блокируются все горутины, которые пишут логи, и
растёт latency запросов. `AsyncWriter` развязывает логирование и вывод:

```go
w := logging.NewAsyncWriter(os.Stdout, 4<<20, 200*time.Millisecond)
logger := logging.NewLogger(logging.WithWriter(w))

// graceful shutdown: дописать оставшееся, но не зависнуть навсегда
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()
_ = w.CloseContext(ctx)
```

- `Write` только копирует запись в память под коротким локом; фоновая
  горутина пишет пачки вне лока. Горутины запросов никогда не ждут вывод.
- Когда объём ожидающих данных достигает размера буфера, новые записи
  **сбрасываются и учитываются** вместо блокировки (`ErrBufferFull`, slog его
  игнорирует). Логи не должны тормозить или ронять сервис.
- Буфер, заполненный на четверть, пишется сразу; иначе не реже, чем раз в
  `flushInterval`. Память: до 2 × size.
- Выгружайте `w.Stats()` (`Accepted`, `Dropped`, `BytesWritten`, `WriteErrors`)
  в метрики и ставьте алерт на `Dropped`.
- Записи, оставшиеся в буфере, теряются при падении процесса.

`NewBufferedWriter(w, size, interval)` по-прежнему доступен: он никогда не
теряет записи, но системный вызов выполняется под его локом, поэтому
зависший вывод блокирует горутины, которые логируют.

### Штормы логов

Middleware всегда логирует 5xx, паники и медленные запросы. Во время
инцидента это может означать запись на каждый запрос, и само логирование
добавляет нагрузку. `WithRateLimit` ограничивает число записей на ключ
(уровень, сообщение) за интервал, по аналогии с сэмплером zap:

```go
logger := logging.NewLogger(
	logging.WithWriter(w),
	logging.WithRateLimit(logging.RateLimitConfig{
		Tick:       time.Second, // за интервал и на пару (уровень, сообщение):
		First:      100,         // первые 100 записей пишутся,
		Thereafter: 100,         // затем каждая 100-я; 0 — остальные сбрасываются
	}),
)

dropped, _ := logging.RateLimitStats(logger) // выгружайте в метрики
```

Счётчики lock-free, без аллокаций и общие для логгеров, полученных через
`With`/`WithGroup`. `logging.NewRateLimitHandler(h, cfg)` оборачивает любой
`slog.Handler`; `WithRateLimit` применяется и к хендлеру из `WithHandler`.

### Прочие советы

- **Не включайте `AddSource`** в проде (выключен по умолчанию).
- **Сэмплируйте успешные запросы** через `WithSampling(n)` и исключайте health-check'и через `WithSkipPaths`.
- **Используйте `WithLevelVar`**, чтобы временно включить debug без рестарта.
- На горячих путях предпочитайте `logger.LogAttrs(ctx, level, msg, attrs...)`: он не упаковывает аргументы в `[]any`.
- Нужен ещё более быстрый энкодер? С `WithHandler` работает любой
  `slog.Handler` (например, на базе zap или zerolog); middleware, rate limiting
  и хелперы контекста остаются прежними.

### Бенчмарки

`make bench`. Go 1.26, Intel Xeon 2.1 GHz, **1 vCPU**, вывод в `io.Discard`,
если не указано иное. v1.1.0 — предыдущая версия.

| Сценарий | v1.1.0 | v1.2.0 |
|---|---|---|
| HTTP middleware, запрос залогирован | 2270 ns, 1448 B, 25 allocs | 1970 ns, 834 B, 7 allocs |
| HTTP middleware, запрос не логируется (отсеян сэмплированием) | 1880 ns, 1448 B, 25 allocs | 620 ns, 754 B, 6 allocs |
| `L(ctx).LogAttrs` с логгером из контекста | 500 ns, 0 allocs | 500 ns, 0 allocs |
| Шторм ошибок, запись сброшена `WithRateLimit` | лимитера нет, пишется каждая запись (465 ns) | 230 ns, 0 allocs |
| Вывод зависает на 20 ms на каждую запись, ~100k записей/с: максимальная блокировка вызова лога | 20–28 ms (`BufferedWriter`) | 0.3–2.5 ms, 0 % потерь (`AsyncWriter`) |

Оставшиеся аллокации на запрос: scope запроса, значение контекста,
`r.WithContext`, строка request ID и значение заголовка ответа. Параллельные
бенчмарки (`*Parallel`) включены; запускайте их на многоядерной машине,
чтобы измерить конкуренцию.

## Разработка

```bash
make tidy   # go mod tidy
make test   # тесты с race detector
make bench  # бенчмарки (middleware, контекст, writer'ы, rate limiting)
make lint   # golangci-lint
```

## Лицензия

См. [LICENSE](LICENSE).
