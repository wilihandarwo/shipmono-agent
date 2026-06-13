// Package gitops performs the deploy's git work, confined to a directory
// derived only from constants and the numeric repo id — never from the
// attacker-influenceable repo_full_name. The ephemeral GitHub installation
// token is passed to git through a GIT_ASKPASS shim (the agent binary itself,
// re-invoked in askpass mode) so it never appears in argv or on disk.
package gitops

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Env var names shared with the askpass shim in package cli.
const (
	EnvAskpass  = "SHIPMONO_ASKPASS"
	EnvGitToken = "SHIPMONO_GIT_TOKEN"
)

// Logger receives streamed progress lines.
type Logger func(line string)

// Repo is a confined git working tree for one app.
type Repo struct {
	dir     string // the jailed working directory
	askpass string // path to the agent binary, used as GIT_ASKPASS
	token   string // ephemeral installation token (in-memory only)
}

// Prepare validates the repo id and returns a Repo whose working directory is
// strictly under appRoot (jail-checked). repoFullName is used only to build the
// HTTPS remote and for log messages; it never influences the path.
func Prepare(appRoot string, repoID int, token string) (*Repo, error) {
	if repoID <= 0 {
		return nil, fmt.Errorf("invalid repo_id %d", repoID)
	}
	dir := filepath.Join(appRoot, "repo")
	clean := filepath.Clean(dir)
	if clean != dir || !strings.HasPrefix(clean+string(os.PathSeparator), filepath.Clean(appRoot)+string(os.PathSeparator)) {
		return nil, fmt.Errorf("refusing git work dir outside app root: %s", dir)
	}
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate agent binary for askpass: %w", err)
	}
	return &Repo{dir: dir, askpass: self, token: token}, nil
}

// Dir returns the working directory.
func (r *Repo) Dir() string { return r.dir }

// Sync fetches the given sha from the repo and checks it out detached, leaving
// the working tree at exactly that commit. git_ref is informational; the
// checkout is pinned to the sha and verified.
func (r *Repo) Sync(ctx context.Context, repoFullName, gitRef, gitSha string, log Logger) error {
	if gitSha == "" {
		return fmt.Errorf("deploy requires git_sha")
	}
	if err := os.MkdirAll(r.dir, 0o755); err != nil {
		return fmt.Errorf("create work dir: %w", err)
	}
	remote := fmt.Sprintf("https://x-access-token@github.com/%s.git", repoFullName)

	if _, err := os.Stat(filepath.Join(r.dir, ".git")); err != nil {
		if err := r.git(ctx, log, "init", "-q"); err != nil {
			return err
		}
	}
	// set-url is idempotent across re-deploys; add if missing.
	if err := r.git(ctx, nil, "remote", "set-url", "origin", remote); err != nil {
		if err := r.git(ctx, log, "remote", "add", "origin", remote); err != nil {
			return err
		}
	}

	log(fmt.Sprintf("Fetching %s @ %s", repoFullName, shortSHA(gitSha)))
	// Prefer fetching the exact sha (GitHub allows reachable-SHA fetches).
	// Fall back to fetching the ref if the server refuses a bare-sha fetch.
	if err := r.git(ctx, log, "fetch", "--depth", "1", "origin", gitSha); err != nil {
		if gitRef == "" {
			return err
		}
		log(fmt.Sprintf("Bare-sha fetch unavailable; fetching ref %s", gitRef))
		if err := r.git(ctx, log, "fetch", "origin", gitRef); err != nil {
			return err
		}
	}

	if err := r.git(ctx, log, "checkout", "--detach", "-f", gitSha); err != nil {
		return err
	}
	return r.verifyHEAD(ctx, gitSha)
}

// Export copies the checked-out tree (excluding .git) into dst, producing a
// clean release directory with no version-control metadata.
func (r *Repo) Export(dst string) error {
	return copyTree(r.dir, dst)
}

func (r *Repo) verifyHEAD(ctx context.Context, want string) error {
	out, err := r.output(ctx, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	got := strings.TrimSpace(out)
	if got != want {
		return fmt.Errorf("checkout mismatch: HEAD %s != requested %s", shortSHA(got), shortSHA(want))
	}
	return nil
}

// git runs a git subcommand in the jailed dir with a scrubbed environment and
// the askpass token plumbing. Combined output is streamed to log when non-nil.
func (r *Repo) git(ctx context.Context, log Logger, args ...string) error {
	cmd := r.command(ctx, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	if log != nil {
		for _, line := range nonEmptyLines(buf.String()) {
			log("  " + line)
		}
	}
	if err != nil {
		return fmt.Errorf("git %s: %w", args[0], err)
	}
	return nil
}

func (r *Repo) output(ctx context.Context, args ...string) (string, error) {
	cmd := r.command(ctx, args...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", args[0], err)
	}
	return string(out), nil
}

// command builds an exec.Cmd for git with cmd.Dir jailed and a minimal,
// explicit environment. The token reaches git only via the askpass child's
// environment — never argv, never disk.
func (r *Repo) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = r.dir
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + r.dir,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_ASKPASS=" + r.askpass,
		EnvAskpass + "=1",
		EnvGitToken + "=" + r.token,
	}
	return cmd
}

func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if t := strings.TrimRight(l, "\r"); strings.TrimSpace(t) != "" {
			out = append(out, t)
		}
	}
	return out
}

// copyTree recursively copies src into dst, skipping the .git directory and
// preserving file modes and symlinks.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, 0o755)
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		target := filepath.Join(dst, rel)

		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(target, info.Mode().Perm())
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		default:
			return copyFile(path, target, info.Mode().Perm())
		}
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := out.ReadFrom(in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
