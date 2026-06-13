// Package health samples host metrics for the heartbeat and the `status` verb.
// The control plane draws usage bars and sparklines from cpu/ram/disk percent,
// so those three fields are the contract's load-bearing values.
//
// The real sampler reads /proc and statfs on Linux (health_linux.go); other
// platforms get a stub returning plausible fixed values (health_other.go) so
// the agent can run end-to-end against a local control plane from macOS/CI.
package health

import (
	"context"

	"github.com/wilihandarwo/shipmono-agent/internal/controlplane"
	"github.com/wilihandarwo/shipmono-agent/internal/version"
)

// Sampler produces a HealthBlob describing the host right now.
type Sampler interface {
	Sample(ctx context.Context) controlplane.HealthBlob
}

// New returns the platform-appropriate sampler for the given app root (the
// filesystem whose free space is reported). appRoot is used by the Linux
// sampler for statfs; the stub ignores it.
func New(appRoot string) Sampler {
	return newSampler(appRoot)
}

// Static returns a sampler that always reports the given blob. Useful in tests
// and as the simulated executor's health source.
func Static(blob controlplane.HealthBlob) Sampler {
	return staticSampler{blob: blob}
}

type staticSampler struct{ blob controlplane.HealthBlob }

func (s staticSampler) Sample(context.Context) controlplane.HealthBlob {
	b := s.blob
	if b.AgentVersion == "" {
		b.AgentVersion = version.Version
	}
	return b
}

// simulated is the plausible fixed blob the off-Linux stub and the simulated
// executor report. Values sit in the same ranges the Ruby simulator emits.
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
