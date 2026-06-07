package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/embed"
)

// unifiedMemoryFile is the derived aggregate that concatenates EVERY memory file
// (including personal.md). It must never be indexed or scored — an aggregate
// bypasses per-file ACL. This is a hard exclusion at both index-build and Load.
const unifiedMemoryFile = "unified_memory.md"

// defaultRecallK is the number of hits returned when the caller passes k <= 0.
const defaultRecallK = 8

// Embedder is the minimal embedding surface Recall needs. embed.Client satisfies
// it structurally; tests inject a deterministic offline stub. Defined here (where
// it is consumed) so embed never needs to import memory.
type Embedder interface {
	Enabled() bool
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	Model() string
	Provider() string
}

// RecallHit is one semantically-matched chunk returned to the caller.
type RecallHit struct {
	File      string
	Heading   string
	Text      string
	LineStart int
	LineEnd   int
	Score     float32
}

// Recall returns the top-k semantically-closest chunks to query.
//
// allow(file) is the ACL gate (the MCP server passes s.canRead) — applied as the
// index Load pre-filter so unshared vectors are never even scored. unified_memory.md
// is excluded at BOTH the index-build pass and the Load filter, independent of allow.
//
// Returns embed.ErrUnavailable (wrapped) when emb is nil/disabled or the query
// embed fails, so the caller can fall back to substring search.
func (s *Store) Recall(ctx context.Context, query string, k int, emb Embedder, allow func(file string) bool) ([]RecallHit, error) {
	if emb == nil || !emb.Enabled() {
		return nil, fmt.Errorf("recall unavailable: %w", embed.ErrUnavailable)
	}
	if k <= 0 {
		k = defaultRecallK
	}

	// Embed the query FIRST: if we can't embed the query there's nothing to score.
	vecs, err := emb.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("recall query embed failed: %w: %w", err, embed.ErrUnavailable)
	}
	if len(vecs) == 0 || len(vecs[0]) == 0 {
		return nil, fmt.Errorf("recall query embed empty: %w", embed.ErrUnavailable)
	}
	qv := vecs[0]
	dim := len(qv)

	indexDir := filepath.Join(s.Root, ".index")
	if err := os.MkdirAll(indexDir, 0o755); err != nil {
		return nil, fmt.Errorf("create index dir: %w", err)
	}
	ix, err := OpenIndex(filepath.Join(indexDir, "embeddings.db"), IndexMeta{
		Provider:           emb.Provider(),
		Model:              emb.Model(),
		Dim:                dim,
		EmbedFormatVersion: embedFormatVersion,
		ChunkerVersion:     chunkerVersion,
	})
	if err != nil {
		return nil, fmt.Errorf("open recall index: %w", err)
	}
	defer ix.Close()

	// Refresh the index incrementally (best-effort: a refresh hiccup must not abort
	// recall — existing indexed chunks still serve).
	s.refreshIndex(ctx, ix, emb)

	// Score only chunks that pass BOTH the aggregate exclusion AND the ACL. Even
	// though unified_memory.md is never indexed (refreshIndex skips it), the Load
	// filter double-guards it so a stale/imported row can never slip through.
	cands, err := ix.Load(func(_ string, file string) bool {
		return file != unifiedMemoryFile && (allow == nil || allow(file))
	})
	if err != nil {
		return nil, fmt.Errorf("load recall candidates: %w", err)
	}

	scored := TopK(qv, cands, k)
	hits := make([]RecallHit, 0, len(scored))
	for _, sc := range scored {
		hits = append(hits, RecallHit{
			File:      sc.File,
			Heading:   sc.Heading,
			Text:      sc.Text,
			LineStart: sc.LineStart,
			LineEnd:   sc.LineEnd,
			Score:     sc.Score,
		})
	}
	return hits, nil
}

// refreshIndex incrementally re-embeds changed chunks across the vault. It is
// best-effort: a per-file embed failure is skipped, never fatal. unified_memory.md
// is ALWAYS excluded — indexing the aggregate would create vectors that bypass
// per-file ACL.
func (s *Store) refreshIndex(ctx context.Context, ix *Index, emb Embedder) {
	files, err := s.List()
	if err != nil {
		return
	}
	// Track every scope_key we actually process. Any stored scope NOT in this set
	// is orphaned — a file deleted from the vault, or a global file shadowed by a
	// workspace file of the same Name (List merges workspace-over-global, so the
	// global's physical Path disappears here). Those chunks must be swept or stale/
	// shadowed content could still be served via Load's bare-filename ACL.
	processedScopes := make(map[string]bool, len(files))
	for _, f := range files {
		if f.Name == unifiedMemoryFile {
			continue // hard exclusion — the aggregate is never indexed
		}
		processedScopes[f.Path] = true
		s.refreshFile(ctx, ix, emb, f)
	}
	_ = ix.PruneScopesExcept(processedScopes)
}

// refreshFile reconciles one file's chunks with the index: embeds new chunks in a
// single batched call, then prunes chunks deleted from the source. scopeKey is the
// physical path (NOT bare Name) so a workspace file never shadows a global one.
func (s *Store) refreshFile(ctx context.Context, ix *Index, emb Embedder, f FileInfo) {
	scopeKey := f.Path
	content, err := s.View(f.Name)
	if err != nil {
		return
	}
	chunks := ChunkMarkdown(f.Name, content)

	currentHashes := make(map[string]bool, len(chunks))
	var pending []Chunk
	for _, c := range chunks {
		currentHashes[c.Hash] = true
		has, herr := ix.Has(scopeKey, c.Hash)
		if herr != nil {
			continue
		}
		if !has {
			pending = append(pending, c)
		}
	}

	if len(pending) > 0 {
		texts := make([]string, len(pending))
		for i, c := range pending {
			texts[i] = c.Text
		}
		// Best-effort: a failed embed here leaves this file's NEW chunks unindexed
		// but does NOT abort recall — already-indexed chunks still serve.
		if vecs, eerr := emb.Embed(ctx, texts); eerr == nil && len(vecs) == len(pending) {
			for i, c := range pending {
				_ = ix.Put(scopeKey, c, vecs[i])
			}
		}
	}

	_ = ix.PruneExcept(scopeKey, currentHashes)
}
