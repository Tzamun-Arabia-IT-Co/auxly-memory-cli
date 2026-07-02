package memory

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/embed"
)

// TestRecallObserverEmitsOnSuccess locks the RecallEvent contract: on a
// successful recall the observer receives one event whose QueryHash is a
// hash (never the raw query), whose hits are ranked 0..n-1 in order, and
// whose Accepted flag reflects the top-k cut — not just "scored above floor".
func TestRecallObserverEmitsOnSuccess(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AUXLY_RECALL_MIN_SCORE", "0.5")
	writeFile(t, root, "a.md", "# A\n\n- best match line.\n")
	writeFile(t, root, "b.md", "# B\n\n- second match line.\n")
	s := &Store{Root: root}
	emb := rankStubEmbedder{vecs: map[string][]float32{
		"query":                {1, 0, 0, 0},
		"- best match line.":   {1, 0.01, 0, 0}, // cosine ≈ 0.9999
		"- second match line.": {1, 0.5, 0, 0},  // cosine ≈ 0.894 — above the 0.5 floor
	}}

	var got *RecallEvent
	s.RecallObserver = func(ev RecallEvent) {
		if got != nil {
			t.Fatalf("observer called more than once")
		}
		e := ev
		got = &e
	}

	const query = "query"
	// k=1 with 2 above-threshold candidates forces a top-k cut.
	hits, err := s.Recall(context.Background(), query, 1, emb, nil)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("want 1 hit (k=1), got %d", len(hits))
	}
	if got == nil {
		t.Fatalf("observer was not called")
	}
	if got.QueryHash != HashRecallText(query) {
		t.Fatalf("QueryHash mismatch: got %s want %s", got.QueryHash, HashRecallText(query))
	}
	if got.QueryHash == query {
		t.Fatalf("QueryHash equals raw query text")
	}
	if got.Fallback {
		t.Fatalf("Fallback should be false on the semantic path")
	}
	if len(got.Hits) != 2 {
		t.Fatalf("want 2 scored candidates in event, got %d", len(got.Hits))
	}
	for i, h := range got.Hits {
		if h.Rank != i {
			t.Fatalf("hit %d: Rank = %d, want %d", i, h.Rank, i)
		}
		if h.LineHash == "" {
			t.Fatalf("hit %d: empty LineHash", i)
		}
		if h.LineHash == "- best match line." || h.LineHash == "- second match line." {
			t.Fatalf("hit %d: LineHash is raw chunk text", i)
		}
	}
	if !got.Hits[0].Accepted {
		t.Fatalf("top-ranked hit should be Accepted")
	}
	if got.Hits[1].Accepted {
		t.Fatalf("scored-but-cut candidate (rank 1, k=1) should not be Accepted")
	}
}

// TestRecallNilObserverSafe locks nil-safety: an unset RecallObserver must
// not change Recall's behavior in any way.
func TestRecallNilObserverSafe(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AUXLY_RECALL_MIN_SCORE", "0")
	writeFile(t, root, "a.md", "# A\n\n- alpha line.\n")
	s := &Store{Root: root} // RecallObserver left nil
	emb := rankStubEmbedder{vecs: map[string][]float32{
		"query":         {1, 0, 0, 0},
		"- alpha line.": {1, 0, 0, 0},
	}}

	hits, err := s.Recall(context.Background(), "query", 8, emb, nil)
	if err != nil {
		t.Fatalf("Recall with nil observer: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("want 1 hit, got %d", len(hits))
	}
}

// TestRecallObserverNotCalledOnThresholdError locks the skip: when every
// candidate scores below the relevance floor, ranked is empty and the
// observer must not fire (there is nothing worth an event).
func TestRecallObserverNotCalledOnThresholdError(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AUXLY_RECALL_MIN_SCORE", "0.99")
	writeFile(t, root, "a.md", "# A\n\n- weak line.\n")
	s := &Store{Root: root}
	emb := rankStubEmbedder{vecs: map[string][]float32{
		"query":        {1, 0, 0, 0},
		"- weak line.": {0.1, 1, 0, 0}, // cosine well below 0.99
	}}

	called := false
	s.RecallObserver = func(RecallEvent) { called = true }

	if _, err := s.Recall(context.Background(), "query", 8, emb, nil); !errors.Is(err, embed.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
	if called {
		t.Fatalf("observer called on threshold-error path")
	}
}

// TestRecallEventNeverCarriesRawQueryText is the privacy regression: walk
// every string field reachable from the emitted RecallEvent and confirm the
// raw query text is not present anywhere in it (only its hash may appear).
func TestRecallEventNeverCarriesRawQueryText(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AUXLY_RECALL_MIN_SCORE", "0")
	const query = "super-secret-query-token"
	writeFile(t, root, "a.md", "# A\n\n- alpha line.\n")
	s := &Store{Root: root}
	emb := rankStubEmbedder{vecs: map[string][]float32{
		query:           {1, 0, 0, 0},
		"- alpha line.": {1, 0, 0, 0},
	}}

	var got RecallEvent
	s.RecallObserver = func(ev RecallEvent) { got = ev }

	if _, err := s.Recall(context.Background(), query, 8, emb, nil); err != nil {
		t.Fatalf("Recall: %v", err)
	}

	walkStrings(reflect.ValueOf(got), func(v string) {
		if strings.Contains(v, query) {
			t.Fatalf("raw query text leaked into RecallEvent field: %q", v)
		}
	})
}

// TestBulletHashesAcceptsStarBullets is Finding 3's regression: bulletHashes
// must accept "* " bullets exactly like isBulletLine (decay.go) does — a "*"
// fact that never accrues a recall hash looks permanently unrecalled and gets
// archived out from under active use.
func TestBulletHashesAcceptsStarBullets(t *testing.T) {
	chunk := "* First star fact\n* Second star fact\n"
	got := bulletHashes(chunk)
	want := []string{
		HashRecallText("* First star fact"),
		HashRecallText("* Second star fact"),
	}
	if len(got) != len(want) {
		t.Fatalf("bulletHashes(%q) = %v, want %v", chunk, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("bulletHashes[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// walkStrings recursively visits every string reachable from v (struct
// fields, slice/array elements, pointers/interfaces).
func walkStrings(v reflect.Value, visit func(string)) {
	switch v.Kind() {
	case reflect.String:
		visit(v.String())
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			walkStrings(v.Field(i), visit)
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			walkStrings(v.Index(i), visit)
		}
	case reflect.Ptr, reflect.Interface:
		if !v.IsNil() {
			walkStrings(v.Elem(), visit)
		}
	}
}
