//go:build !linux

package health

import (
	"context"

	"github.com/wilihandarwo/shipmono-agent/internal/controlplane"
)

// newSampler on non-Linux hosts returns simulated values — the real /proc and
// statfs reads only mean anything on the Ubuntu target.
func newSampler(_ string) Sampler { return stubSampler{} }

type stubSampler struct{}

func (stubSampler) Sample(context.Context) controlplane.HealthBlob { return simulated() }
