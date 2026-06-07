package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestPruneScopesExceptDeletesNotIn proves the direct NOT-IN sweep: chunks whose
// scope_key is absent from keep are deleted, while kept scopes survive.
func TestPruneScopesExceptDeletesNotIn(t *testing.T) {
	ix, _ := openTestIndex(t, baseMeta(3))

	put := func(scope, text string) {
		c := sampleChunk("f.md", "H", text, 1, 2)
		if err := ix.Put(scope, c, []float32{1, 0, 0}); err != nil {
			t.Fatalf("Put(%s): %v", scope, err)
		}
	}
	put("/vault/keep.md", "alpha")
	put("/vault/orphan.md", "beta")
	put("/vault/also-keep.md", "gamma")

	keep := map[string]bool{"/vault/keep.md": true, "/vault/also-keep.md": true}
	if err := ix.PruneScopesExcept(keep); err != nil {
		t.Fatalf("PruneScopesExcept: %v", err)
	}

	got := loadScopes(t, ix)
	if got["/vault/orphan.md"] {
		t.Fatalf("orphan scope was not pruned: %v", got)
	}
	if !got["/vault/keep.md"] || !got["/vault/also-keep.md"] {
		t.Fatalf("kept scopes were wrongly pruned: %v", got)
	}
}

// TestPruneScopesExceptEmptyDeletesAll proves an empty keep set clears the table.
func TestPruneScopesExceptEmptyDeletesAll(t *testing.T) {
	ix, _ := openTestIndex(t, baseMeta(3))
	c := sampleChunk("f.md", "H", "x", 1, 2)
	if err := ix.Put("/vault/a.md", c, []float32{1, 0, 0}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if err := ix.PruneScopesExcept(map[string]bool{}); err != nil {
		t.Fatalf("PruneScopesExcept(empty): %v", err)
	}
	if n, err := ix.Count(); err != nil || n != 0 {
		t.Fatalf("expected 0 chunks after empty prune, got %d (err=%v)", n, err)
	}
}

// TestPruneScopesExceptManyScopes exercises the >999 batching path so a large
// keep set does not blow the SQLite parameter limit.
func TestPruneScopesExceptManyScopes(t *testing.T) {
	ix, _ := openTestIndex(t, baseMeta(3))
	keep := map[string]bool{}
	for i := 0; i < 1500; i++ {
		scope := "/vault/keep-" + itoa(i) + ".md"
		c := sampleChunk("f.md", "H", "k"+itoa(i), 1, 2)
		if err := ix.Put(scope, c, []float32{1, 0, 0}); err != nil {
			t.Fatalf("Put: %v", err)
		}
		keep[scope] = true
	}
	orphan := sampleChunk("f.md", "H", "orphan-text", 1, 2)
	if err := ix.Put("/vault/orphan.md", orphan, []float32{1, 0, 0}); err != nil {
		t.Fatalf("Put orphan: %v", err)
	}

	if err := ix.PruneScopesExcept(keep); err != nil {
		t.Fatalf("PruneScopesExcept(many): %v", err)
	}
	got := loadScopes(t, ix)
	if got["/vault/orphan.md"] {
		t.Fatalf("orphan survived large-keep prune")
	}
	if len(got) != 1500 {
		t.Fatalf("expected 1500 kept scopes, got %d", len(got))
	}
}

func loadScopes(t *testing.T, ix *Index) map[string]bool {
	t.Helper()
	chunks, err := ix.Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	out := map[string]bool{}
	for _, c := range chunks {
		out[c.ScopeKey] = true
	}
	return out
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// TestRefreshIndexPrunesDeletedFile proves the refreshIndex-level sweep: a file
// removed from the vault has ALL its chunks pruned on the next refresh, and
// recall no longer returns its content.
func TestRefreshIndexPrunesDeletedFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.md", "# A\n\n- alpha content lives here.\n")
	writeFile(t, root, "b.md", "# B\n\n- beta content lives here.\n")
	s := &Store{Root: root}
	emb := adminStubEmbedder{enabled: true}

	if _, err := s.RebuildIndex(context.Background(), emb); err != nil {
		t.Fatalf("RebuildIndex: %v", err)
	}
	if !scopePresent(t, s, hasFileChunks("b.md")) {
		t.Fatalf("expected b.md chunks after initial build")
	}

	// Delete b.md from the vault, then drive the INCREMENTAL refresh via Recall
	// (which does NOT wipe the DB first, unlike RebuildIndex). The orphaned b.md
	// scope must be swept by refreshIndex's PruneScopesExcept.
	if err := os.Remove(filepath.Join(root, "b.md")); err != nil {
		t.Fatalf("remove b.md: %v", err)
	}
	hits, err := s.Recall(context.Background(), "beta content", 8, emb, nil)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}

	if scopePresent(t, s, hasFileChunks("b.md")) {
		t.Fatalf("b.md chunks were NOT pruned after deletion")
	}
	if !scopePresent(t, s, hasFileChunks("a.md")) {
		t.Fatalf("a.md chunks were wrongly pruned")
	}

	// Recall must not surface b.md content any more.
	for _, h := range hits {
		if h.File == "b.md" || strings.Contains(h.Text, "beta content") {
			t.Fatalf("recall returned stale b.md content: %+v", h)
		}
	}
}

// TestRefreshIndexShadowedGlobalPruned proves the workspace-over-global shadow
// case: when List() returns the workspace projects.md, the previously-indexed
// GLOBAL projects.md scope_key is swept and only the workspace content remains.
func TestRefreshIndexShadowedGlobalPruned(t *testing.T) {
	global := t.TempDir()
	workspace := t.TempDir()
	writeFile(t, global, "projects.md", "# Projects\n\n- GLOBAL secret stale project.\n")
	writeFile(t, workspace, "projects.md", "# Projects\n\n- WORKSPACE fresh project.\n")

	emb := adminStubEmbedder{enabled: true}

	// Phase 1: index the GLOBAL projects.md alone (no workspace yet).
	globalOnly := &Store{Root: global}
	if _, err := globalOnly.RebuildIndex(context.Background(), emb); err != nil {
		t.Fatalf("RebuildIndex(global): %v", err)
	}
	globalScope := filepath.Join(global, "projects.md")
	if !scopePresent(t, globalOnly, hasScope(globalScope)) {
		t.Fatalf("expected global projects.md scope after phase 1")
	}

	// Phase 2: the same vault now has a workspace projects.md shadowing the global.
	// List() will return the WORKSPACE Path as the scope_key, so the global scope
	// is orphaned and must be swept.
	shadowed := &Store{Root: global, WorkspaceRoot: workspace}
	ctx := context.Background()
	dim := probeDim(t, ctx, emb)
	ix, err := OpenIndex(shadowed.indexDBPath(), IndexMeta{
		Provider: emb.Provider(), Model: emb.Model(), Dim: dim,
		EmbedFormatVersion: embedFormatVersion, ChunkerVersion: chunkerVersion,
	})
	if err != nil {
		t.Fatalf("OpenIndex(shadowed): %v", err)
	}
	shadowed.refreshIndex(ctx, ix, emb)
	ix.Close()

	workspaceScope := filepath.Join(workspace, "projects.md")
	if !scopePresent(t, shadowed, hasScope(workspaceScope)) {
		t.Fatalf("expected workspace projects.md scope after shadow refresh")
	}
	if scopePresent(t, shadowed, hasScope(globalScope)) {
		t.Fatalf("stale GLOBAL projects.md scope was NOT swept after shadowing")
	}

	// Load returns only the workspace content, never the stale global text.
	reopened, err := OpenIndex(shadowed.indexDBPath(), ix.Meta())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	chunks, err := reopened.Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, c := range chunks {
		if strings.Contains(c.Text, "GLOBAL secret") {
			t.Fatalf("Load returned stale global content: %q", c.Text)
		}
	}
}

// TestRefreshIndexNoFalsePrune proves an unchanged vault refreshed twice keeps
// every chunk (keep set includes all current scope_keys).
func TestRefreshIndexNoFalsePrune(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.md", "# A\n\n- alpha stays.\n")
	writeFile(t, root, "b.md", "# B\n\n- beta stays.\n")
	s := &Store{Root: root}
	emb := adminStubEmbedder{enabled: true}

	if _, err := s.RebuildIndex(context.Background(), emb); err != nil {
		t.Fatalf("RebuildIndex #1: %v", err)
	}
	first := countChunks(t, s)
	if _, err := s.RebuildIndex(context.Background(), emb); err != nil {
		t.Fatalf("RebuildIndex #2: %v", err)
	}
	second := countChunks(t, s)
	if first == 0 || first != second {
		t.Fatalf("false prune: chunk count changed %d -> %d", first, second)
	}
}

// TestWALEngaged confirms OpenIndex actually switches the journal to WAL.
func TestWALEngaged(t *testing.T) {
	ix, _ := openTestIndex(t, baseMeta(3))
	var mode string
	if err := ix.db.QueryRow("PRAGMA journal_mode;").Scan(&mode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
}

// TestWALConcurrentPutLoad proves WAL + busy_timeout let a concurrent writer and
// reader coexist without "database is locked".
func TestWALConcurrentPutLoad(t *testing.T) {
	ix, _ := openTestIndex(t, baseMeta(3))

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			c := sampleChunk("f.md", "H", "w"+itoa(i), 1, 2)
			if err := ix.Put("/vault/w.md", c, []float32{1, 0, 0}); err != nil {
				errs <- err
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			if _, err := ix.Load(nil); err != nil {
				errs <- err
				return
			}
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil && strings.Contains(err.Error(), "locked") {
			t.Fatalf("database is locked under WAL: %v", err)
		} else if err != nil {
			t.Fatalf("concurrent op error: %v", err)
		}
	}
}

// --- helpers shared by the prune/shadow tests ---

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func probeDim(t *testing.T, ctx context.Context, emb Embedder) int {
	t.Helper()
	vecs, err := emb.Embed(ctx, []string{"probe"})
	if err != nil || len(vecs) == 0 {
		t.Fatalf("probe embed: %v", err)
	}
	return len(vecs[0])
}

func countChunks(t *testing.T, s *Store) int {
	t.Helper()
	ix, err := OpenIndexReadOnly(s.indexDBPath())
	if err != nil {
		t.Fatalf("open ro: %v", err)
	}
	defer ix.Close()
	n, err := ix.Count()
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// scopePresent opens the store's index read-only and reports whether any loaded
// chunk satisfies match.
func scopePresent(t *testing.T, s *Store, match func(IndexedChunk) bool) bool {
	t.Helper()
	ix, err := OpenIndexReadOnly(s.indexDBPath())
	if err != nil {
		t.Fatalf("open ro: %v", err)
	}
	defer ix.Close()
	chunks, err := ix.Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, c := range chunks {
		if match(c) {
			return true
		}
	}
	return false
}

func hasFileChunks(file string) func(IndexedChunk) bool {
	return func(c IndexedChunk) bool { return c.File == file }
}

func hasScope(scope string) func(IndexedChunk) bool {
	return func(c IndexedChunk) bool { return c.ScopeKey == scope }
}
