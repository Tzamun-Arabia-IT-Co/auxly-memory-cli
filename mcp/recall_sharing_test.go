package mcp

import (
	"context"
	"hash/fnv"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/sharing"
)

// stubEmbedder is a deterministic, OFFLINE embedder for tests: identical text
// always maps to the identical vector, so a query for a secret string ranks the
// chunk that contains that secret HIGHEST. That ordering is the whole point — it
// proves the ACL filter REMOVES the personal chunk (rather than ranking merely
// hiding it).
type stubEmbedder struct{ enabled bool }

func (e stubEmbedder) Enabled() bool    { return e.enabled }
func (e stubEmbedder) Model() string    { return "stub-embed" }
func (e stubEmbedder) Provider() string { return "stub" }

// stubDim is a small fixed dimensionality for the deterministic vectors.
const stubDim = 16

// Embed derives a deterministic unit-ish vector from a hash of each text. Two
// texts that share substrings still get distinct vectors, but the SAME text is
// always identical — and a query equal to a chunk's text yields an identical
// vector (cosine 1.0), guaranteeing that chunk tops the ranking.
func (e stubEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = stubVector(t)
	}
	return out, nil
}

// stubVector spreads a 64-bit FNV hash of s across stubDim components so equal
// strings produce equal vectors and different strings diverge.
func stubVector(s string) []float32 {
	vec := make([]float32, stubDim)
	for d := 0; d < stubDim; d++ {
		h := fnv.New64a()
		// Salt per-dimension so the components aren't all equal.
		_, _ = h.Write([]byte{byte(d)})
		_, _ = h.Write([]byte(s))
		// Map the hash to [-1, 1] deterministically.
		v := float64(h.Sum64()%2000)/1000.0 - 1.0
		vec[d] = float32(v)
	}
	// Guard against an accidental all-zero vector (cosine undefined).
	var sq float64
	for _, x := range vec {
		sq += float64(x) * float64(x)
	}
	if math.Sqrt(sq) == 0 {
		vec[0] = 1
	}
	return vec
}

// withStubEmbedder wires a server to use the deterministic stub embedder.
func withStubEmbedder(s *Server, enabled bool) {
	s.newEmbedder = func() memory.Embedder { return stubEmbedder{enabled: enabled} }
}

// 1. A remote read peer granted only projects.md must never have the personal
// secret surface through semantic recall — even though the stub ranks the
// personal chunk highest for that query.
func TestRemoteRecall_DoesNotLeakPersonal(t *testing.T) {
	s, secret := remoteServer(t, &sharing.ClientShare{SharedFiles: []string{"projects.md"}, Access: "read"})
	withStubEmbedder(s, true)

	out := resultText(s.toolRecall(secret))
	if strings.Contains(out, "personal.md") {
		t.Errorf("recall leaked personal.md to a remote peer: %q", out)
	}
	if strings.Contains(out, secret) {
		t.Errorf("recall leaked the secret value to a remote peer: %q", out)
	}

	// Sanity: recall for a granted-file term DOES return projects.md.
	if hit := resultText(s.toolRecall("launch")); !strings.Contains(hit, "projects.md") {
		t.Errorf("recall should still return granted projects.md, got: %q", hit)
	}
}

// 2. unified_memory.md concatenates every file (personal included); recall must
// never surface it even when it is nominally readable.
func TestRemoteRecall_ExcludesUnifiedMemory(t *testing.T) {
	s, secret := remoteServer(t, &sharing.ClientShare{SharedFiles: []string{"projects.md"}, Access: "read"})
	withStubEmbedder(s, true)

	// Write an aggregate that carries the secret.
	if err := os.WriteFile(filepath.Join(s.memoryPath, "unified_memory.md"),
		[]byte("# Unified\n"+secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	out := resultText(s.toolRecall(secret))
	if strings.Contains(out, "unified_memory.md") {
		t.Errorf("recall surfaced unified_memory.md: %q", out)
	}
	if strings.Contains(out, secret) {
		t.Errorf("recall leaked the secret via unified_memory.md: %q", out)
	}
}

// 3. The host owns the data: an explicit personal.md grant means recall for the
// secret DOES return personal.md.
func TestRemoteRecall_ServedWhenExplicitlyGranted(t *testing.T) {
	s, secret := remoteServer(t, &sharing.ClientShare{
		SharedFiles: []string{"projects.md", "personal.md"},
		WriteFiles:  []string{"personal.md"},
	})
	withStubEmbedder(s, true)

	out := resultText(s.toolRecall(secret))
	if !strings.Contains(out, "personal.md") {
		t.Errorf("an explicit personal grant must make recall return personal.md, got: %q", out)
	}
}

// 4. Offline (no embedding model) must fall back to substring search, never
// hard-fail — and must still respect the ACL (personal.md stays hidden).
func TestRecall_OfflineFallsBackToSubstring(t *testing.T) {
	s, secret := remoteServer(t, &sharing.ClientShare{SharedFiles: []string{"projects.md"}, Access: "read"})
	withStubEmbedder(s, false) // disabled embedder

	recallOut := resultText(s.toolRecall("launch"))
	searchOut := resultText(s.toolSearch("launch"))
	if recallOut != searchOut {
		t.Errorf("offline recall must equal substring search.\nrecall: %q\nsearch: %q", recallOut, searchOut)
	}
	if !strings.Contains(recallOut, "projects.md") {
		t.Errorf("offline recall should return granted projects.md, got: %q", recallOut)
	}

	// The ACL still holds on the fallback path.
	if out := resultText(s.toolRecall(secret)); strings.Contains(out, "personal.md") {
		t.Errorf("offline recall fallback leaked personal.md: %q", out)
	}
}

// 5. A local (non-remote) server has full access: recall for the secret returns
// personal.md.
func TestLocalRecall_FullAccess(t *testing.T) {
	dir := t.TempDir()
	const secret = "SECRET_SALARY_FIGURE"
	if err := os.WriteFile(filepath.Join(dir, "personal.md"), []byte("# Personal\n"+secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "projects.md"), []byte("# Projects\nauxly launch notes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewServer(dir)
	s.store.WorkspaceRoot = ""
	withStubEmbedder(s, true)

	out := resultText(s.toolRecall(secret))
	if !strings.Contains(out, "personal.md") {
		t.Errorf("local recall should return personal.md, got: %q", out)
	}
}
