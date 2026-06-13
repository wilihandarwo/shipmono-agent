//go:build linux

package executor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/wilihandarwo/shipmono-agent/internal/config"
	"github.com/wilihandarwo/shipmono-agent/internal/gitops"
	"github.com/wilihandarwo/shipmono-agent/internal/release"
)

// linuxExecutor performs the real host operations. Elevated steps go through
// runSudo, which refuses any command that is not one of the three forms the
// install.sh sudoers drop-in permits — a defense-in-depth mirror of the host
// policy inside the agent itself.
type linuxExecutor struct {
	deps   Deps
	layout release.Layout
}

func newLinuxExecutor(deps Deps) Executor {
	return &linuxExecutor{deps: deps, layout: release.Layout{AppRoot: deps.AppRoot}}
}

func (e *linuxExecutor) Mode() Mode { return ModeLinux }

// ── deploy ───────────────────────────────────────────────────────────────────

func (e *linuxExecutor) Deploy(ctx context.Context, p DeployParams, gitToken string, sink EventSink) Result {
	if err := e.checkRepoAllowlist(p.RepoID); err != nil {
		return failed(err)
	}

	repo, err := gitops.Prepare(e.deps.AppRoot, p.RepoID, gitToken)
	if err != nil {
		return failed(err)
	}
	log := func(line string) { sink.Log(line) }

	tokenState := "missing"
	if gitToken != "" {
		tokenState = "present"
	}
	sink.Log(fmt.Sprintf("Cloning %s @ %s (token %s)…", p.RepoFullName, release.ShortSHA(p.GitSha), tokenState))
	if err := repo.Sync(ctx, p.RepoFullName, p.GitRef, p.GitSha, log); err != nil {
		return failed(err)
	}

	rel := release.NewID()
	releaseDir := e.layout.Release(rel)
	sink.Log(fmt.Sprintf("Building release releases/%s", rel))
	if err := os.MkdirAll(e.layout.ReleasesDir(), 0o755); err != nil {
		return failed(fmt.Errorf("create releases dir: %w", err))
	}
	if err := repo.Export(releaseDir); err != nil {
		return failed(fmt.Errorf("export release: %w", err))
	}

	if err := e.phpLint(ctx, releaseDir, sink); err != nil {
		// Lint failed — abort before swapping. Leave the half-built release
		// dir for inspection; it is never made current.
		return failed(err)
	}

	sink.Log("Symlinking shared/app.db into the release")
	if err := e.linkSharedDB(releaseDir); err != nil {
		return failed(err)
	}

	// Capture the release we are replacing so a failed reload can roll back.
	previous := e.currentTarget()

	sink.Log(fmt.Sprintf("Atomic symlink swap: current → releases/%s", rel))
	if err := e.swapCurrent(ctx, releaseDir); err != nil {
		return failed(err)
	}

	sink.Log("FrankenPHP graceful reload — zero downtime")
	if err := e.reloadFrankenphp(ctx); err != nil {
		if previous != "" {
			sink.Log("Reload failed — rolling back to the previous release")
			if rbErr := e.swapCurrent(ctx, previous); rbErr == nil {
				_ = e.reloadFrankenphp(ctx)
			}
		}
		return failed(fmt.Errorf("frankenphp reload: %w", err))
	}

	return okRelease(rel)
}

// ── rollback / reload ────────────────────────────────────────────────────────

func (e *linuxExecutor) Rollback(ctx context.Context, p RollbackParams, sink EventSink) Result {
	if !release.ValidID(p.ReleaseID) {
		return failed(fmt.Errorf("invalid release_id %q", p.ReleaseID))
	}
	releaseDir := e.layout.Release(p.ReleaseID)
	if fi, err := os.Stat(releaseDir); err != nil || !fi.IsDir() {
		return failed(fmt.Errorf("release %s does not exist", p.ReleaseID))
	}
	sink.Log(fmt.Sprintf("Symlink swap: current → releases/%s", p.ReleaseID))
	if err := e.swapCurrent(ctx, releaseDir); err != nil {
		return failed(err)
	}
	sink.Log("FrankenPHP graceful reload")
	if err := e.reloadFrankenphp(ctx); err != nil {
		return failed(fmt.Errorf("frankenphp reload: %w", err))
	}
	return okRelease(p.ReleaseID)
}

func (e *linuxExecutor) Reload(ctx context.Context, sink EventSink) Result {
	sink.Log("FrankenPHP graceful reload")
	if err := e.reloadFrankenphp(ctx); err != nil {
		return failed(fmt.Errorf("frankenphp reload: %w", err))
	}
	return ok()
}

// ── restore ──────────────────────────────────────────────────────────────────

func (e *linuxExecutor) Restore(ctx context.Context, p RestoreParams, sink EventSink) Result {
	if _, err := exec.LookPath("litestream"); err != nil {
		return failed(fmt.Errorf("litestream not installed; restore needs Litestream configured (production-readiness 3.1)"))
	}
	db := e.layout.SharedDB()
	sink.Log(fmt.Sprintf("Litestream restore of %s to %s…", db, p.PointInTime))
	args := []string{"restore", "-if-replica-exists"}
	if p.PointInTime != "" {
		args = append(args, "-timestamp", p.PointInTime)
	}
	args = append(args, db)
	if out, err := exec.CommandContext(ctx, "litestream", args...).CombinedOutput(); err != nil {
		return failed(fmt.Errorf("litestream restore: %v: %s", err, strings.TrimSpace(string(out))))
	}
	return ok()
}

// ── domains ──────────────────────────────────────────────────────────────────
//
// Domains are tracked in shared/domains.list; on each change the agent
// regenerates shared/Caddyfile from that list and reloads FrankenPHP, which
// provisions a certificate via ACME on the first request to a new host. The
// frankenphp systemd unit is expected to load shared/Caddyfile (host
// provisioning follow-up; see README).

func (e *linuxExecutor) AddDomain(ctx context.Context, p DomainParams, sink EventSink) Result {
	if err := validateDomain(p.Domain); err != nil {
		return failed(err)
	}
	sink.Log(fmt.Sprintf("Adding %s to FrankenPHP config", p.Domain))
	if err := e.mutateDomains(func(set map[string]struct{}) { set[p.Domain] = struct{}{} }); err != nil {
		return failed(err)
	}
	sink.Log("Reloading — certificate will be provisioned on first request (ACME)")
	if err := e.reloadFrankenphp(ctx); err != nil {
		return failed(fmt.Errorf("frankenphp reload: %w", err))
	}
	return ok()
}

func (e *linuxExecutor) RemoveDomain(ctx context.Context, p DomainParams, sink EventSink) Result {
	if err := validateDomain(p.Domain); err != nil {
		return failed(err)
	}
	sink.Log(fmt.Sprintf("Removing %s from FrankenPHP config", p.Domain))
	if err := e.mutateDomains(func(set map[string]struct{}) { delete(set, p.Domain) }); err != nil {
		return failed(err)
	}
	if err := e.reloadFrankenphp(ctx); err != nil {
		return failed(fmt.Errorf("frankenphp reload: %w", err))
	}
	return ok()
}

// ── status ───────────────────────────────────────────────────────────────────

func (e *linuxExecutor) Status(ctx context.Context, sink EventSink) Result {
	return okHealth(e.deps.Sampler.Sample(ctx))
}

// ── host primitives ──────────────────────────────────────────────────────────

// checkRepoAllowlist binds the server to the first repo it deploys. A later
// command for a different numeric repo id is refused — a compromised control
// plane cannot repoint the box at an arbitrary repository.
func (e *linuxExecutor) checkRepoAllowlist(repoID int) error {
	if repoID <= 0 {
		return fmt.Errorf("invalid repo_id %d", repoID)
	}
	path := filepath.Join(e.layout.SharedDir(), ".repo_id")
	raw, err := os.ReadFile(path)
	if err == nil {
		bound := strings.TrimSpace(string(raw))
		if bound != "" && bound != fmt.Sprint(repoID) {
			return fmt.Errorf("server is bound to repo id %s; refusing deploy for repo id %d", bound, repoID)
		}
		return nil
	}
	if err := os.MkdirAll(e.layout.SharedDir(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(fmt.Sprint(repoID)), 0o644)
}

func (e *linuxExecutor) linkSharedDB(releaseDir string) error {
	if err := os.MkdirAll(e.layout.SharedDir(), 0o755); err != nil {
		return fmt.Errorf("create shared dir: %w", err)
	}
	link := filepath.Join(releaseDir, "app.db")
	_ = os.Remove(link)
	if err := os.Symlink(e.layout.SharedDB(), link); err != nil {
		return fmt.Errorf("symlink shared/app.db: %w", err)
	}
	return nil
}

// currentTarget returns the release dir that `current` points at, or "" if the
// symlink does not exist yet (first deploy).
func (e *linuxExecutor) currentTarget() string {
	target, err := os.Readlink(e.layout.Current())
	if err != nil {
		return ""
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(e.deps.AppRoot, target)
	}
	return target
}

// swapCurrent atomically repoints current → releaseDir via the one sudo ln
// form the host policy allows.
func (e *linuxExecutor) swapCurrent(ctx context.Context, releaseDir string) error {
	return e.runSudo(ctx, "/usr/bin/ln", "-sfn", releaseDir, e.layout.Current())
}

func (e *linuxExecutor) reloadFrankenphp(ctx context.Context) error {
	return e.runSudo(ctx, "/usr/bin/systemctl", "reload", config.FrankenPHPUnit)
}

// runSudo runs an elevated command, but only after asserting it is one of the
// exact forms the sudoers drop-in permits. Anything else is refused locally,
// before sudo is ever invoked.
func (e *linuxExecutor) runSudo(ctx context.Context, args ...string) error {
	if err := e.assertSudoAllowed(args); err != nil {
		return err
	}
	out, err := exec.CommandContext(ctx, "sudo", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("sudo %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// assertSudoAllowed encodes the three permitted sudo forms from install.sh:
//
//	/usr/bin/ln -sfn <releases/*> <appRoot>/current
//	/usr/bin/systemctl reload  shipmono-frankenphp.service
//	/usr/bin/systemctl restart shipmono-frankenphp.service
func (e *linuxExecutor) assertSudoAllowed(args []string) error {
	current := e.layout.Current()
	releasesPrefix := e.layout.ReleasesDir() + string(os.PathSeparator)

	switch {
	case len(args) == 4 && args[0] == "/usr/bin/ln" && args[1] == "-sfn" && args[3] == current:
		target := filepath.Clean(args[2])
		if !strings.HasPrefix(target, releasesPrefix) {
			return fmt.Errorf("refused sudo ln: target %q not under %q", args[2], releasesPrefix)
		}
		return nil
	case len(args) == 3 && args[0] == "/usr/bin/systemctl" &&
		(args[1] == "reload" || args[1] == "restart") && args[2] == config.FrankenPHPUnit:
		return nil
	default:
		return fmt.Errorf("refused sudo: %q is not an allowed elevated command", strings.Join(args, " "))
	}
}
