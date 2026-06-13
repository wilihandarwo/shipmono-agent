package executor

import (
	"time"

	"github.com/wilihandarwo/shipmono-agent/internal/config"
	"github.com/wilihandarwo/shipmono-agent/internal/health"
)

// Deps are the shared dependencies both executors need.
type Deps struct {
	AppRoot   string
	Sampler   health.Sampler
	StepDelay time.Duration // simulated executor's per-log-line pace
}

// New selects the executor per the resolved configuration: the simulated
// executor when cfg.Simulate is set (forced, or any non-Linux host), otherwise
// the real Linux executor.
//
// newLinuxExecutor is only a real implementation under //go:build linux; on
// other platforms it is a stub that panics, but it is unreachable there because
// config.resolveSimulate forces Simulate=true off Linux.
func New(cfg config.Config, sampler health.Sampler) Executor {
	deps := Deps{AppRoot: cfg.AppRoot, Sampler: sampler, StepDelay: cfg.SimStepDelay}
	if cfg.Simulate {
		return newSimExecutor(deps)
	}
	return newLinuxExecutor(deps)
}
