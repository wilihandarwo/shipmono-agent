// Package config resolves the agent's runtime configuration from environment
// variables and CLI flags. The defaults match the host contract baked into the
// control plane's public/install.sh (SHIPMONO_HOME=/var/lib/shipmono,
// app root /srv/app, 2s poll cadence).
package config

import (
	"os"
	"runtime"
	"time"
)

// Defaults mirror install.sh exactly. Changing these means changing install.sh.
const (
	DefaultHome    = "/var/lib/shipmono"
	DefaultAppRoot = "/srv/app"
	DefaultPoll    = 2 * time.Second
	// FrankenPHPUnit is the systemd unit name (used only for read-only health
	// checks via `systemctl is-active`, which needs no privilege).
	FrankenPHPUnit = "shipmono-frankenphp.service"
	// FrankenPHPBin is the FrankenPHP binary, used to reload the running server
	// via its localhost admin API — no sudo, no systemctl.
	FrankenPHPBin = "/usr/local/bin/frankenphp"
)

// Config is the resolved runtime configuration.
type Config struct {
	// Home is SHIPMONO_HOME — where the credential is persisted (0600).
	Home string

	// AppRoot is the deploy root: releases/, shared/, current symlink.
	AppRoot string

	// Host is the control-plane base URL. For `pair` it comes from --host; for
	// `run` it is loaded from the persisted credential.
	Host string

	// Simulate selects the simulated executor (no host ops) instead of the
	// real Linux executor. See resolveSimulate.
	Simulate bool

	// PollInterval is the steady-state idle poll cadence. Seeded from the
	// register response's poll_interval, defaulting to DefaultPoll.
	PollInterval time.Duration

	// SimStepDelay is the pause the simulated executor inserts between streamed
	// log lines so a local demo's deploy log is readable. Production uses the
	// default; tests leave it zero for instant runs.
	SimStepDelay time.Duration
}

// DefaultSimStepDelay paces the simulated executor's log streaming.
const DefaultSimStepDelay = 400 * time.Millisecond

// Load builds a Config from the environment, applying defaults.
func Load() Config {
	return Config{
		Home:         envOr("SHIPMONO_HOME", DefaultHome),
		AppRoot:      envOr("SHIPMONO_APP_ROOT", DefaultAppRoot),
		Simulate:     resolveSimulate(),
		PollInterval: DefaultPoll,
		SimStepDelay: DefaultSimStepDelay,
	}
}

// resolveSimulate decides whether to use the simulated executor:
//
//  1. SHIPMONO_SIMULATE=1/true forces simulation (even on Linux, for debugging).
//  2. Otherwise, any non-Linux host must simulate — the real host ops (sudo,
//     systemctl, /srv/app) only exist on the Ubuntu target.
//  3. Otherwise (Linux, no override): run the real executor.
func resolveSimulate() bool {
	switch os.Getenv("SHIPMONO_SIMULATE") {
	case "1", "true", "yes":
		return true
	}
	return runtime.GOOS != "linux"
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
