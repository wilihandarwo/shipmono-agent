// Package executor is the seam between the (fully real, shared) transport layer
// and the host operations. The real Linux executor performs git checkouts,
// release builds, atomic symlink swaps, and FrankenPHP reloads; the simulated
// executor logs the identical steps without touching the host so the agent runs
// end-to-end against a local control plane on macOS and in CI.
//
// Executors only ever stream log lines through the EventSink and return a
// Result. The verb dispatcher (package verbs) translates the Result into the
// single terminal status event the contract requires — executors never build
// status events themselves.
package executor

import (
	"context"

	"github.com/wilihandarwo/shipmono-agent/internal/controlplane"
)

// Mode identifies which executor is running, for startup logging.
type Mode string

const (
	ModeLinux    Mode = "linux"
	ModeSimulate Mode = "simulate"
)

// EventSink receives streamed log lines during command execution.
type EventSink interface {
	Log(line string)
}

// Result is the outcome of executing one verb.
//
//   - Err != nil           → terminal status "failed" with Err's message.
//   - ReleaseID != ""      → carried on a successful deploy/rollback.
//   - Health != nil        → carried by the `status` verb.
type Result struct {
	ReleaseID string
	Health    *controlplane.HealthBlob
	Err       error
}

func ok() Result                                { return Result{} }
func okRelease(id string) Result                { return Result{ReleaseID: id} }
func okHealth(h controlplane.HealthBlob) Result { return Result{Health: &h} }
func failed(err error) Result                   { return Result{Err: err} }

// Verb parameter structs. Each is unmarshalled from the command's raw params by
// the dispatcher and handed to the matching executor method. The field set is
// exactly the control plane's per-verb allowlist.

type DeployParams struct {
	RepoID       int    `json:"repo_id"`
	RepoFullName string `json:"repo_full_name"`
	GitRef       string `json:"git_ref"`
	GitSha       string `json:"git_sha"`
}

type RollbackParams struct {
	ReleaseID string `json:"release_id"`
}

type RestoreParams struct {
	PointInTime string `json:"point_in_time"`
}

type DomainParams struct {
	Domain string `json:"domain"`
}

// Executor performs the fixed verb set on the host. There is deliberately no
// generic "run command" method: the closed interface is the local enforcement
// of the security model's fixed verb set.
type Executor interface {
	Mode() Mode

	// Deploy checks out the repo (allowlisted by numeric RepoID) at GitSha
	// using the ephemeral gitToken, builds a release, swaps the current
	// symlink atomically, and reloads FrankenPHP. Returns the release id.
	Deploy(ctx context.Context, p DeployParams, gitToken string, sink EventSink) Result

	// Rollback swaps the current symlink to a prior release and reloads.
	Rollback(ctx context.Context, p RollbackParams, sink EventSink) Result

	// Reload gracefully reloads FrankenPHP.
	Reload(ctx context.Context, sink EventSink) Result

	// Restore performs a Litestream point-in-time restore.
	Restore(ctx context.Context, p RestoreParams, sink EventSink) Result

	// AddDomain adds a host to the FrankenPHP config and reloads (ACME
	// provisions the certificate on first request).
	AddDomain(ctx context.Context, p DomainParams, sink EventSink) Result

	// RemoveDomain removes a host from the FrankenPHP config and reloads.
	RemoveDomain(ctx context.Context, p DomainParams, sink EventSink) Result

	// Status reports current host health.
	Status(ctx context.Context, sink EventSink) Result
}
