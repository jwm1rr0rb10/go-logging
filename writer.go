package logging

import (
	"bufio"
	"errors"
	"io"
	"sync"
	"time"
)

const (
	defaultBufferSize    = 256 * 1024
	defaultFlushInterval = time.Second
)

// ErrWriterClosed is returned by BufferedWriter.Write after Close.
var ErrWriterClosed = errors.New("logging: buffered writer is closed")

// BufferedWriter is a goroutine-safe buffered io.Writer for log output.
//
// Without buffering every log record is a separate write syscall. Under high
// load this becomes a contention point, because slog handlers serialize
// writes. BufferedWriter turns most writes into a memory copy and flushes
// when the buffer is full and every flush interval.
//
// Trade-off: records still in the buffer are lost if the process crashes.
// Always call Close (or Flush) on shutdown.
type BufferedWriter struct {
	mu     sync.Mutex
	buf    *bufio.Writer
	closed bool

	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

// NewBufferedWriter wraps w. A size <= 0 uses 256 KiB; a flushInterval <= 0
// uses 1 second.
func NewBufferedWriter(w io.Writer, size int, flushInterval time.Duration) *BufferedWriter {
	if size <= 0 {
		size = defaultBufferSize
	}
	if flushInterval <= 0 {
		flushInterval = defaultFlushInterval
	}
	b := &BufferedWriter{
		buf:  bufio.NewWriterSize(w, size),
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	go b.flushLoop(flushInterval)
	return b
}

func (b *BufferedWriter) flushLoop(interval time.Duration) {
	defer close(b.done)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			_ = b.Flush()
		case <-b.stop:
			return
		}
	}
}

// Write implements io.Writer.
func (b *BufferedWriter) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0, ErrWriterClosed
	}
	return b.buf.Write(p)
}

// Flush writes buffered data to the underlying writer.
func (b *BufferedWriter) Flush() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Flush()
}

// Close stops the background flusher and flushes remaining data.
// It does not close the underlying writer. Safe to call more than once.
func (b *BufferedWriter) Close() error {
	var err error
	b.closeOnce.Do(func() {
		close(b.stop)
		<-b.done
		b.mu.Lock()
		defer b.mu.Unlock()
		b.closed = true
		err = b.buf.Flush()
	})
	return err
}
