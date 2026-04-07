package logging

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewLogger(t *testing.T) {
	var buf bytes.Buffer

	l := NewLogger(
		WithDevMode(true),
		WithLevel("debug"),
		WithWriter(&buf),
	)
	if l == nil {
		t.Fatal("logger is nil")
	}

	l.Debug("debug test")
	l.Info("info test")

	if buf.Len() == 0 {
		t.Error("expected output to buffer")
	}
}

func TestMiddleware(t *testing.T) {
	// Simple handler
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	mw := Middleware(next)
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()

	mw.ServeHTTP(w, req)

	if w.Header().Get("X-Request-ID") == "" {
		t.Error("expected X-Request-ID header")
	}
	if w.Code != http.StatusOK {
		t.Error("expected status 200")
	}
}
