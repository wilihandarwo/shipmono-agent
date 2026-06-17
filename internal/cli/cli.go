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
	"time"

	"github.com/wilihandarwo/shipmono-agent/internal/config"
	"github.com/wilihandarwo/shipmono-agent/internal/controlplane"
	"github.com/wilihandarwo/shipmono-agent/internal/credstore"
	"github.com/wilihandarwo/shipmono-agent/internal/daemon"
	"github.com/wilihandarwo/shipmono-agent/internal/executor"
	"github.com/wilihandarwo/shipmono-agent/internal/gitops"
	"github.com/wilihandarwo/shipmono-agent/internal/health"
	"github.com/wilihandarwo/shipmono-agent/internal/mtls"
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

	// Generate our mTLS keypair + CSR up front; the private key never leaves the box.
	// Harmless if the control plane isn't running mTLS — it just won't sign it.
	keyPEM, csrPEM, err := mtls.GenerateKeyAndCSR(mtls.DefaultCN)
	if err != nil {
		fmt.Fprintf(stderr, "could not generate client key: %v\n", err)
		return 1
	}

	osName, osVersion := osRelease()
	client := controlplane.New(cfg.Host)
	resp, err := client.Register(context.Background(), controlplane.RegisterRequest{
		PairingToken: *token,
		OSName:       osName,
		OSVersion:    osVersion,
		AgentVersion: version.Version,
		CSR:          string(csrPEM),
	})
	if err != nil {
		fmt.Fprintf(stderr, "pairing failed: %v\n", err)
		return 1
	}

	cred := credstore.Credential{
		Host:       cfg.Host,
		AgentToken: resp.AgentToken,
		ServerID:   resp.ServerID,
	}

	// If the control plane signed our CSR, persist the mTLS material and switch
	// steady-state traffic to the mTLS endpoint.
	if resp.ClientCertificate != "" {
		certPEM := resp.CertificateChain
		if certPEM == "" {
			certPEM = resp.ClientCertificate
		}
		if err := credstore.SaveCertMaterial(cfg.Home, []byte(certPEM), keyPEM, []byte(resp.CABundle)); err != nil {
			fmt.Fprintf(stderr, "could not save client certificate: %v\n", err)
			return 1
		}
		cred.AgentEndpoint = resp.AgentEndpoint
	}

	if err := credstore.Save(cfg.Home, cred); err != nil {
		fmt.Fprintf(stderr, "could not save credential: %v\n", err)
		return 1
	}

	if cred.AgentEndpoint != "" {
		fmt.Fprintf(stdout, "✓ Paired (server #%d) with mTLS. Endpoint: %s.\n", resp.ServerID, cred.AgentEndpoint)
	} else {
		fmt.Fprintf(stdout, "✓ Paired (server #%d). Credential saved to %s (0600).\n", resp.ServerID, credstore.Path(cfg.Home))
	}
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

	// Graceful shutdown on SIGTERM/SIGINT (systemd stop, Ctrl-C).
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	var client *controlplane.Client
	if cred.AgentEndpoint != "" && credstore.HasCertMaterial(cfg.Home) {
		// mTLS: present our client cert + pin the control-plane CA, and keep the cert
		// fresh in the background.
		certPEM, keyPEM, caPEM, lerr := credstore.LoadCertMaterial(cfg.Home)
		if lerr != nil {
			slog.Error("could not load mTLS material", "err", lerr)
			return 1
		}
		holder, herr := mtls.NewHolder(certPEM, keyPEM)
		if herr != nil {
			slog.Error("could not load client certificate", "err", herr)
			return 1
		}
		tlsCfg, terr := mtls.TLSConfig(holder, caPEM)
		if terr != nil {
			slog.Error("could not build mTLS config", "err", terr)
			return 1
		}
		cfg.Host = endpointURL(cred.AgentEndpoint)
		client = controlplane.NewWithTLS(cfg.Host, tlsCfg)
		client.SetToken(cred.AgentToken)
		go renewCertLoop(ctx, client, holder, cfg.Home)
	} else {
		// Bearer-only (dev/test, or a control plane not running mTLS).
		cfg.Host = cred.Host
		client = controlplane.New(cfg.Host)
		client.SetToken(cred.AgentToken)
	}

	sampler := health.New(cfg.AppRoot)
	ex := executor.New(cfg, sampler)

	if err := daemon.Run(ctx, cfg, client, ex, sampler); err != nil {
		slog.Error("agent stopped with error", "err", err)
		return 1
	}
	return 0
}

// endpointURL normalises a bare host[:port] (e.g. "agents.shipmono.dev:8443") to an
// https URL, leaving an already-qualified URL untouched.
func endpointURL(endpoint string) string {
	if strings.Contains(endpoint, "://") {
		return strings.TrimRight(endpoint, "/")
	}
	return "https://" + endpoint
}

// renewBefore is how long before expiry to renew. For the control plane's 7-day leaf TTL
// this lands renewal at ~2/3 of life, leaving ample slack if the box is briefly offline.
const renewBefore = 56 * time.Hour

// renewCertLoop periodically renews the client certificate before it expires, swapping the
// live cert in place (new TLS handshakes pick it up). Best-effort: a failed renewal is
// retried on the next tick, and the cert is still valid until renewBefore elapses.
func renewCertLoop(ctx context.Context, client *controlplane.Client, holder *mtls.Holder, home string) {
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			notAfter, err := holder.NotAfter()
			if err != nil {
				slog.Warn("cert renewal: cannot read expiry", "err", err)
				continue
			}
			if time.Until(notAfter) > renewBefore {
				continue
			}
			if err := renewOnce(ctx, client, holder, home); err != nil {
				slog.Warn("cert renewal failed; will retry", "err", err)
			} else {
				slog.Info("client certificate renewed")
			}
		}
	}
}

func renewOnce(ctx context.Context, client *controlplane.Client, holder *mtls.Holder, home string) error {
	keyPEM, csrPEM, err := mtls.GenerateKeyAndCSR(mtls.DefaultCN)
	if err != nil {
		return err
	}
	resp, err := client.RenewCertificate(ctx, string(csrPEM))
	if err != nil {
		return err
	}
	certPEM := resp.CertificateChain
	if certPEM == "" {
		certPEM = resp.ClientCertificate
	}
	if err := credstore.SaveCertMaterial(home, []byte(certPEM), keyPEM, []byte(resp.CABundle)); err != nil {
		return err
	}
	return holder.Set([]byte(certPEM), keyPEM)
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
