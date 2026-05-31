package usage

import (
	"context"
	"os"
	"path/filepath"
)

// cursorFetcher is a deliberate special case. Cursor exposes NO session/week
// quota endpoint like the others — the only local signal is an AI-authored-code
// share in ~/.cursor/.../ai-code-tracking.db, which is a different metric (not a
// limit) and would need its own schema reverse-engineering pass.
//
// For now the card shows "—" and the popup explains Cursor has no usage limit to
// report. The presence check below keeps the report honest: it confirms Cursor
// is installed before claiming "not applicable" vs "not installed". When the
// ai-code-tracking.db reader lands, this returns an informational Window with
// IsLimit:false labeled "AI code".
type cursorFetcher struct{}

func (cursorFetcher) provider() string { return "cursor" }

func (cursorFetcher) fetch(_ context.Context) Report {
	r := Report{Provider: "cursor", Source: "local"}
	if !cursorInstalled() {
		r.Err = "Cursor not detected"
		return r
	}
	r.Err = "no usage limit exposed by Cursor"
	return r
}

func cursorInstalled() bool {
	home := homeDir()
	for _, p := range []string{
		filepath.Join(home, ".cursor"),
		filepath.Join(home, "Library", "Application Support", "Cursor"),
	} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}
