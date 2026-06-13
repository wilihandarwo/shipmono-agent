// Package daemon runs the agent's poll loop: poll → ack → dispatch → events,
// with an idle heartbeat, revocation handling, and capped backoff when the
// control plane is unreachable. It is the production counterpart to the loop in
// the Ruby simulator (bin/dev-agent).
package daemon

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/wilihandarwo/shipmono-agent/internal/config"
	"github.com/wilihandarwo/shipmono-agent/internal/controlplane"
	"github.com/wilihandarwo/shipmono-agent/internal/credstore"
	"github.com/wilihandarwo/shipmono-agent/internal/executor"
	"github.com/wilihandarwo/shipmono-agent/internal/health"
	"github.com/wilihandarwo/shipmono-agent/internal/verbs"
)

// maxBackoff caps the unreachable-retry delay. Steady-state idle polling stays
// at cfg.PollInterval (2s); only consecutive failures grow the delay.
const maxBackoff = 30 * time.Second

// Run drives the poll loop until the context is cancelled (graceful shutdown,
// returns nil) or the identity is revoked (credential deleted, returns nil).
func Run(ctx context.Context, cfg config.Config, client *controlplane.Client, ex executor.Executor, sampler health.Sampler) error {
	slog.Info("agent polling", "host", cfg.Host, "interval", cfg.PollInterval, "mode", ex.Mode())
	bo := newBackoff(cfg.PollInterval, maxBackoff)

	for {
		if ctx.Err() != nil {
			slog.Info("shutting down")
			return nil
		}

		cmd, err := client.Poll(ctx)
		switch {
		case err == nil:
			handleCommand(ctx, client, ex, cmd)
			bo.reset()
			// Drain: immediately poll again so a queue of commands runs back
			// to back rather than one per interval.
			continue

		case errors.Is(err, controlplane.ErrNoWork):
			bo.reset()
			sendHeartbeat(ctx, client, sampler)
			if !sleep(ctx, cfg.PollInterval) {
				return nil
			}

		case errors.Is(err, controlplane.ErrRevoked):
			slog.Warn("identity revoked by control plane — deleting credential and exiting")
			if derr := credstore.Delete(cfg.Home); derr != nil {
				slog.Error("failed to delete credential", "err", derr)
			}
			return nil

		case errors.Is(err, controlplane.ErrUnauthorized):
			// Token rejected but not revoked: re-pairing is needed. Keep the
			// credential, back off, and keep trying rather than churning under
			// systemd's Restart=always.
			slog.Error("unauthorized — the agent needs to be re-paired", "err", err)
			if !sleep(ctx, bo.next()) {
				return nil
			}

		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return nil

		default:
			d := bo.next()
			slog.Warn("poll failed; backing off", "err", err, "retry_in", d)
			if !sleep(ctx, d) {
				return nil
			}
		}
	}
}

// handleCommand acks the command immediately, then dispatches it, streaming log
// events and posting the terminal status via the batching sink.
func handleCommand(ctx context.Context, client *controlplane.Client, ex executor.Executor, cmd *controlplane.Command) {
	slog.Info("command received", "id", cmd.ID, "verb", cmd.Verb)
	if err := client.Ack(ctx, cmd.ID); err != nil {
		// Ack is best-effort; the command still runs and reports via events.
		slog.Warn("ack failed", "command", cmd.ID, "err", err)
	}
	sink := newEventSink(ctx, client, cmd.ID)
	verbs.Dispatch(ctx, ex, *cmd, sink)
	slog.Info("command finished", "id", cmd.ID, "verb", cmd.Verb)
}

func sendHeartbeat(ctx context.Context, client *controlplane.Client, sampler health.Sampler) {
	if err := client.Heartbeat(ctx, sampler.Sample(ctx)); err != nil {
		slog.Warn("heartbeat failed", "err", err)
	}
}

// sleep waits for d or until the context is cancelled. It returns false if the
// context was cancelled (caller should stop).
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// backoff is capped exponential with jitter. reset() returns to the base delay
// after any success.
type backoff struct {
	base, max, cur time.Duration
}

func newBackoff(base, max time.Duration) *backoff {
	return &backoff{base: base, max: max, cur: base}
}

func (b *backoff) reset() { b.cur = b.base }

func (b *backoff) next() time.Duration {
	d := b.cur
	b.cur *= 2
	if b.cur > b.max {
		b.cur = b.max
	}
	// ±20% jitter so a fleet doesn't retry in lockstep against a down server.
	jitter := 1 + (rand.Float64()*0.4 - 0.2)
	return time.Duration(float64(d) * jitter)
}
