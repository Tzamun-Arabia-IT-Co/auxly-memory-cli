package memory

import (
	"context"
	"errors"
	"testing"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/embed"
)

type disabledDebugEmbedder struct {
	rankStubEmbedder
}

func (disabledDebugEmbedder) Enabled() bool { return false }

func TestRecallDebugRanksAcceptedAndCutHits(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AUXLY_RECALL_MIN_SCORE", "0.5")
	writeFile(t, root, "best.md", "# B\n\n- best debug line.\n")
	writeFile(t, root, "second.md", "# S\n\n- second debug line.\n")
	writeFile(t, root, "third.md", "# T\n\n- third debug line.\n")
	s := &Store{Root: root}
	emb := rankStubEmbedder{vecs: map[string][]float32{
		"query":                {1, 0, 0, 0},
		"- best debug line.":   {1, 0.01, 0, 0},
		"- second debug line.": {1, 0.2, 0, 0},
		"- third debug line.":  {1, 0.5, 0, 0},
	}}

	hits, floor, err := s.RecallDebug(context.Background(), "query", 2, emb, nil)
	if err != nil {
		t.Fatalf("RecallDebug: %v", err)
	}
	if floor != 0.5 {
		t.Fatalf("floor = %v, want 0.5", floor)
	}
	if len(hits) != 3 {
		t.Fatalf("want 3 hits, got %d", len(hits))
	}
	for i, h := range hits {
		if h.Rank != i {
			t.Fatalf("hit %d Rank = %d, want %d", i, h.Rank, i)
		}
		if !h.AboveFloor {
			t.Fatalf("hit %d AboveFloor = false, want true", i)
		}
		wantAccepted := i < 2
		if h.Accepted != wantAccepted {
			t.Fatalf("hit %d Accepted = %v, want %v", i, h.Accepted, wantAccepted)
		}
	}
}

func TestRecallDebugIncludesBelowFloorCandidates(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AUXLY_RECALL_MIN_SCORE", "0.8")
	writeFile(t, root, "hit.md", "# H\n\n- strong debug line.\n")
	writeFile(t, root, "weak.md", "# W\n\n- weak debug line.\n")
	s := &Store{Root: root}
	emb := rankStubEmbedder{vecs: map[string][]float32{
		"query":                {1, 0, 0, 0},
		"- strong debug line.": {1, 0.1, 0, 0},
		"- weak debug line.":   {0.1, 1, 0, 0},
	}}

	hits, _, err := s.RecallDebug(context.Background(), "query", 8, emb, nil)
	if err != nil {
		t.Fatalf("RecallDebug: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("want 2 hits, got %d", len(hits))
	}
	if hits[0].File != "hit.md" || !hits[0].AboveFloor || hits[0].Rank != 0 || !hits[0].Accepted {
		t.Fatalf("above-floor hit misclassified: %+v", hits[0])
	}
	if hits[1].File != "weak.md" || hits[1].AboveFloor || hits[1].Rank != -1 || hits[1].Accepted {
		t.Fatalf("below-floor hit misclassified: %+v", hits[1])
	}
}

func TestRecallDebugDisabledEmbedderUnavailable(t *testing.T) {
	root := t.TempDir()
	s := &Store{Root: root}

	_, _, err := s.RecallDebug(context.Background(), "query", 8, disabledDebugEmbedder{}, nil)
	if !errors.Is(err, embed.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}

func TestRecallDebugNeverEmitsObserver(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AUXLY_RECALL_MIN_SCORE", "0")
	writeFile(t, root, "a.md", "# A\n\n- observer debug line.\n")
	s := &Store{Root: root}
	s.RecallObserver = func(RecallEvent) {
		t.Fatalf("RecallDebug emitted observer event")
	}
	emb := rankStubEmbedder{vecs: map[string][]float32{
		"query":                  {1, 0, 0, 0},
		"- observer debug line.": {1, 0, 0, 0},
	}}

	if _, _, err := s.RecallDebug(context.Background(), "query", 8, emb, nil); err != nil {
		t.Fatalf("RecallDebug: %v", err)
	}
}

func TestRecallDebugAllowExcludesFile(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AUXLY_RECALL_MIN_SCORE", "0")
	writeFile(t, root, "allowed.md", "# A\n\n- allowed debug line.\n")
	writeFile(t, root, "blocked.md", "# B\n\n- blocked debug line.\n")
	s := &Store{Root: root}
	emb := rankStubEmbedder{vecs: map[string][]float32{
		"query":                 {1, 0, 0, 0},
		"- allowed debug line.": {1, 0, 0, 0},
		"- blocked debug line.": {1, 0, 0, 0},
	}}

	hits, _, err := s.RecallDebug(context.Background(), "query", 8, emb, func(file string) bool {
		return file != "blocked.md"
	})
	if err != nil {
		t.Fatalf("RecallDebug: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("want 1 hit, got %d: %+v", len(hits), hits)
	}
	if hits[0].File != "allowed.md" {
		t.Fatalf("ACL-excluded file surfaced: %+v", hits[0])
	}
}
