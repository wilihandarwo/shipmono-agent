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

// swapCurrent atomically repoints current → releaseDir. The agent owns the app
// root (install.sh chowns /srv/app to the agent user), so this needs no
// privilege: write a temp symlink in the same directory, then rename it over
// `current` — rename(2) is atomic on the same filesystem.
func (e *linuxExecutor) swapCurrent(_ context.Context, releaseDir string) error {
	current := e.layout.Current()
	tmp := current + ".tmp"
	if err := os.Remove(tmp); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clear temp symlink: %w", err)
	}
	if err := os.Symlink(releaseDir, tmp); err != nil {
		return fmt.Errorf("create symlink: %w", err)
	}
	if err := os.Rename(tmp, current); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("swap current symlink: %w", err)
	}
	return nil
}

// reloadFrankenphp gracefully reloads the running FrankenPHP server through its
// localhost admin API (`frankenphp reload`), which needs no privilege — no sudo,
// no systemctl. --force re-applies even when the Caddyfile is unchanged (a
// symlink-swap deploy), so the new release's code is picked up.
func (e *linuxExecutor) reloadFrankenphp(ctx context.Context) error {
	out, err := exec.CommandContext(ctx, config.FrankenPHPBin,
		"reload", "--config", e.caddyfilePath(), "--adapter", "caddyfile", "--force").CombinedOutput()
	if err != nil {
		return fmt.Errorf("frankenphp reload: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
