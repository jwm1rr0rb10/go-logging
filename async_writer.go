package logging

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultAsyncBufferSize    = 1 << 20 // 1 MiB
	defaultAsyncFlushInterval = 200 * time.Millisecond
)

// ErrBufferFull is returned by AsyncWriter.Write when the record was dropped
// because the buffer is full. log/slog ignores write errors, so for a
// logger this simply means the record is lost; see AsyncWriter.Stats.
var ErrBufferFull = errors.New("logging: async writer buffer is full, record dropped")

// AsyncWriterStats is a snapshot of AsyncWriter counters. Export them to
// your metrics system to see dropped records and output failures.
type AsyncWriterStats struct {
	Accepted     uint64 // records (Write calls) accepted into the buffer
	Dropped      uint64 // records dropped because the buffer was full
	BytesWritten uint64 // bytes successfully written to the underlying writer
	WriteErrors  uint64 // failed writes to the underlying writer
}

// AsyncWriter is a non-blocking, goroutine-safe io.Writer for log output.
//
// Write only copies the record into an in-memory buffer under a short lock;
// a background goroutine writes the accumulated batch to the underlying
// writer outside the lock. A slow or blocked destination (a full pipe, a
// slow container log driver) therefore never blocks request goroutines.
//
// When the pending data reaches the buffer size, new records are dropped
// and counted instead of blocking (see Stats). Logs must never slow down or
// take down the service.
//
// Memory: up to two buffers of the configured size (one being filled, one
// being written).
//
// Records still in the buffer are lost if the process crashes. Call Close
// (or CloseContext with a deadline) on graceful shutdown.
type AsyncWriter struct {
	out     io.Writer
	size    int
	wakeAt  int
	mu      sync.Mutex // guards cur, spare, closed
	cur     []byte
	spare   []byte
	closed  bool
	drainMu sync.Mutex // serializes writes to out

	wake      chan struct{}
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error

	accepted     atomic.Uint64
	dropped      atomic.Uint64
	bytesWritten atomic.Uint64
	writeErrors  atomic.Uint64
}

// NewAsyncWriter wraps w. size is the maximum amount of pending data in
// bytes (<= 0 uses 1 MiB); a single record larger than size is always
// dropped. flushInterval is the maximum delay before pending data is
// written (<= 0 uses 200ms); a quarter-full buffer is written immediately.
func NewAsyncWriter(w io.Writer, size int, flushInterval time.Duration) *AsyncWriter {
	if size <= 0 {
		size = defaultAsyncBufferSize
	}
	if flushInterval <= 0 {
		flushInterval = defaultAsyncFlushInterval
	}
	a := &AsyncWriter{
		out:    w,
		size:   size,
		wakeAt: size / 4,
		cur:    make([]byte, 0, size),
		spare:  make([]byte, 0, size),
		wake:   make(chan struct{}, 1),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	go a.loop(flushInterval)
	return a
}

// Write implements io.Writer. It never blocks on the underlying writer.
// If the buffer is full the record is dropped and ErrBufferFull is returned.
func (a *AsyncWriter) Write(p []byte) (int, error) {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return 0, ErrWriterClosed
	}
	if len(a.cur)+len(p) > a.size {
		a.mu.Unlock()
		a.dropped.Add(1)
		return 0, ErrBufferFull
	}
	a.cur = append(a.cur, p...)
	full := len(a.cur) >= a.wakeAt
	a.mu.Unlock()

	a.accepted.Add(1)
	if full {
		select {
		case a.wake <- struct{}{}:
		default:
		}
	}
	return len(p), nil
}

func (a *AsyncWriter) loop(interval time.Duration) {
	defer close(a.done)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
		case <-a.wake:
		case <-a.stop:
			a.closeErr = a.drain()
			return
		}
		_ = a.drain()
	}
}

// drain swaps the buffers and writes the pending batch outside a.mu.
func (a *AsyncWriter) drain() error {
	a.drainMu.Lock()
	defer a.drainMu.Unlock()

	a.mu.Lock()
	batch := a.cur
	a.cur, a.spare = a.spare[:0], nil
	a.mu.Unlock()

	var err error
	if len(batch) > 0 {
		var n int
		n, err = a.out.Write(batch)
		a.bytesWritten.Add(uint64(n))
		if err != nil {
			a.writeErrors.Add(1)
		}
	}

	a.mu.Lock()
	a.spare = batch[:0]
	a.mu.Unlock()
	return err
}

// Flush synchronously writes pending data to the underlying writer.
func (a *AsyncWriter) Flush() error {
	return a.drain()
}

// Stats returns a snapshot of the counters.
func (a *AsyncWriter) Stats() AsyncWriterStats {
	return AsyncWriterStats{
		Accepted:     a.accepted.Load(),
		Dropped:      a.dropped.Load(),
		BytesWritten: a.bytesWritten.Load(),
		WriteErrors:  a.writeErrors.Load(),
	}
}

// Close stops accepting records, writes the remaining data and stops the
// background goroutine. It does not close the underlying writer and may
// block for as long as the underlying writer blocks; use CloseContext to
// bound it. Safe to call more than once.
func (a *AsyncWriter) Close() error {
	return a.CloseContext(context.Background())
}

// CloseContext is Close with a deadline. If ctx expires before the remaining
// data is written, it returns ctx.Err(); the final write continues in the
// background.
func (a *AsyncWriter) CloseContext(ctx context.Context) error {
	a.closeOnce.Do(func() {
		a.mu.Lock()
		a.closed = true
		a.mu.Unlock()
		close(a.stop)
	})
	select {
	case <-a.done:
		return a.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}
