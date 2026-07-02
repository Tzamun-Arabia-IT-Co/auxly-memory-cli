package mcp

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
)

// supersedeThreshold is the cosine floor above which an existing fact in the
// TARGET file counts as "the same fact, restated" — the new write then
// REPLACES it (with a dated "was:" trace) instead of accumulating a
// contradiction next to it.
const supersedeThreshold = 0.75

// maybeSupersede rewrites a plain-append diff into a REPLACE diff when an
// added fact contradicts (≈duplicates with different content) a fact already
// in the target file. The old fact is never destroyed without trace — it is
// carried in the new bullet's "was:" note, and the whole transformation rides
// the normal trust pipeline (require_approval agents see the REPLACE in
// pending; auto trust applies it and the audit diff records old→new).
//
// Graceful by construction: embeddings down, recall error, AUXLY_SUPERSEDE=off,
// or no similar fact → the diff is returned untouched (plain append).
func (s *Server) maybeSupersede(file, diff string) string {
	if os.Getenv("AUXLY_SUPERSEDE") == "off" {
		return diff
	}
	emb := s.newEmbedder()
	if emb == nil || !emb.Enabled() {
		return diff
	}

	lines := strings.Split(diff, "\n")
	// The MCP request loop is synchronous — every embed round-trip here stalls
	// ALL of this agent's tool calls. Single-fact syncs (the contradiction
	// case) get checked; bulk imports skip, and a slow endpoint can never hold
	// a write hostage for more than the overall budget.
	added := 0
	for _, dl := range lines {
		if strings.HasPrefix(dl, "+") && !strings.HasPrefix(dl, "+++") && strings.TrimSpace(strings.TrimPrefix(dl, "+")) != "" {
			added++
		}
	}
	if added == 0 || added > 3 {
		return diff
	}
	deadline := time.Now().Add(6 * time.Second)

	out := make([]string, 0, len(lines)+4)
	for _, dl := range lines {
		if !strings.HasPrefix(dl, "+") || strings.HasPrefix(dl, "+++") {
			out = append(out, dl)
			continue
		}
		fact := strings.TrimSpace(strings.TrimPrefix(dl, "+"))
		if fact == "" {
			out = append(out, dl)
			continue
		}

		if time.Now().After(deadline) {
			out = append(out, dl)
			continue // overall budget spent — remaining lines append plainly
		}
		ctx, cancel := context.WithTimeout(memory.WithoutRecallObserver(context.Background()), 3*time.Second)
		hits, err := s.store.Recall(ctx, fact, 3, emb, func(f string) bool { return f == file })
		cancel()
		if err != nil {
			out = append(out, dl)
			continue
		}

		old := ""
		for _, h := range hits {
			if h.Score < supersedeThreshold {
				continue
			}
			if cand := closestLine(h.Text, fact); cand != "" && strings.TrimSpace(cand) != fact {
				old = cand
				break
			}
		}
		if old == "" {
			out = append(out, dl)
			continue
		}

		// REPLACE: delete the stale bullet, append the new one carrying the
		// dated trace of what it superseded.
		out = append(out, "- "+old)
		wasNote := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(old), "-•* "))
		out = append(out, fmt.Sprintf("+%s (updated %s; was: %s)",
			strings.TrimSuffix(strings.TrimPrefix(dl, "+"), "\n"),
			time.Now().Format("2006-01-02"), wasNote))
	}
	return strings.Join(out, "\n")
}

// closestLine picks the single line of a recalled chunk that best matches the
// new fact by word overlap (Dice over word sets) — chunks can span several
// bullets and only ONE of them is being superseded. Returns "" when nothing
// overlaps meaningfully (the chunk matched on theme, not on fact).
func closestLine(chunk, fact string) string {
	fw := wordSet(fact)
	best, bestScore := "", 0.0
	for _, l := range strings.Split(chunk, "\n") {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		lw := wordSet(t)
		if len(lw) == 0 {
			continue
		}
		inter := 0
		for w := range lw {
			if fw[w] {
				inter++
			}
		}
		score := 2 * float64(inter) / float64(len(lw)+len(fw))
		if score > bestScore {
			best, bestScore = t, score
		}
	}
	if bestScore < 0.5 {
		return ""
	}
	return best
}

func wordSet(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.Fields(strings.ToLower(s)) {
		w = strings.Trim(w, ".,;:!?()[]\"'`-•*")
		if len(w) > 1 {
			out[w] = true
		}
	}
	return out
}
