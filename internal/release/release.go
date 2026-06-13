// Package release builds the deploy layout paths and release identifiers used
// by the executors. A release id is a UTC timestamp (YYYYMMDDTHHMMSS) — the same
// format the Ruby simulator emits and the control plane stores as release_id.
package release

import (
	"path/filepath"
	"regexp"
	"time"
)

// IDLayout is the release-id / directory timestamp format.
const IDLayout = "20060102T150405"

// idPattern validates a release id before it is used to build a path, so a
// malformed release_id from a rollback command can never escape the releases
// tree.
var idPattern = regexp.MustCompile(`^\d{8}T\d{6}$`)

// NewID returns a fresh release id from the current UTC time.
func NewID() string { return time.Now().UTC().Format(IDLayout) }

// ValidID reports whether s is a well-formed release id.
func ValidID(s string) bool { return idPattern.MatchString(s) }

// ShortSHA truncates a git sha for display, falling back to "HEAD".
func ShortSHA(sha string) string {
	if sha == "" {
		return "HEAD"
	}
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// Layout resolves the standard paths under an app root.
type Layout struct{ AppRoot string }

func (l Layout) ReleasesDir() string      { return filepath.Join(l.AppRoot, "releases") }
func (l Layout) SharedDir() string        { return filepath.Join(l.AppRoot, "shared") }
func (l Layout) SharedDB() string         { return filepath.Join(l.SharedDir(), "app.db") }
func (l Layout) Current() string          { return filepath.Join(l.AppRoot, "current") }
func (l Layout) Release(id string) string { return filepath.Join(l.ReleasesDir(), id) }
