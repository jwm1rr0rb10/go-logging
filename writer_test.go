package logging

import (
	"bytes"
	"errors"
	"sync"
	"testing"
	"time"
)

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Len()
}

func TestBufferedWriterFlushOnClose(t *testing.T) {
	var out syncBuffer
	w := NewBufferedWriter(&out, 1024, time.Hour)

	if _, err := w.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatal("data must stay in the buffer before flush")
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if out.Len() != len("hello\n") {
		t.Fatalf("data not flushed on close, got %d bytes", out.Len())
	}
	if err := w.Close(); err != nil {
		t.Fatal("second Close must be a no-op")
	}
	if _, err := w.Write([]byte("x")); !errors.Is(err, ErrWriterClosed) {
		t.Fatalf("want ErrWriterClosed, got %v", err)
	}
}

func TestBufferedWriterPeriodicFlush(t *testing.T) {
	var out syncBuffer
	w := NewBufferedWriter(&out, 1024, 10*time.Millisecond)
	defer w.Close()

	_, _ = w.Write([]byte("tick\n"))
	deadline := time.Now().Add(time.Second)
	for out.Len() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("periodic flush did not happen")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestBufferedWriterConcurrentWithLogger(t *testing.T) {
	var out syncBuffer
	w := NewBufferedWriter(&out, 4096, time.Hour)
	l := NewLogger(WithWriter(w), WithSetDefault(false))

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				l.Info("concurrent", IntAttr("j", j))
			}
		}()
	}
	wg.Wait()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	out.mu.Lock()
	defer out.mu.Unlock()
	lines := decodeLines(t, &out.buf) // fails on interleaved lines
	if len(lines) != 8*200 {
		t.Fatalf("want %d lines, got %d", 8*200, len(lines))
	}
}
