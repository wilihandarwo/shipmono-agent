package release

import "testing"

func TestValidID(t *testing.T) {
	valid := []string{"20260613T010203", "19990101T000000"}
	invalid := []string{"", "2026-06-13", "20260613T0102", "../../etc", "20260613t010203", "20260613T01020399"}
	for _, s := range valid {
		if !ValidID(s) {
			t.Errorf("ValidID(%q) = false, want true", s)
		}
	}
	for _, s := range invalid {
		if ValidID(s) {
			t.Errorf("ValidID(%q) = true, want false", s)
		}
	}
}

func TestNewIDIsValid(t *testing.T) {
	if id := NewID(); !ValidID(id) {
		t.Errorf("NewID() = %q is not a valid release id", id)
	}
}

func TestShortSHA(t *testing.T) {
	cases := map[string]string{"": "HEAD", "abc": "abc", "abcdef1234567890": "abcdef1"}
	for in, want := range cases {
		if got := ShortSHA(in); got != want {
			t.Errorf("ShortSHA(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLayoutPaths(t *testing.T) {
	l := Layout{AppRoot: "/srv/app"}
	checks := map[string]string{
		l.ReleasesDir():              "/srv/app/releases",
		l.SharedDir():                "/srv/app/shared",
		l.SharedDB():                 "/srv/app/shared/app.db",
		l.Current():                  "/srv/app/current",
		l.Release("20260613T010203"): "/srv/app/releases/20260613T010203",
	}
	for got, want := range checks {
		if got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
	}
}
