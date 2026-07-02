package memory

import (
	"context"
	"fmt"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/embed"
)

// RecallDebugHit is one candidate scored by RecallDebug for explainable recall
// UI. Score is raw cosine. Rank is the post-recency-sort position for
// above-floor hits, or -1 for candidates the real Recall would discard before
// ranking. Accepted means the real Recall would return the hit for the same k.
type RecallDebugHit struct {
	File       string
	Heading    string
	Text       string
	LineStart  int
	LineEnd    int
	Score      float32
	Rank       int
	AboveFloor bool
	Accepted   bool
}

// RecallDebug mirrors Recall's semantic pipeline but returns explainability
// rows for the TUI playground and deliberately never emits a RecallEvent.
func (s *Store) RecallDebug(ctx context.Context, query string, k int, emb Embedder, allow func(file string) bool) (hits []RecallDebugHit, floor float32, err error) {
	if emb == nil || !emb.Enabled() {
		return nil, 0, fmt.Errorf("recall unavailable: %w", embed.ErrUnavailable)
	}
	if k <= 0 {
		k = defaultRecallK
	}

	// Embed the query FIRST: if we can't embed the query there's nothing to score.
	vecs, err := emb.Embed(ctx, []string{query})
	if err != nil {
		return nil, 0, fmt.Errorf("recall query embed failed: %w: %w", err, embed.ErrUnavailable)
	}
	if len(vecs) == 0 || len(vecs[0]) == 0 {
		return nil, 0, fmt.Errorf("recall query embed empty: %w", embed.ErrUnavailable)
	}
	qv := vecs[0]
	dim := len(qv)

	meta := IndexMeta{
		Provider:           emb.Provider(),
		Model:              emb.Model(),
		Dim:                dim,
		EmbedFormatVersion: embedFormatVersion,
		ChunkerVersion:     chunkerVersion,
	}

	// This refresh-and-load section intentionally mirrors Recall. Any change to
	// index reuse, vault signatures, refresh behavior, or ACL filtering must land
	// in both paths so debug rows explain the real recall result.
	s.recallMu.Lock()
	ix, err := s.recallIndex(meta)
	if err != nil {
		s.recallMu.Unlock()
		return nil, 0, fmt.Errorf("open recall index: %w", err)
	}

	files, lerr := s.List()
	sig := vaultSignature(files, meta)
	if lerr != nil || sig != s.lastRefreshSig {
		clean := s.refreshIndex(ctx, ix, emb)
		if lerr == nil && clean && !vaultTouchedWithin(files, 2*time.Second) {
			s.lastRefreshSig = sig
		}
	}

	cands, err := ix.Load(func(_ string, file string) bool {
		return file != unifiedMemoryFile && (allow == nil || allow(file))
	})
	s.recallMu.Unlock()
	if err != nil {
		return nil, 0, fmt.Errorf("load recall candidates: %w", err)
	}

	scored := TopK(qv, cands, len(cands))
	floor = recallMinScore()
	mtimes := fileMtimes(files)
	above := make([]ScoredChunk, 0, len(scored))
	ranks := make([]float32, 0, len(scored))
	below := make([]ScoredChunk, 0, len(scored))
	for _, sc := range scored {
		if sc.Score < floor {
			below = append(below, sc)
			continue
		}
		above = append(above, sc)
		ranks = append(ranks, sc.Score*recencyBoost(mtimes[sc.File]))
	}
	sortByRank(above, ranks)
	sortScored(below)
	if len(below) > 20 {
		below = below[:20]
	}

	hits = make([]RecallDebugHit, 0, len(above)+len(below))
	for i, sc := range above {
		hits = append(hits, recallDebugHit(sc, i, true, i < k))
	}
	for _, sc := range below {
		hits = append(hits, recallDebugHit(sc, -1, false, false))
	}
	return hits, floor, nil
}

func recallDebugHit(sc ScoredChunk, rank int, aboveFloor bool, accepted bool) RecallDebugHit {
	return RecallDebugHit{
		File:       sc.File,
		Heading:    sc.Heading,
		Text:       sc.Text,
		LineStart:  sc.LineStart,
		LineEnd:    sc.LineEnd,
		Score:      sc.Score,
		Rank:       rank,
		AboveFloor: aboveFloor,
		Accepted:   accepted,
	}
}
