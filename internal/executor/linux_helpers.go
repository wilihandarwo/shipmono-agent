//go:build linux

package executor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// maxLintFiles caps how many .php files a single deploy lints, so a giant repo
// can't make a deploy run unbounded. Streamed as a note if exceeded.
const maxLintFiles = 500

// phpLint runs `php -l` over the release's PHP files. If no `php` binary is on
// PATH (FrankenPHP bundles its own runtime), the check is skipped with a note
// rather than failing the deploy.
func (e *linuxExecutor) phpLint(ctx context.Context, releaseDir string, sink EventSink) error {
	php, err := exec.LookPath("php")
	if err != nil {
		sink.Log("php CLI not found — skipping syntax check")
		return nil
	}

	var files []string
	walkErr := filepath.WalkDir(releaseDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.EqualFold(filepath.Ext(path), ".php") {
			files = append(files, path)
			if len(files) > maxLintFiles {
				return filepath.SkipAll
			}
		}
		return nil
	})
	if walkErr != nil {
		return fmt.Errorf("scan php files: %w", walkErr)
	}
	if len(files) == 0 {
		sink.Log("No .php files to lint")
		return nil
	}
	sort.Strings(files)
	if len(files) > maxLintFiles {
		sink.Log(fmt.Sprintf("Linting first %d .php files (repo has more)", maxLintFiles))
		files = files[:maxLintFiles]
	}

	for _, f := range files {
		out, err := exec.CommandContext(ctx, php, "-l", f).CombinedOutput()
		if err != nil {
			rel, _ := filepath.Rel(releaseDir, f)
			sink.Log(fmt.Sprintf("php -l %s — FAILED", rel))
			return fmt.Errorf("php -l failed: %s", strings.TrimSpace(lastLine(string(out))))
		}
	}
	sink.Log(fmt.Sprintf("php -l — %d file(s), no syntax errors", len(files)))
	return nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

// domainListPath / caddyfilePath live in shared/ so they survive deploys.
func (e *linuxExecutor) domainListPath() string {
	return filepath.Join(e.layout.SharedDir(), "domains.list")
}

func (e *linuxExecutor) caddyfilePath() string {
	return filepath.Join(e.layout.SharedDir(), "Caddyfile")
}

// mutateDomains applies mutate to the current domain set, then rewrites both
// domains.list and the generated Caddyfile atomically.
func (e *linuxExecutor) mutateDomains(mutate func(set map[string]struct{})) error {
	if err := os.MkdirAll(e.layout.SharedDir(), 0o755); err != nil {
		return err
	}
	set := map[string]struct{}{}
	for _, d := range e.readDomains() {
		set[d] = struct{}{}
	}
	mutate(set)

	domains := make([]string, 0, len(set))
	for d := range set {
		domains = append(domains, d)
	}
	sort.Strings(domains)

	if err := writeFileAtomic(e.domainListPath(), []byte(strings.Join(domains, "\n")+"\n"), 0o644); err != nil {
		return fmt.Errorf("write domains.list: %w", err)
	}
	if err := writeFileAtomic(e.caddyfilePath(), []byte(e.renderCaddyfile(domains)), 0o644); err != nil {
		return fmt.Errorf("write Caddyfile: %w", err)
	}
	return nil
}

func (e *linuxExecutor) readDomains() []string {
	raw, err := os.ReadFile(e.domainListPath())
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(string(raw), "\n") {
		if d := strings.TrimSpace(l); d != "" {
			out = append(out, d)
		}
	}
	return out
}

// renderCaddyfile builds a FrankenPHP/Caddy site config serving the current
// release's public root for the configured domains (automatic HTTPS). With no
// domains it serves plain HTTP on :80 so the box still responds.
func (e *linuxExecutor) renderCaddyfile(domains []string) string {
	root := filepath.Join(e.layout.Current(), "public")
	var b strings.Builder
	if len(domains) == 0 {
		fmt.Fprintf(&b, ":80 {\n\troot * %s\n\tphp_server\n}\n", root)
		return b.String()
	}
	fmt.Fprintf(&b, "%s {\n\troot * %s\n\tphp_server\n}\n", strings.Join(domains, " "), root)
	return b.String()
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// validateDomain rejects anything that isn't a plausible hostname, so a domain
// string can never smuggle shell metacharacters or whitespace into the config.
var domainRE = regexp.MustCompile(`^(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,63}$`)

func validateDomain(d string) error {
	if d == "" {
		return fmt.Errorf("empty domain")
	}
	if len(d) > 253 || !domainRE.MatchString(d) {
		return fmt.Errorf("invalid domain %q", d)
	}
	return nil
}
