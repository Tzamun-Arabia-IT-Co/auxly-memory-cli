package memory

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Optimization 1 — dirty-file skip: the dominant cost of organize is the
// model call itself (a cold CLI-agent spawn + a full-vault rewrite can take
// minutes), and most real-world runs touch only a handful of files since the
// last organize. This ledger records, per file, the sha256 of its content AT
// LAST SUCCESSFUL ORGANIZE, so gatherOrganizeFilesOpts can skip anything
// unchanged since then — no model call at all when nothing is dirty. Mirrors
// decay.go's reviewSeenPath/loadReviewSeen/saveReviewSeen ledger pattern.

// organizeSeenPath is the dirty-file ledger for organize: file name → sha256
// hex of its content as of the last successful organize apply.
func organizeSeenPath(root string) string {
	return filepath.Join(root, ".index", "organize-seen.json")
}

// loadOrganizeSeen reads the ledger, or an empty map if it doesn't exist yet
// or is unreadable/corrupt — the caller then treats every file as never-seen
// (dirty), which is the safe, self-bootstrapping behavior: worst case is a
// wasted model call, never a silently skipped file.
func loadOrganizeSeen(root string) map[string]string {
	data, err := os.ReadFile(organizeSeenPath(root))
	if err != nil {
		return map[string]string{}
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil || m == nil {
		return map[string]string{}
	}
	return m
}

// saveOrganizeSeen persists the ledger. Best-effort — called under LockVault
// by ApplyOrganizeChanges; a write failure just means the next run treats
// this run's files as never-seen again (a wasted model call, not data loss).
func saveOrganizeSeen(root string, seen map[string]string) {
	data, err := json.Marshal(seen)
	if err != nil {
		return
	}
	_ = AtomicWriteFile(organizeSeenPath(root), data, 0644)
}
