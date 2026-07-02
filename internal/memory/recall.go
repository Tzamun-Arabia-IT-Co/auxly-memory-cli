package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

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

	meta := IndexMeta{
		Provider:           emb.Provider(),
		Model:              emb.Model(),
		Dim:                dim,
		EmbedFormatVersion: embedFormatVersion,
		ChunkerVersion:     chunkerVersion,
	}

	// The whole refresh-and-load section is serialized per Store: the long-lived
	// MCP server reuses one index handle and one vault signature across recalls.
	s.recallMu.Lock()
	ix, err := s.recallIndex(meta)
	if err != nil {
		s.recallMu.Unlock()
		return nil, fmt.Errorf("open recall index: %w", err)
	}

	// Refresh the index incrementally (best-effort: a refresh hiccup must not
	// abort recall — existing indexed chunks still serve). Skipped entirely when
	// the vault signature (file set + mtimes) is unchanged since the last CLEAN
	// refresh: on the steady state this avoids re-chunking and re-hashing every
	// file on every recall. A partial refresh (embed hiccup, prune failure) never
	// records the signature, so the next recall retries instead of masking the
	// gap until the next vault write.
	files, lerr := s.List()
	sig := vaultSignature(files, meta)
	if lerr != nil || sig != s.lastRefreshSig {
		clean := s.refreshIndex(ctx, ix, emb)
		// Never record a signature while a file's mtime is still inside the
		// current coarse-mtime tick (FAT/SMB ~2s): a second same-tick,
		// same-size edit would produce an identical signature and the change
		// would be skipped forever. Holding off costs one extra refresh pass.
		if lerr == nil && clean && !vaultTouchedWithin(files, 2*time.Second) {
			s.lastRefreshSig = sig
		}
	}

	// Score only chunks that pass BOTH the aggregate exclusion AND the ACL. Even
	// though unified_memory.md is never indexed (refreshIndex skips it), the Load
	// filter double-guards it so a stale/imported row can never slip through.
	cands, err := ix.Load(func(_ string, file string) bool {
		return file != unifiedMemoryFile && (allow == nil || allow(file))
	})
	s.recallMu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("load recall candidates: %w", err)
	}

	// Score everything, then rank with a light recency nudge and drop
	// low-relevance noise. The threshold applies to the RAW cosine score (the
	// recency boost is ranking-only, never a relevance signal).
	scored := TopK(qv, cands, len(cands))
	minScore := recallMinScore()
	mtimes := fileMtimes(files)
	kept := make([]ScoredChunk, 0, len(scored))
	ranks := make([]float32, 0, len(scored))
	for _, sc := range scored {
		if sc.Score < minScore {
			continue
		}
		kept = append(kept, sc)
		ranks = append(ranks, sc.Score*recencyBoost(mtimes[sc.File]))
	}
	sortByRank(kept, ranks)
	// Snapshot the full ranked list (pre-cut) for the RecallEvent below — kept
	// gets sliced down to k next, but the observer wants the whole picture
	// (including what was scored but trimmed).
	ranked := kept
	if len(kept) > k {
		kept = kept[:k]
	}
	if len(kept) == 0 {
		// Everything scored below the relevance floor — treat like an unavailable
		// semantic layer so callers fall back to substring search instead of
		// receiving top-8 noise.
		return nil, fmt.Errorf("no recall hit above relevance threshold %.2f: %w", minScore, embed.ErrUnavailable)
	}

	hits := make([]RecallHit, 0, len(kept))
	for _, sc := range kept {
		hits = append(hits, RecallHit{
			File:      sc.File,
			Heading:   sc.Heading,
			Text:      sc.Text,
			LineStart: sc.LineStart,
			LineEnd:   sc.LineEnd,
			Score:     sc.Score,
		})
	}

	if s.RecallObserver != nil && !recallObserverSuppressed(ctx) {
		// Cap chunks at 32 (top-ranked — `ranked` is already sorted) and total
		// emitted rows at 64: an outsized candidate list must not turn one
		// recall into a giant event. One hit per BULLET line, not per chunk —
		// fact-granularity identity is what the decay/review features match on.
		nChunks := len(ranked)
		if nChunks > 32 {
			nChunks = 32
		}
		evHits := make([]RecallEventHit, 0, 64)
	emit:
		for i := 0; i < nChunks; i++ {
			sc := ranked[i]
			for _, lh := range bulletHashes(sc.Text) {
				if len(evHits) >= 64 {
					break emit
				}
				evHits = append(evHits, RecallEventHit{
					File:     sc.File,
					LineHash: lh,
					Score:    sc.Score,
					Rank:     i,
					Accepted: i < len(kept),
				})
			}
		}
		// A panicking observer must not take recall (or the whole MCP server)
		// down with it — analytics are strictly best-effort.
		func() {
			defer func() { _ = recover() }()
			s.RecallObserver(RecallEvent{
				QueryHash: HashRecallText(query),
				Fallback:  false,
				Hits:      evHits,
			})
		}()
	}

	return hits, nil
}

// recallIndex returns the cached open index handle, (re)opening it when absent
// or when the embedding identity changed. Caller holds recallMu. The handle is
// deliberately NOT closed per-recall: the MCP server is long-lived and SQLite
// open/close per call was pure overhead (one-shot CLI processes release it at
// exit).
func (s *Store) recallIndex(meta IndexMeta) (*Index, error) {
	if s.recallIdx != nil && s.recallIdxMeta == meta && s.recallIdxLive() {
		return s.recallIdx, nil
	}
	if s.recallIdx != nil {
		s.recallIdx.Close()
		s.recallIdx = nil
	}
	indexDir := filepath.Join(s.Root, ".index")
	if err := os.MkdirAll(indexDir, 0o755); err != nil {
		return nil, fmt.Errorf("create index dir: %w", err)
	}
	dbPath := filepath.Join(indexDir, "embeddings.db")
	ix, err := OpenIndex(dbPath, meta)
	if err != nil {
		return nil, err
	}
	s.recallIdx = ix
	s.recallIdxMeta = meta
	s.recallIdxStat, _ = os.Stat(dbPath)
	s.lastRefreshSig = "" // new handle → force one full refresh pass
	return ix, nil
}

// recallIdxLive reports whether the cached handle still points at the on-disk
// DB. `auxly index rebuild` (another process, or the CLI while the MCP server
// runs) DELETES and recreates embeddings.db — a handle on the unlinked inode
// would serve stale chunks forever. One stat per recall buys the check.
func (s *Store) recallIdxLive() bool {
	if s.recallIdxStat == nil {
		return false
	}
	st, err := os.Stat(s.indexDBPath())
	return err == nil && os.SameFile(s.recallIdxStat, st)
}

// vaultSignature fingerprints the vault's file set + mtimes + the embedding
// identity. Unchanged signature ⇒ nothing to re-index. The meta is included so
// an index wipe caused by an embedding-model change never skips its rebuild.
func vaultSignature(files []FileInfo, meta IndexMeta) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s|%s|%d|%d|%d;", meta.Provider, meta.Model, meta.Dim, meta.EmbedFormatVersion, meta.ChunkerVersion)
	for _, f := range files {
		fmt.Fprintf(&b, "%s=%d,%d;", f.Path, f.ModTime.UnixNano(), f.Size)
	}
	return b.String()
}

// vaultTouchedWithin reports whether any vault file was modified within d of
// now — the window where a coarse-mtime filesystem could still fold a further
// edit into the same signature.
func vaultTouchedWithin(files []FileInfo, d time.Duration) bool {
	cutoff := time.Now().Add(-d)
	for _, f := range files {
		if f.ModTime.After(cutoff) {
			return true
		}
	}
	return false
}

func fileMtimes(files []FileInfo) map[string]time.Time {
	m := make(map[string]time.Time, len(files))
	for _, f := range files {
		m[f.Name] = f.ModTime
	}
	return m
}

// recallMinScore is the raw-cosine floor below which a hit is noise, not a
// memory. Tuned on the real vault; override with AUXLY_RECALL_MIN_SCORE.
func recallMinScore() float32 {
	if v := os.Getenv("AUXLY_RECALL_MIN_SCORE"); v != "" {
		if f, err := strconv.ParseFloat(v, 32); err == nil && f >= 0 && f <= 1 {
			return float32(f)
		}
	}
	return 0.35
}

// recencyBoost nudges recently-touched files up the ranking: ×1.2 for a file
// written today decaying linearly to ×1.0 at 30 days. A nudge, not a reorder —
// relevance stays the dominant signal.
func recencyBoost(mtime time.Time) float32 {
	if mtime.IsZero() {
		return 1
	}
	age := time.Since(mtime)
	const window = 30 * 24 * time.Hour
	if age < 0 {
		return 1.2 // future mtime (clock skew, restored backup) — cap, don't extrapolate
	}
	if age >= window {
		return 1
	}
	return 1 + 0.2*float32(1-age.Seconds()/window.Seconds())
}

// sortByRank orders kept by the parallel ranks slice (descending), stable
// tie-break on the underlying deterministic order TopK already produced.
func sortByRank(kept []ScoredChunk, ranks []float32) {
	idx := make([]int, len(kept))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return ranks[idx[a]] > ranks[idx[b]] })
	out := make([]ScoredChunk, len(kept))
	for i, j := range idx {
		out[i] = kept[j]
	}
	copy(kept, out)
}

// refreshIndex incrementally re-embeds changed chunks across the vault. It is
// best-effort: a per-file embed failure is skipped, never fatal. unified_memory.md
// is ALWAYS excluded — indexing the aggregate would create vectors that bypass
// per-file ACL. Returns true only when EVERY file reconciled cleanly — a partial
// pass must not be recorded as a completed refresh.
func (s *Store) refreshIndex(ctx context.Context, ix *Index, emb Embedder) bool {
	files, err := s.List()
	if err != nil {
		return false
	}
	// Track every scope_key we actually process. Any stored scope NOT in this set
	// is orphaned — a file deleted from the vault, or a global file shadowed by a
	// workspace file of the same Name (List merges workspace-over-global, so the
	// global's physical Path disappears here). Those chunks must be swept or stale/
	// shadowed content could still be served via Load's bare-filename ACL.
	processedScopes := make(map[string]bool, len(files))
	clean := true
	for _, f := range files {
		if f.Name == unifiedMemoryFile {
			continue // hard exclusion — the aggregate is never indexed
		}
		processedScopes[f.Path] = true
		if !s.refreshFile(ctx, ix, emb, f) {
			clean = false
		}
	}
	if err := ix.PruneScopesExcept(processedScopes); err != nil {
		clean = false
	}
	return clean
}

// refreshFile reconciles one file's chunks with the index: embeds new chunks in a
// single batched call, then prunes chunks deleted from the source. scopeKey is the
// physical path (NOT bare Name) so a workspace file never shadows a global one.
// Returns true only when the file reconciled cleanly (a skipped embed/put leaves
// the refresh incomplete, and the caller must not record it as done).
func (s *Store) refreshFile(ctx context.Context, ix *Index, emb Embedder, f FileInfo) bool {
	scopeKey := f.Path
	content, err := s.View(f.Name)
	if err != nil {
		return false
	}
	chunks := ChunkMarkdown(f.Name, content)

	clean := true
	currentHashes := make(map[string]bool, len(chunks))
	var pending []Chunk
	for _, c := range chunks {
		currentHashes[c.Hash] = true
		has, herr := ix.Has(scopeKey, c.Hash)
		if herr != nil {
			clean = false
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
				if perr := ix.Put(scopeKey, c, vecs[i]); perr != nil {
					clean = false
				}
			}
		} else {
			clean = false
		}
	}

	if perr := ix.PruneExcept(scopeKey, currentHashes); perr != nil {
		clean = false
	}
	return clean
}
