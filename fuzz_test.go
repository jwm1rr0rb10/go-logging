package logging

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// FuzzRequestID checks that any incoming X-Request-ID yields a log-safe ID
// and a valid JSON record (no log injection through the header).
func FuzzRequestID(f *testing.F) {
	for _, s := range []string{"", "abc-123", "a\nb", `"},"level":"ERROR`, "\x00", "ü", string(make([]byte, 300))} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, incoming string) {
		var buf bytes.Buffer
		mw := NewMiddleware(WithMiddlewareLogger(newTestLogger(&buf)))
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header["X-Request-Id"] = []string{incoming}
		id := serve(t, mw, func(http.ResponseWriter, *http.Request) {}, req).Header().Get("X-Request-ID")

		if !validRequestID(id, defaultMaxRequestIDLen) {
			t.Fatalf("unsafe request id %q for input %q", id, incoming)
		}
		if validRequestID(incoming, defaultMaxRequestIDLen) && id != incoming {
			t.Fatalf("valid id %q replaced with %q", incoming, id)
		}
		lines := decodeLines(t, &buf)
		if len(lines) != 1 || lines[0]["request_id"] != id {
			t.Fatalf("bad record for %q: %v", incoming, lines)
		}
	})
}

// FuzzParseLevel checks that parsing never panics and round-trips.
func FuzzParseLevel(f *testing.F) {
	for _, s := range []string{"debug", "INFO", "warning", "error+2", "info-4", " warn ", "", "loud"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		l, err := ParseLevel(s)
		if err != nil {
			return
		}
		back, err := ParseLevel(l.String())
		if err != nil || back != l {
			t.Fatalf("%q -> %v -> %q -> %v (%v)", s, l, l.String(), back, err)
		}
	})
}

// FuzzAsyncWriter checks that accepted records are written completely and
// in order, whatever their sizes and the priority mix.
func FuzzAsyncWriter(f *testing.F) {
	f.Add([]byte("hello\nworld\n"), uint8(64), uint8(3))
	f.Add([]byte{}, uint8(1), uint8(0))
	f.Fuzz(func(t *testing.T, data []byte, size, chunk uint8) {
		var out syncBuffer
		w := NewAsyncWriter(&out, int(size)+1, time.Millisecond)
		step := int(chunk%16) + 1
		var want []byte
		for i := 0; i < len(data); i += step {
			p := data[i:min(i+step, len(data))]
			write := w.Write
			if i%3 == 0 {
				write = w.Priority().Write
			}
			if n, err := write(p); err == nil {
				if n != len(p) {
					t.Fatalf("short write: %d of %d", n, len(p))
				}
				want = append(want, p...)
			}
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(out.buf.Bytes(), want) {
			t.Fatalf("output mismatch:\n got %q\nwant %q", out.buf.Bytes(), want)
		}
	})
}
