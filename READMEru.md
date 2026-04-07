# logging

**Легковесная, готовая к использованию в производственной среде, контекстно-ориентированная обертка над Go-библиотекой `log/slog` со встроенной возможностью мониторинга, логирования запросов/ответов и превосходным удобством для разработчиков.**

Эта библиотека предоставляет чистое, продуманное и легко настраиваемое решение для логирования, которое бесперебойно работает с OpenTelemetry, HTTP-серверами и gRPC-сервисами. Она устраняет рутинную работу, предоставляя структурированные, отслеживаемые и красивые логи «из коробки».

---

## ✨ Features

- **«Бесшовная» обертка для `slog`** — полная совместимость со стандартным пакетом `log/slog`
- **Контекстно-зависимое логирование** — логгеры с областью видимости запроса (request-scoped) через `L(ctx)` и `ContextWithAttrs`
- **Конфигурация на основе переменных окружения** — автоматическое чтение `LOG_LEVEL` или `SLOG_LEVEL`
- **Режим разработчика (DevMode)** — красивый цветной текстовый вывод + автоматический уровень отладки для локальной разработки
- **Полнофункциональное middleware для HTTP-запросов/ответов**:
    - Автоматическая генерация и сквозная передача `X-Request-ID`
    - Логирование метода, эндпоинта, удаленного адреса, ID трассировки/спана
    - Фиксация кода состояния, времени выполнения и объема записанных данных
    - Интеллектуальный выбор уровня логирования: `Error` для 5xx, `Warn` для 4xx, `Info` в остальных случаях
- **Унарный интерцептор для gRPC** с корреляцией трассировок и спанов
- **Расширенный набор вспомогательных функций для атрибутов** (`ErrAttr`, `TimeAttr`, `UInt32Attr` и др.)
- **Функциональные опции** для лаконичной настройки
- **Поддержка пользовательских писателей (writers)** — файлы, буферы, составные писатели (multi-writers) и т. д.
- **Отсутствие внешних зависимостей**, за исключением `go.opentelemetry.io/otel` (который уже используется в большинстве современных бэкендов)
- **Полностью протестировано** и готово к использованию в продакшене

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

`logging.Middleware` — главная звезда этой библиотеки.
**Что она делает автоматически:**

- Генерирует или считывает `X-Request-ID`
- Добавляет его в заголовок ответа
- Обогащает логгер следующими данными: `request_id`, `method`, `endpoint`, `remote_addr`, `trace_id`, `span_id`
- Логирует контекст входящего запроса
- Фиксирует статус, длительность и размер ответа
- Логирует завершенный запрос, используя «умный» уровень логирования

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

Интерцептор автоматически добавляет `method`, `trace_id` и `span_id` в логгер для каждого RPC-вызова.

---

## 🧪 Testing
Библиотека включает исчерпывающие тесты:

```bash
go test ./... -v
```

См. `logger_test.go` для примеров тестирования как `NewLogger`, так и `Middleware`.

---

## 🛠️ Best Practices

1. **Всегда используйте контекстно-зависимое логирование** — `logging.L(ctx)` или `ContextWithAttrs`.
2. **Используйте** `ContextWithAttrs` в самом начале ваших обработчиков или промежуточного ПО (middleware).
3. **Включайте** `WithDevMode(true)` в средах разработки или локальных средах.
4. **Оставляйте** `WithAddSource(true)` включенным в продакшене (это очень полезно для отладки).
5. **Используйте структурированные атрибуты** вместо `fmt.Sprintf`.
6. **Доверьте обработку ID запросов и трассировку промежуточному ПО** — не делайте это вручную.

---

## 🤝 Contributing
Мы приветствуем ваш вклад! Смело открывайте Issues или Pull Requests для:

- Дополнительных функций промежуточного ПО (middleware)
- Сэмплирования логов
- Логирования тел запросов и ответов (с ограничением по размеру)
- Улучшения цветового оформления вывода
- Добавления новых примеров

---

## 📄 License
[MIT License](https://github.com/jwm1rr0rb10/go-logging/blob/main/LICENSE) – © Raman Zaitsau [@jwm1rrr0rb10](https://github.com/jwm1rr0rb10)

Сделано с ❤️ для Go-команд, которые хотят чистые, наблюдаемые и приятные логи.