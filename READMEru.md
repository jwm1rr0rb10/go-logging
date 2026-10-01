# go-logging

[English version](README.md)

Тонкая обёртка над стандартным `log/slog` с поддержкой контекста, HTTP-middleware
и gRPC-интерцепторами: request ID, корреляция с трейсами, итоговая запись по
каждому запросу, сэмплирование и логирование паник. Рассчитана на
высоконагруженные сервисы: неблокирующий асинхронный writer, который при
переполнении отбрасывает записи и держит резерв для Warn/Error, ограничение
«штормов» логов со сводными записями, согласованное по trace ID
сэмплирование между сервисами, ленивое построение логгера запроса
(6 аллокаций на запрос, который ничего не логирует) и проброс request ID
в исходящие HTTP- и gRPC-вызовы.

Все типы — алиасы типов `log/slog`, поэтому `*logging.Logger` — это `*slog.Logger`,
и он работает со всей экосистемой slog.

## Установка

```bash
go get github.com/jwm1rr0rb10/go-logging/v2

# gRPC-интерцепторы (отдельный модуль, чтобы HTTP-сервисы не зависели от gRPC)
go get github.com/jwm1rr0rb10/go-logging/grpc/v2
```

Нужен Go 1.25+ (см. `go.mod`). Единственная зависимость основного модуля —
`go.opentelemetry.io/otel/trace`.

## Переход с v1

| v1 | v2 |
|---|---|
| `import "github.com/jwm1rr0rb10/go-logging"` | `import logging "github.com/jwm1rr0rb10/go-logging/v2"` |
| `NewLogger()` вызывал `slog.SetDefault` | По умолчанию больше не вызывает. Добавьте `logging.WithSetDefault(true)` в `main`, если код с `slog.Info(...)` или `L(ctx)` без логгера в контексте должен использовать этот логгер. |
| `logging.UnaryServerInterceptor`, `StreamServerInterceptor`, `UnaryClientInterceptor`, `StreamClientInterceptor` | Те же имена в `logginggrpc "github.com/jwm1rr0rb10/go-logging/grpc/v2"`; опции по-прежнему `logging.With...`. |
| `logging.WithTraceIDInLogger()` (устаревший) | Удалён: `logginggrpc.UnaryServerInterceptor(logging.WithLogCompletion(false))`. |
| Итоговая запись: `status`, `duration`, `bytes` | Добавлено поле `route` (шаблон `http.ServeMux`), `duration` идёт последним: `route`, `status`, `bytes`, `duration`. |
| `WithSampling(n)`: каждый n-й запрос | Запросы с trace ID сэмплируются по trace ID (согласованно между сервисами); `WithTraceSampling(false)` возвращает счётчик. |

## Быстрый старт

```go
package main

import (
	"net/http"

	logging "github.com/jwm1rr0rb10/go-logging/v2"
)

func main() {
	// JSON в stdout, уровень из LOG_LEVEL; также slog default для кода, который вызывает slog.Info.
	logger := logging.NewLogger(logging.WithSetDefault(true))

	mux := http.NewServeMux()
	mux.HandleFunc("GET /users/{id}", func(w http.ResponseWriter, r *http.Request) {
		// Возвращённый контекст нужно использовать, иначе атрибуты потеряются.
		ctx := logging.ContextWithAttrs(r.Context(), logging.StringAttr("user_id", r.PathValue("id")))

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
| `WithDevMode(true)` | `false` | Текстовый вывод, уровень debug, `file:line` (если не заданы явно). |
| `WithIsJSON(bool)` | `true` | JSON или текстовый вывод. |
| `WithAddSource(bool)` | `false` | Добавлять `file:line`. Дорого на горячих путях. |
| `WithSetDefault(bool)` | `false` | Вызвать `slog.SetDefault`. Библиотека не должна неявно менять глобальное состояние, поэтому включайте в `main`. |
| `WithWriter(w)` | `os.Stdout` | Куда писать. С `*AsyncWriter` записи Warn/Error используют его резерв, см. [Асинхронный writer](#асинхронный-writer-рекомендуется). |
| `WithReplaceAttr(fn)` | — | Хук `ReplaceAttr` из slog, например для маскирования секретов. |
| `WithHandler(h)` | — | Свой `slog.Handler` (опции вывода и формата игнорируются). |
| `WithRateLimit(cfg)` | — | Ограничение числа записей на пару (уровень, сообщение) за интервал, см. [Штормы логов](#штормы-логов). |

**Приоритет уровня:** `WithLevel` → `LOG_LEVEL` / `SLOG_LEVEL` → debug в dev-режиме → info.
Допустимые значения: `debug`, `info`, `warn`/`warning`, `error` (без учёта регистра),
можно со смещением, например `info+2`.

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
ctx = logging.ContextWithRequestID(ctx, id)          // задать request ID (логгер не меняется)
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
- пишет **одну запись на завершённый запрос** с `route`, `status`, `bytes` и
  `duration`: Error для 5xx и паник, Warn для 4xx и медленных запросов, Info
  для остальных. `route` — шаблон `http.ServeMux` (`GET /users/{id}`), по нему
  удобно агрегировать, в отличие от `endpoint` (`/users/42`); поле опускается,
  если шаблон не совпал или используется другой роутер;
- сохраняет `http.Flusher`, `http.Hijacker`, `io.ReaderFrom` и `Unwrap()`, поэтому
  SSE, WebSocket, `http.ResponseController` и sendfile продолжают работать;
- логирует паники со стектрейсом и пробрасывает их дальше (или отвечает 500
  при `WithRecoverPanics(true)`); `http.ErrAbortHandler` пробрасывается без
  записи.

| Опция | По умолчанию | Описание |
|---|---|---|
| `WithMiddlewareLogger(l)` | логгер из ctx / slog default | Базовый логгер. |
| `WithSampling(n)` | `1` | Логировать 1 из n успешных запросов; ошибки, паники и медленные запросы логируются всегда. |
| `WithTraceSampling(bool)` | `true` | Запросы с trace ID сэмплировать по trace ID (см. ниже); иначе ровно каждый n-й запрос. |
| `WithSlowThreshold(d)` | выкл. | Медленные запросы — на уровне Warn с `"slow": true`. |
| `WithSkipPaths(p...)` | — | Без итоговой записи для этих путей, кроме паник (контекст всё равно обогащается). |
| `WithLogQuery(bool)` | `false` | Включать query string в `endpoint`. Выключено: в query string часто бывают токены и персональные данные. |
| `WithRecoverPanics(bool)` | `false` | Перехватывать паники и отвечать 500 вместо проброса. |
| `WithRequestIDHeader(h)` | `X-Request-ID` | Имя заголовка (для gRPC — ключ metadata в нижнем регистре). |
| `WithTrustRequestID(bool)` | `true` | Использовать входящий ID или всегда генерировать новый. Отключайте для публичных сервисов без доверенного прокси. |
| `WithMaxRequestIDLength(n)` | `128` | Максимальная длина входящего ID. |
| `WithLogCompletion(bool)` | `true` | Отключить итоговые записи, оставив обогащение контекста. |

Ставьте middleware **после** `otelhttp`, чтобы были доступны trace ID:

```go
handler := otelhttp.NewHandler(logging.NewMiddleware()(mux), "server")
```

Пример записи:

```json
{"time":"2026-10-01T10:00:00Z","level":"INFO","msg":"request completed","request_id":"9f86d081884c7d65","method":"GET","endpoint":"/users/42","remote_addr":"10.0.0.7:51234","trace_id":"4bf92f3577b34da6a3ce929d0e0e4736","span_id":"00f067aa0ba902b7","route":"GET /users/{id}","status":200,"bytes":512,"duration":1843200}
```

### Сэмплирование между сервисами

С `WithSampling(n)` запрос с валидным trace ID логируется, если младшие
8 байт trace ID делятся на n. Решение зависит только от trace ID, поэтому
все сервисы с одинаковым n логируют **одни и те же трейсы**, и вы видите
весь путь запроса, а не случайные куски. Трейс, залогированный сервисом с
n = 1000, логируют и сервисы с n = 100 или 10 (любым делителем n), поэтому
выбирайте степени десяти. Запросы без trace ID используют счётчик (ровно
каждый n-й запрос).

### Другие транспорты

`RequestTracker` — независимое от транспорта ядро middleware (request ID,
ленивый логгер, сэмплирование, медленные запросы, итоговая запись). На нём
построены gRPC-интерцепторы; используйте его для очередей сообщений или
своих протоколов:

```go
var tracker = logging.NewRequestTracker(logging.WithMiddlewareLogger(logger), logging.WithSampling(100))

func handle(msg *kafka.Message) {
	ctx, req := tracker.Start(context.Background(), msg.Topic, header(msg, "x-request-id"),
		logging.StringAttr("topic", msg.Topic))
	err := process(ctx, msg) // logging.L(ctx) содержит request_id и topic
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

Интерцепторы принимают те же опции, что и HTTP-middleware
(`WithSkipPaths` принимает полные имена методов, например `/grpc.health.v1.Health/Check`).
Каждый вызов даёт запись `rpc completed` с полями `grpc_method`, `grpc_code` и `duration`.
Серверные коды (`Internal`, `Unknown`, `DataLoss`, `Unavailable`,
`DeadlineExceeded`, `Unimplemented`) логируются как Error, остальные не-OK коды — как Warn.

Чтобы только обогащать логгер в контексте без итоговых записей, используйте
`logginggrpc.UnaryServerInterceptor(logging.WithLogCompletion(false))`.

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
	grpc.WithChainUnaryInterceptor(logginggrpc.UnaryClientInterceptor()),
	grpc.WithChainStreamInterceptor(logginggrpc.StreamClientInterceptor()),
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
  **отбрасываются и учитываются** вместо блокировки (`ErrBufferFull`, slog его
  игнорирует). Логи не должны тормозить или ронять сервис.
- **Записи Warn и Error защищены.** Последняя восьмая часть буфера — резерв,
  в который может писать только `w.Priority()`, и `NewLogger` автоматически
  отправляет туда записи Warn/Error. При всплеске Info/Debug отбрасываются
  сначала они, а ошибки по-прежнему проходят. Порядок записей сохраняется.
  Со своим хендлером пишите важные записи в `w.Priority()` сами.
- Буфер, заполненный на четверть, пишется сразу; иначе не реже, чем раз в
  `flushInterval`. Память: до 2 × size.
- Выгружайте `w.Stats()` (`Accepted`, `Dropped`, `DroppedPriority`,
  `BytesWritten`, `WriteErrors`) в метрики; ставьте алерт на `Dropped` и,
  с более высоким приоритетом, на `DroppedPriority` (исчерпан даже резерв).
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
		Thereafter: 100,         // затем каждая 100-я; 0 — остальные отбрасываются
	}),
)

dropped, _ := logging.RateLimitStats(logger) // выгружайте в метрики
```

Отброшенные записи видны и в логах: когда ключ, по которому были потери,
снова появляется в новом интервале, сначала пишется сводная запись с тем же
уровнем (без атрибутов отдельного запроса):

```json
{"level":"ERROR","msg":"log records suppressed","suppressed_msg":"db timeout","suppressed":9900,"interval":1000000000}
```

Сводка пишется, когда ключ появляется снова, поэтому после окончания
шторма последний интервал виден только в `RateLimitStats`.

Счётчики lock-free, без аллокаций и общие для логгеров, полученных через
`With`/`WithGroup`. `logging.NewRateLimitHandler(h, cfg)` оборачивает любой
`slog.Handler`; `WithRateLimit` применяется и к хендлеру из `WithHandler`.

### Прочие советы

- **Не включайте `AddSource`** в проде (выключен по умолчанию).
- **Сэмплируйте успешные запросы** через `WithSampling(n)` и исключайте health-check'и через `WithSkipPaths`.
- **Используйте `WithLevelVar`**, чтобы временно включить debug без рестарта.
- На горячих путях предпочитайте `logger.LogAttrs(ctx, level, msg, attrs...)`: он не упаковывает аргументы в `[]any`.
- Нужен более быстрый энкодер? Основная стоимость — `slog.JSONHandler` (см.
  сравнение ниже). С `WithHandler` работает любой `slog.Handler` (например, на
  базе zap через `go.uber.org/zap/exp/zapslog`); middleware, rate limiting и
  хелперы контекста остаются прежними.

## Бенчмарки

Go 1.27, Intel Core Ultra 5 225H, `-cpu=1`, вывод в `io.Discard`, если не
указано иное. Запуск: `make bench`.

| Сценарий | Результат |
|---|---|
| HTTP middleware, запрос залогирован | 2230 ns, 898 B, 7 allocs |
| HTTP middleware, запрос не логируется (отсеян сэмплированием) | 680 ns, 818 B, 6 allocs |
| `L(ctx).LogAttrs` с логгером из контекста | 640 ns, 0 allocs |
| Шторм ошибок: запись отброшена `WithRateLimit` / без лимитера | 310 ns / 650 ns, 0 allocs |
| Вывод зависает на 20 ms на каждую запись, ~100k записей/с: максимальная блокировка вызова лога | `BufferedWriter` 20.6 ms; `AsyncWriter` 0.04 ms, 0 % потерь |

Оставшиеся аллокации на запрос: состояние запроса, значение контекста,
`r.WithContext`, строка request ID и значение заголовка ответа. Параллельные
бенчмарки (`*Parallel`) измеряют конкуренцию на многоядерных машинах.

### Сравнение с zap и zerolog

`make compare` (отдельный модуль в `benchmarks/`). Та же машина, ns/op на
1 ядре / 8 ядрах, аллокации на операцию. Это одиночные прогоны, поэтому
цифры ориентировочные.

| Сценарий | go-logging (slog JSON) | zap | zerolog |
|---|---|---|---|
| Запись с 4 полями | 771 / 173 ns, 0 allocs | 655 / 95 ns, 1 alloc | 205 / 50 ns, 0 allocs |
| Запись из логгера с 4 полями запроса | 663 / 191 ns, 0 allocs | 370 / 51 ns, 1 alloc | 177 / 56 ns, 0 allocs |
| Выключенный уровень (Debug при Info) | 43 / 5 ns, 0 allocs | 53 / 35 ns, 1 alloc | 5 / 0.6 ns, 0 allocs |
| Новый логгер запроса (4 поля) + одна запись | 1143 / 356 ns, 10 allocs | 769 / 642 ns, 7 allocs | 278 / 233 ns, 1 alloc |

Энкодер zerolog в 3–4 раза быстрее `slog.JSONHandler`, zap — посередине.
Для типичных сервисов (тысячи — десятки тысяч записей в секунду на инстанс)
разница несущественна; для больших объёмов подключите более быстрый хендлер
через `WithHandler`. Благодаря ленивому логгеру middleware большинство
запросов вообще не создаёт логгер запроса.

## Разработка

```bash
make tidy     # go mod tidy во всех модулях
make test     # тесты библиотеки и gRPC-модуля (с -race, если доступен cgo)
make cover    # покрытие библиотеки
make bench    # бенчмарки (middleware, контекст, writer'ы, rate limiting)
make compare  # сравнение с zap и zerolog
make fuzz     # fuzz-тесты, FUZZTIME=5m make fuzz для длинных прогонов
make lint     # golangci-lint (.golangci.yml)
make tags     # теги vX.Y.Z и grpc/vX.Y.Z из ./version
```

В репозитории три модуля: библиотека (`.`), gRPC-интеграция (`grpc/`, при
разработке зависит от библиотеки через локальный `replace`) и сравнительные
бенчмарки (`benchmarks/`, не публикуются). Перед `make tags` убедитесь, что
`grpc/go.mod` требует выпускаемую версию библиотеки. CI
(`.github/workflows/ci.yml`) запускает линтер, тесты с `-race` на минимальной
и последней версии Go, фаззинг и пробный прогон бенчмарков.

## Лицензия

См. [LICENSE](LICENSE).
