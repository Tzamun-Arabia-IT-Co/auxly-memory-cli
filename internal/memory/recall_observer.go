package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// RecallEvent describes one completed recall, emitted to Store.RecallObserver
// (set by the MCP server, which forwards it to the audit layer). The memory
// package stays audit-free; this is the only coupling surface.
//
// PRIVACY: the query is carried as a hash ONLY. Recall queries can contain
// secrets (the personal-leak regression proved it) — raw query text must never
// reach a log or database.
type RecallEvent struct {
	QueryHash string // HashRecallText(query)
	Fallback  bool   // substring-fallback path, no embeddings involved
	Hits      []RecallEventHit
}

// RecallEventHit is one scored chunk in a recall result. LineHash identifies
// the FACT (chunk text), stable across re-indexing and line moves — file+line
// numbers churn, content doesn't.
type RecallEventHit struct {
	File     string
	LineHash string // HashRecallText(chunk text)
	Score    float32
	Rank     int  // 0-based rank after recency-adjusted sort
	Accepted bool // survived the top-k cut (false = scored but trimmed)
}

// HashRecallText is the shared short content hash for queries and chunk text:
// sha256 truncated to 16 hex chars — plenty for identity, useless for recovery.
func HashRecallText(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:8])
}

// recallObserverSuppressKey marks a context whose Recall must NOT emit an
// event: internal recalls (supersede's duplicate check, the TUI playground)
// would otherwise masquerade as agent usage and corrupt the analytics.
type recallObserverSuppressKey struct{}

// WithoutRecallObserver returns a ctx whose Recall skips event emission.
func WithoutRecallObserver(ctx context.Context) context.Context {
	return context.WithValue(ctx, recallObserverSuppressKey{}, true)
}

func recallObserverSuppressed(ctx context.Context) bool {
	v, _ := ctx.Value(recallObserverSuppressKey{}).(bool)
	return v
}

// bulletHashes returns one hash per bullet line of a chunk — fact-granularity
// identity for the decay/review features (a chunk hash would churn whenever
// ANY sibling bullet changes). Chunks without bullets fall back to one hash of
// the whole text.
func bulletHashes(chunkText string) []string {
	var out []string
	for _, line := range strings.Split(chunkText, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "- ") {
			out = append(out, HashRecallText(strings.TrimSpace(line)))
		}
	}
	if len(out) == 0 {
		out = append(out, HashRecallText(chunkText))
	}
	return out
}
