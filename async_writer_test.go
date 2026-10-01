package logging

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// blockingWriter blocks every Write until release is closed.
type blockingWriter struct {
	release chan struct{}
	syncBuffer
}

func (b *blockingWriter) Write(p []byte) (int, error) {
	<-b.release
	return b.syncBuffer.Write(p)
}

// gateWriter signals entered on the first Write and blocks until release.
type gateWriter struct {
	entered chan struct{}
	release chan struct{}
	syncBuffer
}

func (g *gateWriter) Write(p []byte) (int, error) {
	select {
	case g.entered <- struct{}{}:
	default:
	}
	<-g.release
	return g.syncBuffer.Write(p)
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("boom") }

func TestAsyncWriterFlushOnClose(t *testing.T) {
	var out syncBuffer
	w := NewAsyncWriter(&out, 1024, time.Hour)
	if _, err := w.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if got := out.buf.String(); got != "hello\n" {
		t.Fatalf("got %q", got)
	}
	if _, err := w.Write([]byte("x")); !errors.Is(err, ErrWriterClosed) {
		t.Fatalf("want ErrWriterClosed, got %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal("second Close must be a no-op:", err)
	}
}

func TestAsyncWriterPeriodicFlush(t *testing.T) {
	var out syncBuffer
	w := NewAsyncWriter(&out, 1024, 10*time.Millisecond)
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

func TestAsyncWriterWakesWhenQuarterFull(t *testing.T) {
	var out syncBuffer
	w := NewAsyncWriter(&out, 64, time.Hour)
	defer w.Close()
	_, _ = w.Write(bytes.Repeat([]byte("a"), 20)) // >= 64/4
	deadline := time.Now().Add(time.Second)
	for out.Len() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("quarter-full buffer must be written without waiting for the ticker")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestAsyncWriterNeverBlocksAndDrops(t *testing.T) {
	out := &blockingWriter{release: make(chan struct{})}
	w := NewAsyncWriter(out, 100, time.Millisecond)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			_, _ = w.Write([]byte("0123456789")) // 10 bytes
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Write blocked on a blocked destination")
	}

	st := w.Stats()
	if st.Dropped == 0 {
		t.Fatalf("expected drops with a blocked destination: %+v", st)
	}
	if st.Accepted+st.Dropped != 1000 {
		t.Fatalf("accepted+dropped must equal writes: %+v", st)
	}

	// Close is bounded by the context while the destination is blocked.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := w.CloseContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want DeadlineExceeded, got %v", err)
	}
	close(out.release)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if got := uint64(out.Len()); got != w.Stats().BytesWritten || got != st.Accepted*10 {
		t.Fatalf("accepted data lost: written=%d stats=%+v", got, w.Stats())
	}
}

func TestAsyncWriterOversizedRecordDropped(t *testing.T) {
	var out syncBuffer
	w := NewAsyncWriter(&out, 8, time.Hour)
	defer w.Close()
	if _, err := w.Write(make([]byte, 9)); !errors.Is(err, ErrBufferFull) {
		t.Fatalf("want ErrBufferFull, got %v", err)
	}
}

func TestAsyncWriterCountsErrors(t *testing.T) {
	w := NewAsyncWriter(failingWriter{}, 64, time.Hour)
	_, _ = w.Write([]byte("x"))
	if err := w.Flush(); err == nil {
		t.Fatal("Flush must return the write error")
	}
	_ = w.Close()
	if w.Stats().WriteErrors != 1 {
		t.Fatalf("want 1 write error, got %+v", w.Stats())
	}
}

func TestAsyncWriterConcurrentWithLogger(t *testing.T) {
	var out syncBuffer
	w := NewAsyncWriter(&out, 1<<20, time.Millisecond)
	l := NewLogger(WithWriter(w), WithSetDefault(false))

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				l.Info("msg", IntAttr("i", i))
				if i%50 == 0 {
					_ = w.Flush()
				}
			}
		}()
	}
	wg.Wait()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if n := len(decodeLines(t, &out.buf)); n != 1600 {
		t.Fatalf("want 1600 complete JSON lines, got %d", n)
	}
}

func TestAsyncWriterPriorityReserve(t *testing.T) {
	out := &gateWriter{entered: make(chan struct{}, 1), release: make(chan struct{})}
	w := NewAsyncWriter(out, 80, time.Hour) // Write may use 70 bytes, Priority 80
	rec := []byte("0123456789")
	// Quarter full: the background goroutine takes this batch and blocks in
	// the destination, leaving an empty buffer.
	_, _ = w.Write(rec)
	_, _ = w.Write(rec)
	<-out.entered
	for i := 0; i < 7; i++ {
		if _, err := w.Write(rec); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if _, err := w.Write(rec); !errors.Is(err, ErrBufferFull) {
		t.Fatalf("plain writes must not use the reserve, got %v", err)
	}
	if _, err := w.Priority().Write(rec); err != nil {
		t.Fatalf("priority write must use the reserve: %v", err)
	}
	if _, err := w.Priority().Write(rec); !errors.Is(err, ErrBufferFull) {
		t.Fatalf("a full buffer must drop priority records too, got %v", err)
	}
	if st := w.Stats(); st.Dropped != 2 || st.DroppedPriority != 1 || st.Accepted != 10 {
		t.Fatalf("unexpected stats: %+v", st)
	}
	close(out.release)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLoggerWithAsyncWriterKeepsErrorsDuringInfoBurst(t *testing.T) {
	out := &blockingWriter{release: make(chan struct{})}
	w := NewAsyncWriter(out, 4096, time.Hour)
	l := NewLogger(WithWriter(w)).With("request_id", "r1").WithGroup("g")

	for i := 0; i < 1000; i++ {
		l.Info("noise", IntAttr("i", i))
	}
	l.Error("important", StringAttr("k", "v"))
	if st := w.Stats(); st.Dropped == 0 || st.DroppedPriority != 0 {
		t.Fatalf("info burst must be dropped, the error kept: %+v", st)
	}

	close(out.release)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	lines := decodeLines(t, &out.buf)
	last := lines[len(lines)-1]
	g, _ := last["g"].(map[string]any)
	if last["msg"] != "important" || last["request_id"] != "r1" || g["k"] != "v" {
		t.Fatalf("error record must keep attrs and groups: %v", last)
	}
	if first := lines[0]; first["request_id"] != "r1" || first["g"] == nil {
		t.Fatalf("info record must keep attrs and groups: %v", first)
	}
}

func TestPriorityHandlerConcurrentMaterialization(t *testing.T) {
	var out syncBuffer
	w := NewAsyncWriter(&out, 1<<20, time.Hour)
	l := NewLogger(WithWriter(w)).With("a", 1)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l.Error("e")
		}()
	}
	wg.Wait()
	_ = w.Close()
	for _, rec := range decodeLines(t, &out.buf) {
		if rec["a"] != float64(1) {
			t.Fatalf("attr lost: %v", rec)
		}
	}
}
