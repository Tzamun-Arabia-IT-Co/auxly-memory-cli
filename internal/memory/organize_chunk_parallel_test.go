package memory

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestChunkedOrganizeRunsConcurrently proves Optimization 2: per-file calls
// overlap in wall-clock time instead of running one after another. Each stub
// call blocks until it observes at least 2 CONCURRENT in-flight calls (or a
// timeout), which only a genuinely parallel dispatch can satisfy for a
// 3-file plan under organizeChunkWorkers() >= 2.
func TestChunkedOrganizeRunsConcurrently(t *testing.T) {
	if organizeChunkWorkers() < 2 {
		t.Skip("single-core runner: parallelism cannot be observed")
	}
	root := t.TempDir()
	s := &Store{Root: root}
	writeVaultFile(t, root, "identity.md", "- a\n")
	writeVaultFile(t, root, "infra.md", "- b\n")
	writeVaultFile(t, root, "projects.md", "- c\n")
	t.Setenv("AUXLY_ORGANIZE_CHUNK_TOKENS", "1")

	var inFlight int32
	var maxSeen int32
	exec := func(ctx context.Context, sys, user string) (organizeRun, OrganizeResult, bool) {
		n := atomic.AddInt32(&inFlight, 1)
		defer atomic.AddInt32(&inFlight, -1)
		for i := 0; i < 50; i++ { // give sibling workers a chance to start
			if cur := atomic.LoadInt32(&maxSeen); n > cur {
				atomic.CompareAndSwapInt32(&maxSeen, cur, n)
			}
			if atomic.LoadInt32(&inFlight) >= 2 {
				break
			}
			time.Sleep(2 * time.Millisecond)
		}
		name := "identity.md"
		switch {
		case strings.Contains(user, "infra.md"):
			name = "infra.md"
		case strings.Contains(user, "projects.md"):
			name = "projects.md"
		}
		return organizeRun{jsonContent: chunkResp(name, "- tidied\n"), modelUsed: "fake"}, OrganizeResult{}, true
	}

	_, res := s.planOrganize(context.Background(), "", false, exec)
	if !res.Success {
		t.Fatalf("plan failed: %s", res.Message)
	}
	if atomic.LoadInt32(&maxSeen) < 2 {
		t.Fatalf("expected at least 2 concurrent calls, max observed = %d", maxSeen)
	}
}

// TestChunkedOrganizeDeterministicOrdering proves results are merged by
// INDEX, not completion order: the slowest file (identity.md) still lands
// first in prop.Changes because it was first in the input file list.
func TestChunkedOrganizeDeterministicOrdering(t *testing.T) {
	root := t.TempDir()
	s := &Store{Root: root}
	writeVaultFile(t, root, "identity.md", "- a\n")
	writeVaultFile(t, root, "infra.md", "- b\n")
	writeVaultFile(t, root, "projects.md", "- c\n")
	t.Setenv("AUXLY_ORGANIZE_CHUNK_TOKENS", "1")

	var called sync.Map
	exec := func(ctx context.Context, sys, user string) (organizeRun, OrganizeResult, bool) {
		switch {
		case strings.Contains(user, "identity.md"):
			called.Store("identity.md", true)
			time.Sleep(30 * time.Millisecond) // finishes LAST despite being first in the list
			return organizeRun{jsonContent: chunkResp("identity.md", "- a tidied\n"), modelUsed: "fake"}, OrganizeResult{}, true
		case strings.Contains(user, "infra.md"):
			called.Store("infra.md", true)
			return organizeRun{jsonContent: chunkResp("infra.md", "- b tidied\n"), modelUsed: "fake"}, OrganizeResult{}, true
		default:
			called.Store("projects.md", true)
			return organizeRun{jsonContent: chunkResp("projects.md", "- c tidied\n"), modelUsed: "fake"}, OrganizeResult{}, true
		}
	}

	prop, res := s.planOrganize(context.Background(), "", false, exec)
	if !res.Success {
		t.Fatalf("plan failed: %s", res.Message)
	}
	for _, name := range []string{"identity.md", "infra.md", "projects.md"} {
		if _, ok := called.Load(name); !ok {
			t.Fatalf("%s was never called", name)
		}
	}
	if len(prop.Changes) != 3 {
		t.Fatalf("expected 3 changes, got %d", len(prop.Changes))
	}
	got := []string{prop.Changes[0].Name, prop.Changes[1].Name, prop.Changes[2].Name}
	want := []string{"identity.md", "infra.md", "projects.md"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("output order not deterministic by index: got %v, want %v", got, want)
		}
	}
}

// TestChunkedOrganizeOneErrorOthersStillApplied is the exact scenario from
// the optimization spec: a 3-file chunked plan where one file's call errors
// must still call all 3, and the other two must be present and applied in
// the resulting proposal.
func TestChunkedOrganizeOneErrorOthersStillApplied(t *testing.T) {
	root := t.TempDir()
	s := &Store{Root: root}
	writeVaultFile(t, root, "identity.md", "- a\n")
	writeVaultFile(t, root, "infra.md", "- b\n")
	writeVaultFile(t, root, "projects.md", "- c\n")
	t.Setenv("AUXLY_ORGANIZE_CHUNK_TOKENS", "1")

	var calledCount int32
	exec := func(ctx context.Context, sys, user string) (organizeRun, OrganizeResult, bool) {
		atomic.AddInt32(&calledCount, 1)
		if strings.Contains(user, "infra.md") {
			return organizeRun{}, OrganizeResult{Success: false, Message: "model exploded"}, false
		}
		name := "identity.md"
		if strings.Contains(user, "projects.md") {
			name = "projects.md"
		}
		return organizeRun{jsonContent: chunkResp(name, "- tidied\n"), modelUsed: "fake"}, OrganizeResult{}, true
	}

	prop, res := s.planOrganize(context.Background(), "", false, exec)
	if atomic.LoadInt32(&calledCount) != 3 {
		t.Fatalf("expected all 3 files called, got %d", calledCount)
	}
	if !res.Success {
		t.Fatalf("a single file error must not fail the whole plan: %s", res.Message)
	}
	names := map[string]bool{}
	for _, c := range prop.Changes {
		names[c.Name] = true
	}
	if !names["identity.md"] || !names["projects.md"] {
		t.Fatalf("the two successful files must still be applied, got %+v", prop.Changes)
	}
	if names["infra.md"] {
		t.Fatalf("the failing file must not appear in the proposal, got %+v", prop.Changes)
	}
	if !strings.Contains(prop.Warning, "infra.md") {
		t.Fatalf("the failure should be reported and name the file: %s", prop.Warning)
	}
}
