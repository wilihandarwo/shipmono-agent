//go:build linux

package executor

import (
	"strings"
	"testing"

	"github.com/wilihandarwo/shipmono-agent/internal/release"
)

func newLinuxFixture() *linuxExecutor {
	return &linuxExecutor{layout: release.Layout{AppRoot: "/srv/app"}}
}

func TestAssertSudoAllowed(t *testing.T) {
	e := newLinuxFixture()

	allowed := [][]string{
		{"/usr/bin/ln", "-sfn", "/srv/app/releases/20260613T010203", "/srv/app/current"},
		{"/usr/bin/systemctl", "reload", "shipmono-frankenphp.service"},
		{"/usr/bin/systemctl", "restart", "shipmono-frankenphp.service"},
	}
	for _, args := range allowed {
		if err := e.assertSudoAllowed(args); err != nil {
			t.Errorf("assertSudoAllowed(%v) = %v, want nil", args, err)
		}
	}

	refused := [][]string{
		// ln target escapes the releases tree.
		{"/usr/bin/ln", "-sfn", "/etc/passwd", "/srv/app/current"},
		// ln target is a traversal that resolves outside releases.
		{"/usr/bin/ln", "-sfn", "/srv/app/releases/../../etc", "/srv/app/current"},
		// wrong link destination.
		{"/usr/bin/ln", "-sfn", "/srv/app/releases/x", "/srv/app/evil"},
		// arbitrary systemctl unit.
		{"/usr/bin/systemctl", "reload", "sshd.service"},
		// disallowed systemctl verb.
		{"/usr/bin/systemctl", "stop", "shipmono-frankenphp.service"},
		// not in the allowlist at all.
		{"/bin/sh", "-c", "rm -rf /"},
		{"/usr/bin/rm", "-rf", "/srv/app"},
	}
	for _, args := range refused {
		if err := e.assertSudoAllowed(args); err == nil {
			t.Errorf("assertSudoAllowed(%v) = nil, want refusal", args)
		}
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
