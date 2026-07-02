package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/pending"
)

// mapEmbedder returns fixed vectors for exact texts so the test controls
// cosine similarity precisely; unknown texts get an orthogonal filler.
type mapEmbedder struct {
	enabled bool
	vecs    map[string][]float32
}

func (e mapEmbedder) Enabled() bool    { return e.enabled }
func (e mapEmbedder) Model() string    { return "map-stub" }
func (e mapEmbedder) Provider() string { return "map-stub" }
func (e mapEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
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

func supersedeServer(t *testing.T, emb memory.Embedder) *Server {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "infra.md"),
		[]byte("# Infra\n\n- prod server IP is 192.168.1.24\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewServer(dir)
	s.store.WorkspaceRoot = ""
	s.newEmbedder = func() memory.Embedder { return emb }
	return s
}

// TestSupersedeRewritesContradiction locks the core: a new fact that restates
// an existing one becomes a REPLACE — old bullet deleted, new bullet carrying
// the dated "was:" trace — never a second contradictory bullet.
func TestSupersedeRewritesContradiction(t *testing.T) {
	emb := mapEmbedder{enabled: true, vecs: map[string][]float32{
		"- prod server IP is 192.168.1.30": {1, 0, 0.05, 0}, // the new fact (query)
		"- prod server IP is 192.168.1.24": {1, 0, 0, 0},    // the indexed old bullet
	}}
	s := supersedeServer(t, emb)

	got := s.maybeSupersede("infra.md", "+ - prod server IP is 192.168.1.30\n")
	if !strings.Contains(got, "- - prod server IP is 192.168.1.24") {
		t.Fatalf("old bullet not deleted:\n%s", got)
	}
	if !strings.Contains(got, "was: prod server IP is 192.168.1.24") {
		t.Fatalf("no dated was: trace:\n%s", got)
	}
	if !strings.Contains(got, "192.168.1.30 (updated ") {
		t.Fatalf("new fact not annotated:\n%s", got)
	}

	// The transformed diff must APPLY correctly: old line gone, trace kept.
	existing, _ := s.store.View("infra.md")
	merged := pending.ApplyDiff(existing, got)
	if strings.Contains(merged, "IP is 192.168.1.24\n") {
		t.Fatalf("stale fact survived application:\n%s", merged)
	}
	if !strings.Contains(merged, "192.168.1.30") || !strings.Contains(merged, "was: prod server IP is 192.168.1.24") {
		t.Fatalf("replacement missing after application:\n%s", merged)
	}
}

// TestSupersedeGracefulDegrade locks every bail-out path: feature off, embedder
// down, and no sufficiently-similar fact all leave the diff untouched.
func TestSupersedeGracefulDegrade(t *testing.T) {
	diff := "+ - prod server IP is 192.168.1.30\n"

	t.Run("env off", func(t *testing.T) {
		t.Setenv("AUXLY_SUPERSEDE", "off")
		emb := mapEmbedder{enabled: true, vecs: map[string][]float32{
			"- prod server IP is 192.168.1.30": {1, 0, 0, 0},
			"- prod server IP is 192.168.1.24": {1, 0, 0, 0},
		}}
		if got := supersedeServer(t, emb).maybeSupersede("infra.md", diff); got != diff {
			t.Fatalf("diff changed despite AUXLY_SUPERSEDE=off: %q", got)
		}
	})

	t.Run("embedder down", func(t *testing.T) {
		if got := supersedeServer(t, mapEmbedder{enabled: false}).maybeSupersede("infra.md", diff); got != diff {
			t.Fatalf("diff changed with embeddings down: %q", got)
		}
	})

	t.Run("nothing similar", func(t *testing.T) {
		emb := mapEmbedder{enabled: true, vecs: map[string][]float32{
			"- prod server IP is 192.168.1.30": {0, 1, 0, 0}, // orthogonal to the vault
			"- prod server IP is 192.168.1.24": {1, 0, 0, 0},
		}}
		if got := supersedeServer(t, emb).maybeSupersede("infra.md", diff); got != diff {
			t.Fatalf("diff changed with no similar fact: %q", got)
		}
	})
}
