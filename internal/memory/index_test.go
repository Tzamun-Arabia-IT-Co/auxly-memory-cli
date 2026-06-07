package memory

import (
	"path/filepath"
	"testing"
)

// baseMeta is a convenience constructor for a known embedding space.
func baseMeta(dim int) IndexMeta {
	return IndexMeta{
		Provider:           "ollama",
		Model:              "nomic-embed-text",
		Dim:                dim,
		EmbedFormatVersion: embedFormatVersion,
		ChunkerVersion:     chunkerVersion,
	}
}

// sampleChunk builds a Chunk with a deterministic hash for a given file/text.
func sampleChunk(file, heading, text string, start, end int) Chunk {
	return Chunk{
		File:      file,
		Heading:   heading,
		Text:      text,
		LineStart: start,
		LineEnd:   end,
		Hash:      hashText(text),
	}
}

func openTestIndex(t *testing.T, want IndexMeta) (*Index, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "index.db")
	ix, err := OpenIndex(path, want)
	if err != nil {
		t.Fatalf("OpenIndex: %v", err)
	}
	t.Cleanup(func() { ix.Close() })
	return ix, path
}

// 1. Open creates schema + writes meta; Meta() returns what was passed.
func TestIndexOpenWritesMeta(t *testing.T) {
	want := baseMeta(768)
	ix, _ := openTestIndex(t, want)

	got := ix.Meta()
	if got != want {
		t.Fatalf("Meta() = %+v, want %+v", got, want)
	}
}

// 2. Put + Load roundtrip with byte-exact vector equality.
func TestIndexPutLoadRoundtrip(t *testing.T) {
	ix, _ := openTestIndex(t, baseMeta(3))

	c1 := sampleChunk("a/x.md", "H1", "hello world", 1, 2)
	c2 := sampleChunk("a/x.md", "H2", "second chunk", 4, 5)
	v1 := []float32{0.5, -1.25, 3.0}
	v2 := []float32{-0.001, 2.5, 100.75}

	if err := ix.Put("a/x.md", c1, v1); err != nil {
		t.Fatalf("Put c1: %v", err)
	}
	if err := ix.Put("a/x.md", c2, v2); err != nil {
		t.Fatalf("Put c2: %v", err)
	}

	got, err := ix.Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Load returned %d chunks, want 2", len(got))
	}

	byHash := map[string]IndexedChunk{}
	for _, ic := range got {
		byHash[ic.Hash] = ic
	}

	assertChunk := func(c Chunk, want []float32) {
		ic, ok := byHash[c.Hash]
		if !ok {
			t.Fatalf("missing chunk hash %s", c.Hash)
		}
		if ic.ScopeKey != "a/x.md" {
			t.Errorf("ScopeKey = %q, want a/x.md", ic.ScopeKey)
		}
		if ic.File != c.File || ic.Heading != c.Heading || ic.Text != c.Text ||
			ic.LineStart != c.LineStart || ic.LineEnd != c.LineEnd {
			t.Errorf("chunk fields mismatch: got %+v want %+v", ic.Chunk, c)
		}
		if len(ic.Vector) != len(want) {
			t.Fatalf("vector len %d, want %d", len(ic.Vector), len(want))
		}
		for i := range want {
			if ic.Vector[i] != want[i] {
				t.Errorf("vector[%d] = %v, want %v", i, ic.Vector[i], want[i])
			}
		}
	}
	assertChunk(c1, v1)
	assertChunk(c2, v2)
}

// 3. Incremental Has/Put: dup (scope,hash) is a no-op; new hash adds a row.
func TestIndexIncrementalHasPut(t *testing.T) {
	ix, _ := openTestIndex(t, baseMeta(2))

	c := sampleChunk("s.md", "H", "alpha", 1, 1)
	if err := ix.Put("s", c, []float32{1, 2}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	has, err := ix.Has("s", c.Hash)
	if err != nil {
		t.Fatalf("Has: %v", err)
	}
	if !has {
		t.Fatal("Has = false, want true after Put")
	}

	// Same (scope, hash) again → still one row.
	if err := ix.Put("s", c, []float32{9, 9}); err != nil {
		t.Fatalf("Put dup: %v", err)
	}
	if n := countRows(t, ix); n != 1 {
		t.Fatalf("row count = %d after dup Put, want 1", n)
	}

	// Different hash adds a row.
	c2 := sampleChunk("s.md", "H", "beta", 2, 2)
	if err := ix.Put("s", c2, []float32{3, 4}); err != nil {
		t.Fatalf("Put c2: %v", err)
	}
	if n := countRows(t, ix); n != 2 {
		t.Fatalf("row count = %d, want 2", n)
	}

	missing, err := ix.Has("s", "nope")
	if err != nil {
		t.Fatalf("Has missing: %v", err)
	}
	if missing {
		t.Fatal("Has(nonexistent) = true, want false")
	}
}

// 4. Dim guard: Put with wrong vector length errors.
func TestIndexPutDimGuard(t *testing.T) {
	ix, _ := openTestIndex(t, baseMeta(3))
	c := sampleChunk("s.md", "", "x", 1, 1)
	if err := ix.Put("s", c, []float32{1, 2}); err == nil {
		t.Fatal("Put with wrong dim returned nil error, want error")
	}
}

// 5. Meta mismatch invalidates the cache.
func TestIndexMetaMismatchInvalidates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")

	ix, err := OpenIndex(path, baseMeta(768))
	if err != nil {
		t.Fatalf("OpenIndex: %v", err)
	}
	c := sampleChunk("s.md", "", "x", 1, 1)
	vec := make([]float32, 768)
	if err := ix.Put("s", c, vec); err != nil {
		t.Fatalf("Put: %v", err)
	}
	ix.Close()

	ix2, err := OpenIndex(path, baseMeta(1024))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer ix2.Close()

	got, err := ix2.Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Load returned %d after meta mismatch, want 0", len(got))
	}
	if ix2.Meta().Dim != 1024 {
		t.Fatalf("Meta().Dim = %d, want 1024", ix2.Meta().Dim)
	}
}

// 6. Meta match preserves the cache.
func TestIndexMetaMatchPreserves(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")

	ix, err := OpenIndex(path, baseMeta(3))
	if err != nil {
		t.Fatalf("OpenIndex: %v", err)
	}
	c := sampleChunk("s.md", "", "keep me", 1, 1)
	if err := ix.Put("s", c, []float32{1, 2, 3}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	ix.Close()

	ix2, err := OpenIndex(path, baseMeta(3))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer ix2.Close()

	got, err := ix2.Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Load returned %d, want 1 (preserved)", len(got))
	}
}

// 7. Load allow-filter — the ACL hook Phase 2 depends on.
func TestIndexLoadAllowFilter(t *testing.T) {
	ix, _ := openTestIndex(t, baseMeta(1))

	ca := sampleChunk("a/x.md", "", "from a", 1, 1)
	cb := sampleChunk("b/y.md", "", "from b", 1, 1)
	if err := ix.Put("a/x.md", ca, []float32{1}); err != nil {
		t.Fatalf("Put a: %v", err)
	}
	if err := ix.Put("b/y.md", cb, []float32{2}); err != nil {
		t.Fatalf("Put b: %v", err)
	}

	got, err := ix.Load(func(sk, _ string) bool { return sk == "a/x.md" })
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("filtered Load returned %d, want 1", len(got))
	}
	if got[0].ScopeKey != "a/x.md" {
		t.Fatalf("filtered ScopeKey = %q, want a/x.md", got[0].ScopeKey)
	}
}

// 8. PruneExcept removes hashes not in the keep set.
func TestIndexPruneExcept(t *testing.T) {
	ix, _ := openTestIndex(t, baseMeta(1))

	h1 := sampleChunk("s.md", "", "one", 1, 1)
	h2 := sampleChunk("s.md", "", "two", 2, 2)
	h3 := sampleChunk("s.md", "", "three", 3, 3)
	for _, c := range []Chunk{h1, h2, h3} {
		if err := ix.Put("s", c, []float32{1}); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}

	keep := map[string]bool{h1.Hash: true, h3.Hash: true}
	if err := ix.PruneExcept("s", keep); err != nil {
		t.Fatalf("PruneExcept: %v", err)
	}

	got, err := ix.Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("after prune Load returned %d, want 2", len(got))
	}
	for _, ic := range got {
		if ic.Hash == h2.Hash {
			t.Fatal("h2 survived PruneExcept")
		}
	}
}

// 9. decodeVector rejects a byte slice whose length is not divisible by 4.
func TestDecodeVectorRejectsBadLength(t *testing.T) {
	if _, err := decodeVector([]byte{1, 2, 3}); err == nil {
		t.Fatal("decodeVector(len 3) returned nil error, want error")
	}
	if _, err := decodeVector([]byte{1, 2, 3, 4, 5}); err == nil {
		t.Fatal("decodeVector(len 5) returned nil error, want error")
	}
	// sanity: a valid multiple of 4 decodes.
	if _, err := decodeVector(encodeVector([]float32{1.5, -2.5})); err != nil {
		t.Fatalf("decodeVector of valid blob errored: %v", err)
	}
}

// countRows reports the total number of stored chunk rows (test helper).
func countRows(t *testing.T, ix *Index) int {
	t.Helper()
	var n int
	if err := ix.db.QueryRow("SELECT COUNT(*) FROM chunks").Scan(&n); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return n
}
