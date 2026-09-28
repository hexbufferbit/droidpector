package storage

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/droidpector/apkinspector/src/model"
)

// WriterOptions tunes the asynchronous event writer.
type WriterOptions struct {
	QueueSize      int           // bounded queue capacity (backpressure point)
	BatchSize      int           // max operations per transaction
	FlushInterval  time.Duration // max latency before a partial batch is committed
	EnqueueTimeout time.Duration // how long producers may block when the queue is full
}

func (o *WriterOptions) defaults() {
	if o.QueueSize <= 0 {
		o.QueueSize = 8192
	}
	if o.BatchSize <= 0 {
		o.BatchSize = 512
	}
	if o.FlushInterval <= 0 {
		o.FlushInterval = 50 * time.Millisecond
	}
	if o.EnqueueTimeout <= 0 {
		o.EnqueueTimeout = 250 * time.Millisecond
	}
}

// WriterStats are exposed to diagnostics and the UI.
type WriterStats struct {
	Queued    int    `json:"queued"`
	Written   int64  `json:"written"`
	Dropped   int64  `json:"dropped"`
	Failed    int64  `json:"failed"`
	LastError string `json:"lastError,omitempty"`
}

type writeOp struct {
	event *model.Event
	frame *frameRec
	flush chan struct{}
}

// Writer persists events asynchronously in batched transactions so that
// capture goroutines never wait on disk I/O. The queue is bounded: when it is
// full, producers block for at most EnqueueTimeout and then the operation is
// dropped and counted (capture must never stall the guest's network).
type Writer struct {
	store *Store
	opts  WriterOptions
	log   *slog.Logger
	ch    chan writeOp
	done  chan struct{}

	closeOnce sync.Once
	closed    atomic.Bool
	written   atomic.Int64
	dropped   atomic.Int64
	failed    atomic.Int64
	lastErr   atomic.Value // string
}

// NewWriter starts the writer goroutine.
func NewWriter(store *Store, opts WriterOptions, log *slog.Logger) *Writer {
	opts.defaults()
	w := &Writer{store: store, opts: opts, log: log, ch: make(chan writeOp, opts.QueueSize), done: make(chan struct{})}
	go w.run()
	return w
}

// Submit enqueues an event snapshot for upsert. The caller must not mutate e
// afterwards. Returns false if the event was dropped.
func (w *Writer) Submit(e *model.Event) bool { return w.enqueue(writeOp{event: e}) }

// SubmitFrame enqueues a WebSocket frame.
func (w *Writer) SubmitFrame(eventID string, f model.WSFrame) bool {
	return w.enqueue(writeOp{frame: &frameRec{eventID: eventID, frame: f}})
}

func (w *Writer) enqueue(op writeOp) (ok bool) {
	if w.closed.Load() {
		w.dropped.Add(1)
		return false
	}
	defer func() {
		if recover() != nil { // channel closed concurrently with Close
			w.dropped.Add(1)
			ok = false
		}
	}()
	select {
	case w.ch <- op:
		return true
	default:
	}
	t := time.NewTimer(w.opts.EnqueueTimeout)
	defer t.Stop()
	select {
	case w.ch <- op:
		return true
	case <-t.C:
		w.dropped.Add(1)
		return false
	}
}

// Flush blocks until every operation submitted before the call is committed.
func (w *Writer) Flush(ctx context.Context) error {
	done := make(chan struct{})
	if !w.enqueue(writeOp{flush: done}) {
		return context.DeadlineExceeded
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close drains the queue, commits it and stops the writer.
func (w *Writer) Close() {
	w.closeOnce.Do(func() {
		w.closed.Store(true)
		close(w.ch)
		<-w.done
	})
}

// Stats returns current counters.
func (w *Writer) Stats() WriterStats {
	s := WriterStats{Queued: len(w.ch), Written: w.written.Load(), Dropped: w.dropped.Load(), Failed: w.failed.Load()}
	if v, ok := w.lastErr.Load().(string); ok {
		s.LastError = v
	}
	return s
}

func (w *Writer) run() {
	defer close(w.done)
	ticker := time.NewTicker(w.opts.FlushInterval)
	defer ticker.Stop()
	var (
		events  []*model.Event
		index   = map[string]int{}
		frames  []frameRec
		flushes []chan struct{}
	)
	commit := func() {
		if len(events) > 0 || len(frames) > 0 {
			err := w.store.apply(context.Background(), events, frames)
			if err != nil {
				// One retry covers transient SQLITE_BUSY; persistent errors (disk
				// full) are counted and surfaced, capture continues.
				time.Sleep(20 * time.Millisecond)
				err = w.store.apply(context.Background(), events, frames)
			}
			if err != nil {
				w.failed.Add(int64(len(events) + len(frames)))
				w.lastErr.Store(err.Error())
				w.log.Error("event store write failed", "err", err, "events", len(events), "frames", len(frames))
			} else {
				w.written.Add(int64(len(events) + len(frames)))
			}
		}
		for _, f := range flushes {
			close(f)
		}
		events, frames, flushes = events[:0], frames[:0], flushes[:0]
		clear(index)
	}
	for {
		select {
		case op, ok := <-w.ch:
			if !ok {
				commit()
				return
			}
			switch {
			case op.event != nil:
				// Coalesce repeated upserts of the same event within a batch,
				// keeping the latest snapshot.
				if i, seen := index[op.event.ID]; seen {
					events[i] = op.event
				} else {
					index[op.event.ID] = len(events)
					events = append(events, op.event)
				}
			case op.frame != nil:
				frames = append(frames, *op.frame)
			case op.flush != nil:
				flushes = append(flushes, op.flush)
				commit()
				continue
			}
			if len(events)+len(frames) >= w.opts.BatchSize {
				commit()
			}
		case <-ticker.C:
			commit()
		}
	}
}
