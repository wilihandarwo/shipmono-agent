package executor

import (
	"context"
	"fmt"
	"time"

	"github.com/wilihandarwo/shipmono-agent/internal/release"
)

// simExecutor logs the same step sequence the real executor would produce, but
// touches nothing on the host. To the control plane its events are
// indistinguishable from a real deploy — which is exactly what makes it a
// faithful local end-to-end against the live contract.
type simExecutor struct {
	deps      Deps
	stepDelay time.Duration // pause between streamed log lines (0 in tests)
}

func newSimExecutor(deps Deps) Executor {
	return &simExecutor{deps: deps, stepDelay: deps.StepDelay}
}

func (e *simExecutor) Mode() Mode { return ModeSimulate }

// step logs a line and pauses briefly so streamed logs are visible in the UI,
// mirroring the Ruby simulator's 0.5s-per-line cadence.
func (e *simExecutor) step(ctx context.Context, sink EventSink, line string) bool {
	sink.Log(line)
	if e.stepDelay == 0 {
		return ctx.Err() == nil
	}
	select {
	case <-time.After(e.stepDelay):
		return true
	case <-ctx.Done():
		return false
	}
}

func (e *simExecutor) Deploy(ctx context.Context, p DeployParams, gitToken string, sink EventSink) Result {
	sha := release.ShortSHA(p.GitSha)
	tokenState := "missing"
	if gitToken != "" {
		tokenState = "present"
	}
	rel := release.NewID()
	steps := []string{
		fmt.Sprintf("Cloning %s @ %s (token %s)…", p.RepoFullName, sha, tokenState),
		"php -l index.php — no syntax errors",
		fmt.Sprintf("Building release releases/%s", rel),
		"Symlinking shared/app.db into the release",
		fmt.Sprintf("Atomic symlink swap: current → releases/%s", rel),
		"FrankenPHP graceful reload — zero downtime",
	}
	for _, s := range steps {
		if !e.step(ctx, sink, s) {
			return failed(ctx.Err())
		}
	}
	return okRelease(rel)
}

func (e *simExecutor) Rollback(ctx context.Context, p RollbackParams, sink EventSink) Result {
	for _, s := range []string{
		fmt.Sprintf("Symlink swap: current → releases/%s", p.ReleaseID),
		"FrankenPHP graceful reload",
	} {
		if !e.step(ctx, sink, s) {
			return failed(ctx.Err())
		}
	}
	return okRelease(p.ReleaseID)
}

func (e *simExecutor) Reload(ctx context.Context, sink EventSink) Result {
	sink.Log("FrankenPHP graceful reload")
	return ok()
}

func (e *simExecutor) Restore(ctx context.Context, p RestoreParams, sink EventSink) Result {
	sink.Log(fmt.Sprintf("Litestream restore to %s…", p.PointInTime))
	return ok()
}

func (e *simExecutor) AddDomain(ctx context.Context, p DomainParams, sink EventSink) Result {
	for _, s := range []string{
		fmt.Sprintf("Adding %s to FrankenPHP config", p.Domain),
		"Reloading — certificate will be provisioned on first request (ACME)",
	} {
		if !e.step(ctx, sink, s) {
			return failed(ctx.Err())
		}
	}
	return ok()
}

func (e *simExecutor) RemoveDomain(ctx context.Context, p DomainParams, sink EventSink) Result {
	sink.Log(fmt.Sprintf("Removing %s from FrankenPHP config", p.Domain))
	return ok()
}

func (e *simExecutor) Status(ctx context.Context, sink EventSink) Result {
	return okHealth(e.deps.Sampler.Sample(ctx))
}
