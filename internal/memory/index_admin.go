package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/embed"
)

// indexDBPath is the on-disk location of the derived semantic index, relative to
// the vault root. Kept in one place so admin + recall agree.
func (s *Store) indexDBPath() string {
	return filepath.Join(s.Root, ".index", "embeddings.db")
}

// RebuildIndex wipes and fully re-embeds the vault index. It errors if the
// embedder is unavailable (you cannot rebuild offline). Returns the number of
// chunks indexed. The existing DB is deleted first so the rebuild is from
// scratch, and unified_memory.md is excluded (refreshIndex skips the aggregate).
func (s *Store) RebuildIndex(ctx context.Context, emb Embedder) (int, error) {
	if emb == nil || !emb.Enabled() {
		return 0, fmt.Errorf("cannot rebuild: embedding endpoint unavailable: %w", embed.ErrUnavailable)
	}

	// Probe the embedding dimensionality so the index meta matches the model.
	vecs, err := emb.Embed(ctx, []string{"auxly index probe"})
	if err != nil {
		return 0, fmt.Errorf("cannot rebuild: probe embed failed: %w: %w", err, embed.ErrUnavailable)
	}
	if len(vecs) == 0 || len(vecs[0]) == 0 {
		return 0, fmt.Errorf("cannot rebuild: probe embed empty: %w", embed.ErrUnavailable)
	}
	dim := len(vecs[0])

	dbPath := s.indexDBPath()
	if err := os.Remove(dbPath); err != nil && !os.IsNotExist(err) {
		return 0, fmt.Errorf("remove stale index: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return 0, fmt.Errorf("create index dir: %w", err)
	}

	ix, err := OpenIndex(dbPath, IndexMeta{
		Provider:           emb.Provider(),
		Model:              emb.Model(),
		Dim:                dim,
		EmbedFormatVersion: embedFormatVersion,
		ChunkerVersion:     chunkerVersion,
	})
	if err != nil {
		return 0, fmt.Errorf("open index for rebuild: %w", err)
	}
	defer ix.Close()

	// Same pass as recall — already skips unified_memory.md.
	s.refreshIndex(ctx, ix, emb)

	n, err := ix.Count()
	if err != nil {
		return 0, fmt.Errorf("count rebuilt chunks: %w", err)
	}
	return n, nil
}

// IndexStatus reports the on-disk index state. Built is false (no error) when the
// index has never been created; otherwise meta + chunk count come from the stored DB.
type IndexStatus struct {
	Built    bool
	Path     string
	Provider string
	Model    string
	Dim      int
	Chunks   int
}

// IndexStatus reports the on-disk index state WITHOUT creating or mutating it. If
// the DB does not exist it returns Built=false. When it exists it opens read-only
// (no reconcile/wipe) and reads meta + chunk count.
func (s *Store) IndexStatus() (IndexStatus, error) {
	path := s.indexDBPath()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return IndexStatus{Built: false, Path: path}, nil
	} else if err != nil {
		return IndexStatus{}, fmt.Errorf("stat index: %w", err)
	}

	ix, err := OpenIndexReadOnly(path)
	if err != nil {
		return IndexStatus{}, fmt.Errorf("open index for status: %w", err)
	}
	defer ix.Close()

	count, err := ix.Count()
	if err != nil {
		return IndexStatus{}, fmt.Errorf("count index chunks: %w", err)
	}
	meta := ix.Meta()
	return IndexStatus{
		Built:    true,
		Path:     path,
		Provider: meta.Provider,
		Model:    meta.Model,
		Dim:      meta.Dim,
		Chunks:   count,
	}, nil
}
