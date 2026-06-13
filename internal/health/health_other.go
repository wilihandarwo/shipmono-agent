//go:build !linux

package health

import (
	"context"

	"github.com/wilihandarwo/shipmono-agent/internal/controlplane"
	"github.com/wilihandarwo/shipmono-agent/internal/version"
)

// newSampler on non-Linux hosts returns simulated values — the real /proc and
// statfs reads only mean anything on the Ubuntu target.
func newSampler(_ string) Sampler { return stubSampler{} }

type stubSampler struct{}

func (stubSampler) Sample(context.Context) controlplane.HealthBlob { return simulated() }

// simulated is the plausible fixed blob the off-Linux stub reports. Values sit
// in the same ranges the Ruby simulator emits. It lives here (not in health.go)
// so it isn't compiled — and flagged unused — on the Linux build.
func simulated() controlplane.HealthBlob {
	return controlplane.HealthBlob{
		AgentVersion:      version.Version,
		FrankenPHPVersion: "1.4.4",
		FrankenPHPHealthy: true,
		DiskFreeBytes:     42_000_000_000,
		LoadAvg:           "0.25",
		CPUPercent:        24,
		RAMPercent:        46,
		DiskPercent:       38,
	}
}
