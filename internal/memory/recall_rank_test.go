package memory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/embed"
)

// rankStubEmbedder maps exact texts to fixed vectors so cosine scores are
// fully controlled by the test. Unknown texts get an orthogonal filler vector.
type rankStubEmbedder struct {
	vecs map[string][]float32
}

func (e rankStubEmbedder) Enabled() bool    { return true }
func (e rankStubEmbedder) Model() string    { return "rank-stub" }
func (e rankStubEmbedder) Provider() string { return "rank-stub" }
func (e rankStubEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		if v, ok := e.vecs[t]; ok {
			out[i] = v
		} else {
			out[i] = []float32{0, 0, 0, 1}
		}
	}
	return out, nil
}

// TestRecallThresholdFiltersNoise locks the relevance floor: chunks scoring
// below AUXLY_RECALL_MIN_SCORE never surface, and when EVERYTHING is below the
// floor Recall reports embed.ErrUnavailable so callers fall back to substring
// search instead of serving top-k noise.
func TestRecallThresholdFiltersNoise(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AUXLY_RECALL_MIN_SCORE", "0.5")
	writeFile(t, root, "hit.md", "# H\n\n- strong match line.\n")
	writeFile(t, root, "noise.md", "# N\n\n- weak noise line.\n")
	s := &Store{Root: root}
	emb := rankStubEmbedder{vecs: map[string][]float32{
		"query":                {1, 0, 0, 0},
		"- strong match line.": {1, 0.1, 0, 0}, // cosine ≈ 0.995
		"- weak noise line.":   {0.1, 1, 0, 0}, // cosine ≈ 0.0995 — below floor
	}}

	hits, err := s.Recall(context.Background(), "query", 8, emb, nil)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	for _, h := range hits {
		if h.File == "noise.md" {
			t.Fatalf("below-threshold chunk surfaced: %+v", h)
		}
	}
	if len(hits) == 0 {
		t.Fatalf("strong match was filtered out")
	}

	// Query orthogonal to everything → all below floor → ErrUnavailable fallback.
	emb.vecs["unrelated"] = []float32{0, 0, 1, 0}
	if _, err := s.Recall(context.Background(), "unrelated", 8, emb, nil); !errors.Is(err, embed.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable when all hits below threshold, got %v", err)
	}
}

// TestRecallRecencyNudgesNewerFile locks the recency boost: two chunks with
// EQUAL cosine relevance rank newer-file-first, but the boost can never lift a
// clearly-less-relevant chunk above a clearly-more-relevant one (×1.2 max).
func TestRecallRecencyNudgesNewerFile(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AUXLY_RECALL_MIN_SCORE", "0")
	writeFile(t, root, "old.md", "# O\n\n- same relevance line A.\n")
	writeFile(t, root, "new.md", "# N\n\n- same relevance line B.\n")
	writeFile(t, root, "best.md", "# B\n\n- clearly better line.\n")
	old := time.Now().Add(-60 * 24 * time.Hour)
	if err := os.Chtimes(filepath.Join(root, "old.md"), old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	s := &Store{Root: root}
	emb := rankStubEmbedder{vecs: map[string][]float32{
		"query":                    {1, 0, 0, 0},
		"- same relevance line A.": {1, 1, 0, 0},   // cosine ≈ 0.707
		"- same relevance line B.": {1, 0, 1, 0},   // cosine ≈ 0.707
		"- clearly better line.":   {1, 0.1, 0, 0}, // cosine ≈ 0.995
	}}

	hits, err := s.Recall(context.Background(), "query", 8, emb, nil)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(hits) != 3 {
		t.Fatalf("want 3 hits, got %d", len(hits))
	}
	if hits[0].File != "best.md" {
		t.Fatalf("recency boost outranked relevance: first hit %s", hits[0].File)
	}
	if hits[1].File != "new.md" || hits[2].File != "old.md" {
		t.Fatalf("equal-relevance tie not broken by recency: %s then %s", hits[1].File, hits[2].File)
	}
}

// TestRecallReusesIndexHandleAndSkipsRefresh locks the fast path: the index
// handle survives across recalls, the vault signature is recorded, and an
// unchanged vault leaves it untouched (⇒ the refresh pass was skipped). A
// vault write invalidates the signature so the next recall re-indexes.
func TestRecallReusesIndexHandleAndSkipsRefresh(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AUXLY_RECALL_MIN_SCORE", "0")
	writeFile(t, root, "a.md", "# A\n\n- alpha.\n")
	// Signatures are deliberately not recorded while a file's mtime is inside
	// the coarse-mtime guard window — backdate so the fast path can engage.
	backdate := func(name string) {
		old := time.Now().Add(-time.Minute)
		if err := os.Chtimes(filepath.Join(root, name), old, old); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
	}
	backdate("a.md")
	s := &Store{Root: root}
	emb := rankStubEmbedder{vecs: map[string][]float32{"q": {1, 0, 0, 0}}}

	if _, err := s.Recall(context.Background(), "q", 8, emb, nil); err != nil {
		t.Fatalf("first Recall: %v", err)
	}
	ix1, sig1 := s.recallIdx, s.lastRefreshSig
	if ix1 == nil || sig1 == "" {
		t.Fatalf("handle/signature not cached after first recall")
	}

	if _, err := s.Recall(context.Background(), "q", 8, emb, nil); err != nil {
		t.Fatalf("second Recall: %v", err)
	}
	if s.recallIdx != ix1 {
		t.Fatalf("index handle was reopened on unchanged vault")
	}
	if s.lastRefreshSig != sig1 {
		t.Fatalf("signature changed on unchanged vault")
	}

	// A write must change the signature so the refresh runs again.
	writeFile(t, root, "b.md", "# B\n\n- beta.\n")
	backdate("b.md") // outside the guard window, so the new signature records
	if _, err := s.Recall(context.Background(), "q", 8, emb, nil); err != nil {
		t.Fatalf("third Recall: %v", err)
	}
	if s.lastRefreshSig == sig1 {
		t.Fatalf("signature did not change after vault write")
	}
	if !scopePresent(t, s, hasFileChunks("b.md")) {
		t.Fatalf("new file was not indexed after vault write")
	}
}
