package memory

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/embed"
)

type disabledPairEmbedder struct{}

func (disabledPairEmbedder) Enabled() bool { return false }
func (disabledPairEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	return nil, nil
}
func (disabledPairEmbedder) Model() string    { return "disabled" }
func (disabledPairEmbedder) Provider() string { return "disabled" }

func TestSimilarCrossFilePairsIdenticalTextTops(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	if err := s.Write("identity.md", "# Identity\n- Same fact.\n"); err != nil {
		t.Fatalf("Write identity: %v", err)
	}
	if err := s.Write("projects.md", "# Projects\n- Same fact.\n"); err != nil {
		t.Fatalf("Write projects: %v", err)
	}
	if err := s.Write("infra.md", "# Infra\n- Different fact.\n"); err != nil {
		t.Fatalf("Write infra: %v", err)
	}

	pairs, err := s.SimilarCrossFilePairs(context.Background(), rankStubEmbedder{vecs: map[string][]float32{
		"- Same fact.":      {1, 0, 0, 0},
		"- Different fact.": {0, 1, 0, 0},
	}}, 0, 0, 0.8)
	if err != nil {
		t.Fatalf("SimilarCrossFilePairs: %v", err)
	}
	if len(pairs) == 0 {
		t.Fatalf("expected at least one pair")
	}
	top := pairs[0]
	if top.A.Line != "- Same fact." || top.B.Line != "- Same fact." {
		t.Fatalf("top pair did not use identical facts: %+v", top)
	}
	if math.Abs(float64(top.Score-1)) > 1e-6 {
		t.Fatalf("top score = %v, want ~1", top.Score)
	}
}

func TestSimilarCrossFilePairsSkipsSameFileNearDuplicates(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	if err := s.Write("identity.md", "# Identity\n- Alpha one.\n- Alpha two.\n"); err != nil {
		t.Fatalf("Write identity: %v", err)
	}

	pairs, err := s.SimilarCrossFilePairs(context.Background(), rankStubEmbedder{vecs: map[string][]float32{
		"- Alpha one.": {1, 0, 0, 0},
		"- Alpha two.": {1, 0.01, 0, 0},
	}}, 0, 0, 0.8)
	if err != nil {
		t.Fatalf("SimilarCrossFilePairs: %v", err)
	}
	if len(pairs) != 0 {
		t.Fatalf("same-file duplicates surfaced: %+v", pairs)
	}
}

func TestSimilarCrossFilePairsSkipsPersonalFile(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	if err := s.Write("personal.md", "# Personal\n- Private fact.\n"); err != nil {
		t.Fatalf("Write personal: %v", err)
	}
	if err := s.Write("identity.md", "# Identity\n- Private fact.\n"); err != nil {
		t.Fatalf("Write identity: %v", err)
	}

	pairs, err := s.SimilarCrossFilePairs(context.Background(), rankStubEmbedder{vecs: map[string][]float32{
		"- Private fact.": {1, 0, 0, 0},
	}}, 0, 0, 0.8)
	if err != nil {
		t.Fatalf("SimilarCrossFilePairs: %v", err)
	}
	if len(pairs) != 0 {
		t.Fatalf("personal fact surfaced: %+v", pairs)
	}
}

func TestSimilarCrossFilePairsBelowMinCosExcluded(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	if err := s.Write("identity.md", "# Identity\n- Alpha.\n"); err != nil {
		t.Fatalf("Write identity: %v", err)
	}
	if err := s.Write("projects.md", "# Projects\n- Beta.\n"); err != nil {
		t.Fatalf("Write projects: %v", err)
	}

	pairs, err := s.SimilarCrossFilePairs(context.Background(), rankStubEmbedder{vecs: map[string][]float32{
		"- Alpha.": {1, 0, 0, 0},
		"- Beta.":  {0, 1, 0, 0},
	}}, 0, 0, 0.8)
	if err != nil {
		t.Fatalf("SimilarCrossFilePairs: %v", err)
	}
	if len(pairs) != 0 {
		t.Fatalf("below-minCos pair surfaced: %+v", pairs)
	}
}

func TestSimilarCrossFilePairsMaxPairsCut(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	if err := s.Write("identity.md", "# Identity\n- A.\n- B.\n"); err != nil {
		t.Fatalf("Write identity: %v", err)
	}
	if err := s.Write("projects.md", "# Projects\n- C.\n- D.\n"); err != nil {
		t.Fatalf("Write projects: %v", err)
	}

	pairs, err := s.SimilarCrossFilePairs(context.Background(), rankStubEmbedder{vecs: map[string][]float32{
		"- A.": {1, 0, 0, 0},
		"- B.": {1, 0, 0, 0},
		"- C.": {1, 0, 0, 0},
		"- D.": {1, 0, 0, 0},
	}}, 0, 2, 0.8)
	if err != nil {
		t.Fatalf("SimilarCrossFilePairs: %v", err)
	}
	if len(pairs) != 2 {
		t.Fatalf("got %d pairs, want 2", len(pairs))
	}
}

func TestSimilarCrossFilePairsDisabledEmbedderUnavailable(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	if _, err := s.SimilarCrossFilePairs(context.Background(), disabledPairEmbedder{}, 0, 0, 0); !errors.Is(err, embed.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}

// batchRecorderEmbedder records the size of every Embed call so a test can
// assert the caller chunks a large fact set into the request budget's
// sub-batches instead of embedding everything in one call.
type batchRecorderEmbedder struct {
	batchSizes []int
}

func (e *batchRecorderEmbedder) Enabled() bool    { return true }
func (e *batchRecorderEmbedder) Model() string    { return "batch-recorder" }
func (e *batchRecorderEmbedder) Provider() string { return "batch-recorder" }
func (e *batchRecorderEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	e.batchSizes = append(e.batchSizes, len(texts))
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{1, 0, 0, 0}
	}
	return out, nil
}

// TestSimilarCrossFilePairsChunksEmbedBatches locks the fix for the CRITICAL
// finding that one Embed call carrying all ≤500 facts blows embed.Client's
// 500ms per-request timeout (sized for tiny realtime recall batches), turning
// a vault-wide sweep into a misleading "configure a provider" error. Feed 100
// bullets and assert no single Embed call ever exceeds 32 texts.
func TestSimilarCrossFilePairsChunksEmbedBatches(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	var b strings.Builder
	b.WriteString("# Identity\n")
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&b, "- fact number %d.\n", i)
	}
	if err := s.Write("identity.md", b.String()); err != nil {
		t.Fatalf("Write identity: %v", err)
	}
	if err := s.Write("projects.md", "# Projects\n- unrelated fact.\n"); err != nil {
		t.Fatalf("Write projects: %v", err)
	}

	emb := &batchRecorderEmbedder{}
	if _, err := s.SimilarCrossFilePairs(context.Background(), emb, 0, 0, 0.99); err != nil {
		t.Fatalf("SimilarCrossFilePairs: %v", err)
	}
	if len(emb.batchSizes) == 0 {
		t.Fatalf("expected at least one Embed call")
	}
	for _, n := range emb.batchSizes {
		if n > 32 {
			t.Fatalf("batch size %d exceeds 32", n)
		}
	}
}

func TestSimilarCrossFilePairsMaxFactsTooLarge(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	if err := s.Write("identity.md", "# Identity\n- A.\n- B.\n"); err != nil {
		t.Fatalf("Write identity: %v", err)
	}
	if err := s.Write("projects.md", "# Projects\n- C.\n- D.\n"); err != nil {
		t.Fatalf("Write projects: %v", err)
	}

	_, err := s.SimilarCrossFilePairs(context.Background(), rankStubEmbedder{}, 3, 0, 0)
	if !errors.Is(err, ErrVaultTooLarge) {
		t.Fatalf("expected ErrVaultTooLarge, got %v", err)
	}
}
