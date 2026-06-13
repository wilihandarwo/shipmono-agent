//go:build linux

package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wilihandarwo/shipmono-agent/internal/release"
)

func newLinuxFixture() *linuxExecutor {
	return &linuxExecutor{layout: release.Layout{AppRoot: "/srv/app"}}
}

func TestSwapCurrentIsAtomicAndUnprivileged(t *testing.T) {
	root := t.TempDir()
	e := &linuxExecutor{deps: Deps{AppRoot: root}, layout: release.Layout{AppRoot: root}}

	relA := e.layout.Release("20260101T000000")
	relB := e.layout.Release("20260102T000000")
	for _, d := range []string{relA, relB} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// First swap creates `current`.
	if err := e.swapCurrent(context.Background(), relA); err != nil {
		t.Fatalf("first swap: %v", err)
	}
	if target, _ := os.Readlink(e.layout.Current()); target != relA {
		t.Errorf("current -> %q, want %q", target, relA)
	}

	// Second swap atomically replaces the existing symlink.
	if err := e.swapCurrent(context.Background(), relB); err != nil {
		t.Fatalf("second swap: %v", err)
	}
	if target, _ := os.Readlink(e.layout.Current()); target != relB {
		t.Errorf("current -> %q, want %q", target, relB)
	}
	// No stray temp symlink left behind.
	if _, err := os.Lstat(filepath.Join(root, "current.tmp")); !os.IsNotExist(err) {
		t.Errorf("temp symlink should be gone, lstat err = %v", err)
	}
}

func TestValidateDomain(t *testing.T) {
	ok := []string{"example.com", "app.example.co.uk", "a-b.example.com"}
	bad := []string{"", "no spaces.com", "example.com; rm -rf /", "-bad.com", "x", "http://example.com"}
	for _, d := range ok {
		if err := validateDomain(d); err != nil {
			t.Errorf("validateDomain(%q) = %v, want nil", d, err)
		}
	}
	for _, d := range bad {
		if err := validateDomain(d); err == nil {
			t.Errorf("validateDomain(%q) = nil, want error", d)
		}
	}
}

func TestRenderCaddyfile(t *testing.T) {
	e := newLinuxFixture()
	empty := e.renderCaddyfile(nil)
	for _, want := range []string{"{\n\tfrankenphp\n}", ":80 {", "respond 404", "@haspublic file"} {
		if !strings.Contains(empty, want) {
			t.Errorf("empty-domain Caddyfile missing %q, got %q", want, empty)
		}
	}
	got := e.renderCaddyfile([]string{"a.com", "b.com"})
	// Flexible root: the repo top is the default, public/ is the override.
	if !strings.Contains(got, "a.com b.com {") ||
		!strings.Contains(got, "root * /srv/app/current\n") ||
		!strings.Contains(got, "root * /srv/app/current/public\n") {
		t.Errorf("Caddyfile = %q", got)
	}
}
