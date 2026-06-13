package daemon

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/wilihandarwo/shipmono-agent/internal/controlplane"
)

// Batch tuning: flush when this many log lines accumulate, or when this much
// time has passed since the last flush — whichever comes first. This collapses
// a burst of git output into a few array POSTs instead of one request per line,
// while keeping latency low enough that the live deploy log streams smoothly.
const (
	maxBatch      = 20
	flushInterval = 250 * time.Millisecond
)

// eventSink buffers log events for one command and posts them to the control
// plane. It implements verbs.Sink (Log + Status). The terminal Status flushes
// any pending logs first so ordering is preserved.
type eventSink struct {
	ctx    context.Context
	client *controlplane.Client
	cmdID  int

	mu        sync.Mutex
	buf       []controlplane.Event
	lastFlush time.Time
}

func newEventSink(ctx context.Context, client *controlplane.Client, cmdID int) *eventSink {
	return &eventSink{ctx: ctx, client: client, cmdID: cmdID, lastFlush: time.Now()}
}

// Log queues a log line, flushing when the batch is full or the flush interval
// has elapsed.
func (s *eventSink) Log(line string) {
	s.mu.Lock()
	s.buf = append(s.buf, controlplane.Event{Type: controlplane.EventLog, Line: line})
	full := len(s.buf) >= maxBatch || time.Since(s.lastFlush) >= flushInterval
	s.mu.Unlock()
	if full {
		s.flush()
	}
}

// Status flushes pending logs, then posts the terminal status event on its own.
func (s *eventSink) Status(ev controlplane.Event) {
	s.flush()
	if err := s.client.PostEvents(s.ctx, s.cmdID, []controlplane.Event{ev}); err != nil {
		slog.Warn("post status event failed", "command", s.cmdID, "err", err)
	}
}

func (s *eventSink) flush() {
	s.mu.Lock()
	if len(s.buf) == 0 {
		s.mu.Unlock()
		return
	}
	batch := s.buf
	s.buf = nil
	s.lastFlush = time.Now()
	s.mu.Unlock()

	if err := s.client.PostEvents(s.ctx, s.cmdID, batch); err != nil {
		slog.Warn("post log events failed", "command", s.cmdID, "count", len(batch), "err", err)
	}
}
