package memory

import (
	"math"
	"sort"
)

// ScoredChunk is an IndexedChunk paired with its cosine similarity to a query.
type ScoredChunk struct {
	IndexedChunk
	Score float32 // cosine similarity in [-1, 1]
}

// TopK returns up to k candidates with the highest cosine similarity to query,
// sorted by Score descending. candidates are assumed already ACL-filtered.
// Returns nil if query is empty, candidates is empty, or k <= 0.
// Candidates whose vector length differs from len(query) are skipped (defensive).
func TopK(query []float32, candidates []IndexedChunk, k int) []ScoredChunk {
	if len(query) == 0 || len(candidates) == 0 || k <= 0 {
		return nil
	}

	// Normalize the query norm once outside the candidate loop.
	queryNorm := norm(query)

	scored := make([]ScoredChunk, 0, len(candidates))
	for _, c := range candidates {
		if len(c.Vector) != len(query) {
			continue // dim mismatch — skip defensively, don't count it
		}
		scored = append(scored, ScoredChunk{
			IndexedChunk: c,
			Score:        cosine(query, c.Vector, queryNorm),
		})
	}

	sortScored(scored)

	if k > len(scored) {
		k = len(scored)
	}
	if k == 0 {
		return nil
	}
	return scored[:k]
}

// cosine returns the cosine similarity between query and vec, reusing the
// pre-computed query norm. dot and the candidate norm accumulate in float64 for
// numerical stability. A zero norm on either side yields 0 (no divide-by-zero).
func cosine(query, vec []float32, queryNorm float64) float32 {
	var dot, vecSq float64
	for i, q := range query {
		v := float64(vec[i])
		dot += float64(q) * v
		vecSq += v * v
	}
	vecNorm := math.Sqrt(vecSq)
	if queryNorm == 0 || vecNorm == 0 {
		return 0
	}
	return float32(dot / (queryNorm * vecNorm))
}

// norm returns the Euclidean norm of v, accumulated in float64.
func norm(v []float32) float64 {
	var sq float64
	for _, x := range v {
		f := float64(x)
		sq += f * f
	}
	return math.Sqrt(sq)
}

// sortScored orders by Score descending, with a deterministic tie-break of
// ScopeKey then Hash ascending so results are stable across runs.
func sortScored(s []ScoredChunk) {
	sort.Slice(s, func(i, j int) bool {
		a, b := s[i], s[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.ScopeKey != b.ScopeKey {
			return a.ScopeKey < b.ScopeKey
		}
		return a.Hash < b.Hash
	})
}
