package memory

import (
	"math"
	"reflect"
	"testing"
)

// ic builds an IndexedChunk with the given scope key, hash, and vector.
func ic(scopeKey, hash string, vector []float32) IndexedChunk {
	return IndexedChunk{
		Chunk:    Chunk{Hash: hash},
		ScopeKey: scopeKey,
		Vector:   vector,
	}
}

const cosineEps = 1e-6

func TestCosineIdenticalOrthogonalOpposite(t *testing.T) {
	tests := []struct {
		name  string
		query []float32
		vec   []float32
		want  float32
	}{
		{"identical", []float32{1, 2, 3}, []float32{1, 2, 3}, 1.0},
		{"orthogonal", []float32{1, 0}, []float32{0, 1}, 0.0},
		{"opposite", []float32{1, 0}, []float32{-1, 0}, -1.0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Arrange / Act
			got := TopK(tc.query, []IndexedChunk{ic("s", "h", tc.vec)}, 1)

			// Assert
			if len(got) != 1 {
				t.Fatalf("expected 1 result, got %d", len(got))
			}
			if math.Abs(float64(got[0].Score-tc.want)) > cosineEps {
				t.Fatalf("score = %v, want ~%v", got[0].Score, tc.want)
			}
		})
	}
}

func TestTopKRanking(t *testing.T) {
	// Arrange
	query := []float32{1, 0, 0}
	candidates := []IndexedChunk{
		ic("s", "A", []float32{0.9, 0.1, 0}),
		ic("s", "B", []float32{0, 1, 0}),
		ic("s", "C", []float32{1, 0, 0}),
	}

	// Act
	got := TopK(query, candidates, 3)

	// Assert
	wantOrder := []string{"C", "A", "B"}
	if len(got) != 3 {
		t.Fatalf("expected 3 results, got %d", len(got))
	}
	for i, h := range wantOrder {
		if got[i].Hash != h {
			t.Fatalf("position %d = %q, want %q (order %v)", i, got[i].Hash, h, hashes(got))
		}
	}
}

func TestTopKLimitsToK(t *testing.T) {
	// Arrange
	query := []float32{1, 0}
	candidates := []IndexedChunk{
		ic("s", "a", []float32{1, 0}),     // score 1.0
		ic("s", "b", []float32{0.9, 0.1}), // high
		ic("s", "c", []float32{0.5, 0.5}), // mid
		ic("s", "d", []float32{0, 1}),     // 0
		ic("s", "e", []float32{-1, 0}),    // -1
	}

	// Act
	got := TopK(query, candidates, 2)

	// Assert
	if len(got) != 2 {
		t.Fatalf("expected 2 results, got %d", len(got))
	}
	if got[0].Hash != "a" || got[1].Hash != "b" {
		t.Fatalf("top-2 = %v, want [a b]", hashes(got))
	}
}

func TestTopKKGreaterThanLen(t *testing.T) {
	// Arrange
	query := []float32{1, 0}
	candidates := []IndexedChunk{
		ic("s", "a", []float32{1, 0}),
		ic("s", "b", []float32{0, 1}),
	}

	// Act
	got := TopK(query, candidates, 10)

	// Assert
	if len(got) != 2 {
		t.Fatalf("expected all 2 results, got %d", len(got))
	}
	if got[0].Hash != "a" || got[1].Hash != "b" {
		t.Fatalf("order = %v, want [a b]", hashes(got))
	}
}

func TestTopKEmptyAndNonPositiveK(t *testing.T) {
	query := []float32{1, 0}
	candidate := []IndexedChunk{ic("s", "a", []float32{1, 0})}

	// Empty candidates → nil
	if got := TopK(query, nil, 3); got != nil {
		t.Fatalf("empty candidates: want nil, got %v", got)
	}
	// k = 0 → nil
	if got := TopK(query, candidate, 0); got != nil {
		t.Fatalf("k=0: want nil, got %v", got)
	}
	// k < 0 → nil
	if got := TopK(query, candidate, -1); got != nil {
		t.Fatalf("k<0: want nil, got %v", got)
	}
	// empty query → nil
	if got := TopK(nil, candidate, 3); got != nil {
		t.Fatalf("empty query: want nil, got %v", got)
	}
}

func TestTopKDimMismatchSkipped(t *testing.T) {
	// Arrange
	query := []float32{1, 0, 0}
	candidates := []IndexedChunk{
		ic("s", "good1", []float32{1, 0, 0}),
		ic("s", "bad", []float32{1, 0}), // wrong dim — skipped
		ic("s", "good2", []float32{0, 1, 0}),
	}

	// Act
	got := TopK(query, candidates, 10)

	// Assert
	if len(got) != 2 {
		t.Fatalf("expected 2 results (bad skipped), got %d: %v", len(got), hashes(got))
	}
	for _, r := range got {
		if r.Hash == "bad" {
			t.Fatalf("dim-mismatch candidate must be skipped, got %v", hashes(got))
		}
	}
}

func TestTopKZeroVectorScoresZero(t *testing.T) {
	// Arrange
	query := []float32{1, 0}
	candidates := []IndexedChunk{
		ic("s", "match", []float32{1, 0}),
		ic("s", "zero", []float32{0, 0}), // zero vector → score 0, no divide-by-zero
	}

	// Act
	got := TopK(query, candidates, 10)

	// Assert
	if len(got) != 2 {
		t.Fatalf("expected 2 results, got %d", len(got))
	}
	var zeroScore float32 = -99
	for _, r := range got {
		if r.Hash == "zero" {
			zeroScore = r.Score
		}
	}
	if zeroScore != 0 {
		t.Fatalf("zero vector score = %v, want 0", zeroScore)
	}
	// zero sorts last (1.0 > 0)
	if got[len(got)-1].Hash != "zero" {
		t.Fatalf("zero vector should sort last, order = %v", hashes(got))
	}
}

func TestTopKDeterministicTieBreak(t *testing.T) {
	// Arrange: identical vectors → equal scores; tie-break by ScopeKey then Hash asc.
	query := []float32{1, 0}
	candidates := []IndexedChunk{
		ic("z", "h1", []float32{1, 0}),
		ic("a", "h2", []float32{1, 0}),
		ic("a", "h1", []float32{1, 0}),
		ic("m", "h0", []float32{1, 0}),
	}
	want := []string{"a/h1", "a/h2", "m/h0", "z/h1"}

	// Act + Assert: stable across repeated calls.
	for run := 0; run < 5; run++ {
		got := TopK(query, candidates, 4)
		if len(got) != 4 {
			t.Fatalf("run %d: expected 4 results, got %d", run, len(got))
		}
		keys := make([]string, len(got))
		for i, r := range got {
			keys[i] = r.ScopeKey + "/" + r.Hash
		}
		if !reflect.DeepEqual(keys, want) {
			t.Fatalf("run %d: tie-break order = %v, want %v", run, keys, want)
		}
	}
}

// hashes extracts hashes for readable failure messages.
func hashes(s []ScoredChunk) []string {
	out := make([]string, len(s))
	for i, r := range s {
		out[i] = r.Hash
	}
	return out
}
