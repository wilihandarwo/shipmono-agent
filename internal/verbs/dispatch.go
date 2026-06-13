// Package verbs maps a command's verb to the matching executor method. The
// switch IS the agent-side enforcement of the fixed verb set: any verb outside
// the closed set falls through to a "failed: unknown verb" status and never
// reaches the host. There is no default execution path.
package verbs

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/wilihandarwo/shipmono-agent/internal/controlplane"
	"github.com/wilihandarwo/shipmono-agent/internal/executor"
)

// Sink is what Dispatch needs to emit events: streamed log lines (forwarded to
// the executor) and the single terminal status event.
type Sink interface {
	executor.EventSink
	Status(ev controlplane.Event)
}

// Dispatch runs one command against the executor and emits its terminal status
// event. Streamed log lines are produced by the executor during the call.
func Dispatch(ctx context.Context, ex executor.Executor, cmd controlplane.Command, sink Sink) {
	res := run(ctx, ex, cmd, sink)
	sink.Status(terminalEvent(res))
}

func run(ctx context.Context, ex executor.Executor, cmd controlplane.Command, sink Sink) executor.Result {
	switch cmd.Verb {
	case "deploy":
		p, err := decode[executor.DeployParams](cmd.Params)
		if err != nil {
			return executor.Result{Err: err}
		}
		token := ""
		if cmd.Credentials != nil {
			token = cmd.Credentials.GitToken
		}
		return ex.Deploy(ctx, p, token, sink)
	case "rollback":
		p, err := decode[executor.RollbackParams](cmd.Params)
		if err != nil {
			return executor.Result{Err: err}
		}
		return ex.Rollback(ctx, p, sink)
	case "reload":
		return ex.Reload(ctx, sink)
	case "restore":
		p, err := decode[executor.RestoreParams](cmd.Params)
		if err != nil {
			return executor.Result{Err: err}
		}
		return ex.Restore(ctx, p, sink)
	case "add_domain":
		p, err := decode[executor.DomainParams](cmd.Params)
		if err != nil {
			return executor.Result{Err: err}
		}
		return ex.AddDomain(ctx, p, sink)
	case "remove_domain":
		p, err := decode[executor.DomainParams](cmd.Params)
		if err != nil {
			return executor.Result{Err: err}
		}
		return ex.RemoveDomain(ctx, p, sink)
	case "status":
		return ex.Status(ctx, sink)
	default:
		// The closed-set gate. Unknown verbs never execute anything.
		return executor.Result{Err: fmt.Errorf("unknown verb %s", cmd.Verb)}
	}
}

// terminalEvent converts an executor Result into the single status event the
// contract expects per command.
func terminalEvent(res executor.Result) controlplane.Event {
	if res.Err != nil {
		return controlplane.Event{
			Type:    controlplane.EventStatus,
			Status:  controlplane.StatusFailed,
			Message: res.Err.Error(),
		}
	}
	return controlplane.Event{
		Type:      controlplane.EventStatus,
		Status:    controlplane.StatusSucceeded,
		ReleaseID: res.ReleaseID,
		Health:    res.Health,
	}
}

func decode[T any](raw json.RawMessage) (T, error) {
	var v T
	if len(raw) == 0 {
		return v, nil
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return v, fmt.Errorf("invalid params: %w", err)
	}
	return v, nil
}
