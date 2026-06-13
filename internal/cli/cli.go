// Package cli is the command-line surface of the agent. It matches the contract
// baked into the control plane's public/install.sh exactly:
//
//	shipmono-agent pair --token <token> --host <url>   # one-time registration
//	shipmono-agent run                                 # poll loop (systemd)
//	shipmono-agent version
//
// It also serves as its own GIT_ASKPASS helper: when invoked with
// SHIPMONO_ASKPASS=1 in the environment it prints the ephemeral git token and
// exits, so the token never appears in any git argv.
package cli

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/wilihandarwo/shipmono-agent/internal/config"
	"github.com/wilihandarwo/shipmono-agent/internal/controlplane"
	"github.com/wilihandarwo/shipmono-agent/internal/credstore"
	"github.com/wilihandarwo/shipmono-agent/internal/daemon"
	"github.com/wilihandarwo/shipmono-agent/internal/executor"
	"github.com/wilihandarwo/shipmono-agent/internal/gitops"
	"github.com/wilihandarwo/shipmono-agent/internal/health"
	"github.com/wilihandarwo/shipmono-agent/internal/version"
)

// Main is the process entrypoint; it returns an exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	// GIT_ASKPASS mode: print the token git is asking for and exit. Must run
	// before any normal CLI handling.
	if os.Getenv(gitops.EnvAskpass) != "" {
		fmt.Fprintln(stdout, os.Getenv(gitops.EnvGitToken))
		return 0
	}

	configureLogging(stderr)

	if len(args) < 1 {
		usage(stderr)
		return 2
	}

	switch args[0] {
	case "pair":
		return cmdPair(args[1:], stdout, stderr)
	case "run":
		return cmdRun()
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, version.Version)
		return 0
	case "help", "--help", "-h":
		usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n", args[0])
		usage(stderr)
		return 2
	}
}

func cmdPair(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("pair", flag.ContinueOnError)
	fs.SetOutput(stderr)
	token := fs.String("token", "", "one-time pairing token from the server page (required)")
	host := fs.String("host", "", "control-plane base URL, e.g. https://app.shipmono.dev (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *token == "" || *host == "" {
		fmt.Fprintln(stderr, "pair requires --token and --host")
		return 2
	}

	cfg := config.Load()
	cfg.Host = strings.TrimRight(*host, "/")

	osName, osVersion := osRelease()
	client := controlplane.New(cfg.Host)
	resp, err := client.Register(context.Background(), controlplane.RegisterRequest{
		PairingToken: *token,
		OSName:       osName,
		OSVersion:    osVersion,
		AgentVersion: version.Version,
	})
	if err != nil {
		fmt.Fprintf(stderr, "pairing failed: %v\n", err)
		return 1
	}

	if err := credstore.Save(cfg.Home, credstore.Credential{
		Host:       cfg.Host,
		AgentToken: resp.AgentToken,
		ServerID:   resp.ServerID,
	}); err != nil {
		fmt.Fprintf(stderr, "could not save credential: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "✓ Paired (server #%d). Credential saved to %s (0600).\n", resp.ServerID, credstore.Path(cfg.Home))
	fmt.Fprintln(stdout, "The systemd unit (shipmono-agent.service) will start polling.")
	return 0
}

func cmdRun() int {
	cfg := config.Load()

	cred, err := credstore.Load(cfg.Home)
	if err != nil {
		slog.Error("not paired", "err", err, "hint", "run `shipmono-agent pair --token <t> --host <url>` first")
		return 1
	}
	cfg.Host = cred.Host

	client := controlplane.New(cfg.Host)
	client.SetToken(cred.AgentToken)

	sampler := health.New(cfg.AppRoot)
	ex := executor.New(cfg, sampler)

	// Graceful shutdown on SIGTERM/SIGINT (systemd stop, Ctrl-C).
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	if err := daemon.Run(ctx, cfg, client, ex, sampler); err != nil {
		slog.Error("agent stopped with error", "err", err)
		return 1
	}
	return 0
}

func configureLogging(w io.Writer) {
	slog.SetDefault(slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo})))
}

func usage(w io.Writer) {
	fmt.Fprint(w, `shipmono-agent — the ShipMono deploy agent

Usage:
  shipmono-agent pair --token <token> --host <url>   Register with the control plane (one-time)
  shipmono-agent run                                 Run the poll loop (started by systemd)
  shipmono-agent version                             Print the agent version

Environment:
  SHIPMONO_HOME       Credential + state directory (default /var/lib/shipmono)
  SHIPMONO_APP_ROOT   Deploy root (default /srv/app)
  SHIPMONO_SIMULATE   Set to 1 to use the simulated executor (no host ops)
`)
}

// osRelease returns the OS name/version reported on register. On Linux it reads
// /etc/os-release; elsewhere (dev/CI on macOS) it reports ubuntu/24.04 to mirror
// the Ruby simulator, since the control plane only accepts Ubuntu LTS.
func osRelease() (name, ver string) {
	name, ver = "ubuntu", "24.04"
	f, err := os.Open("/etc/os-release")
	if err != nil {
		return name, ver
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		key, val, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		val = strings.Trim(val, `"`)
		switch key {
		case "ID":
			name = val
		case "VERSION_ID":
			ver = val
		}
	}
	return name, ver
}
