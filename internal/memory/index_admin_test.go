package memory

import (
	"context"
	"errors"
	"hash/fnv"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/embed"
)

// adminStubEmbedder is a deterministic, OFFLINE embedder for index-admin tests.
// Identical text maps to an identical vector so rebuilds are stable.
type adminStubEmbedder struct{ enabled bool }

func (e adminStubEmbedder) Enabled() bool    { return e.enabled }
func (e adminStubEmbedder) Model() string    { return "stub-embed" }
func (e adminStubEmbedder) Provider() string { return "stub-provider" }

const adminStubDim = 16

func (e adminStubEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = adminStubVector(t)
	}
	return out, nil
}

func adminStubVector(s string) []float32 {
	vec := make([]float32, adminStubDim)
	for d := 0; d < adminStubDim; d++ {
		h := fnv.New64a()
		_, _ = h.Write([]byte{byte(d)})
		_, _ = h.Write([]byte(s))
		vec[d] = float32(float64(h.Sum64()%2000)/1000.0 - 1.0)
	}
	var sq float64
	for _, x := range vec {
		sq += float64(x) * float64(x)
	}
	if math.Sqrt(sq) == 0 {
		vec[0] = 1
	}
	return vec
}

// newAdminVault builds a Store rooted at a fresh TempDir with two real .md files
// plus a unified_memory.md aggregate (which must NOT be indexed).
func newAdminVault(t *testing.T) *Store {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"projects.md": "# Projects\n\n- Auxly is a local-first memory CLI.\n- It indexes the vault semantically.\n",
		"identity.md": "# Identity\n\n- Wael builds Auxly.\n- He prefers Go and immutable patterns.\n",
		// The aggregate concatenates everything and must contribute nothing.
		unifiedMemoryFile: "# Unified\n\n- Auxly is a local-first memory CLI.\n- Wael builds Auxly.\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return &Store{Root: root}
}

func TestRebuildIndexSkipsUnifiedAndMatchesStatus(t *testing.T) {
	s := newAdminVault(t)
	emb := adminStubEmbedder{enabled: true}

	n, err := s.RebuildIndex(context.Background(), emb)
	if err != nil {
		t.Fatalf("RebuildIndex: %v", err)
	}
	if n <= 0 {
		t.Fatalf("expected chunks > 0, got %d", n)
	}

	// Count the chunks the NON-aggregate files should produce. If the aggregate
	// were indexed, the total would exceed this.
	wantChunks := 0
	for _, name := range []string{"projects.md", "identity.md"} {
		body, err := s.View(name)
		if err != nil {
			t.Fatalf("View %s: %v", name, err)
		}
		wantChunks += len(ChunkMarkdown(name, body))
	}
	if n != wantChunks {
		t.Fatalf("indexed %d chunks, want %d (unified_memory.md must contribute 0)", n, wantChunks)
	}

	st, err := s.IndexStatus()
	if err != nil {
		t.Fatalf("IndexStatus: %v", err)
	}
	if !st.Built {
		t.Fatalf("expected Built=true after rebuild")
	}
	if st.Chunks != n {
		t.Fatalf("status chunks %d != rebuild count %d", st.Chunks, n)
	}
	if st.Provider != emb.Provider() || st.Model != emb.Model() {
		t.Fatalf("status meta mismatch: got %s/%s want %s/%s", st.Provider, st.Model, emb.Provider(), emb.Model())
	}
	if st.Dim != adminStubDim {
		t.Fatalf("status dim %d != %d", st.Dim, adminStubDim)
	}
}

func TestRebuildIndexOfflineErrors(t *testing.T) {
	s := newAdminVault(t)
	emb := adminStubEmbedder{enabled: false}

	_, err := s.RebuildIndex(context.Background(), emb)
	if err == nil {
		t.Fatalf("expected error when embedder disabled")
	}
	if !errors.Is(err, embed.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}

func TestIndexStatusNotBuiltCreatesNothing(t *testing.T) {
	s := &Store{Root: t.TempDir()}

	st, err := s.IndexStatus()
	if err != nil {
		t.Fatalf("IndexStatus: %v", err)
	}
	if st.Built {
		t.Fatalf("expected Built=false for fresh vault")
	}

	// IndexStatus must be read-only: it must NOT create the DB.
	if _, err := os.Stat(st.Path); !os.IsNotExist(err) {
		t.Fatalf("IndexStatus created the DB at %s (err=%v); it must be read-only", st.Path, err)
	}
}

func TestIndexStatusReadOnlyStableCount(t *testing.T) {
	s := newAdminVault(t)
	emb := adminStubEmbedder{enabled: true}

	if _, err := s.RebuildIndex(context.Background(), emb); err != nil {
		t.Fatalf("RebuildIndex: %v", err)
	}

	first, err := s.IndexStatus()
	if err != nil {
		t.Fatalf("IndexStatus #1: %v", err)
	}
	second, err := s.IndexStatus()
	if err != nil {
		t.Fatalf("IndexStatus #2: %v", err)
	}
	if first.Chunks != second.Chunks {
		t.Fatalf("read-only status mutated count: %d -> %d", first.Chunks, second.Chunks)
	}
	if first.Chunks == 0 {
		t.Fatalf("expected non-zero chunk count")
	}
}
