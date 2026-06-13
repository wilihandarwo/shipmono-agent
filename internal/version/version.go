// Package version exposes the agent's build version. It is reported to the
// control plane as agent_version on register and every heartbeat, so it must
// match the version string a release is published under.
package version

// Version is injected at build time via:
//
//	-ldflags "-X github.com/wilihandarwo/shipmono-agent/internal/version.Version=0.1.0"
//
// It defaults to "dev" for local builds that don't set it.
var Version = "dev"
