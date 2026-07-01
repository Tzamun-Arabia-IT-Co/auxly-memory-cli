package memory

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestAtomicWriteSurvivesInterrupt: readers racing an atomic overwrite must only
// ever observe the complete old content or the complete new content — never a
// truncated or mixed file (the failure mode of plain os.WriteFile).
func TestAtomicWriteSurvivesInterrupt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "identity.md")

	oldContent := strings.Repeat("- old fact line\n", 500)
	newContent := strings.Repeat("- NEW fact line\n", 500)
	if err := AtomicWriteFile(path, []byte(oldContent), 0644); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	var bad string
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			data, err := os.ReadFile(path)
			if err != nil {
				continue // rename window on some platforms; missing ≠ torn
			}
			s := string(data)
			if s != oldContent && s != newContent {
				mu.Lock()
				bad = s[:min(80, len(s))]
				mu.Unlock()
				return
			}
		}
	}()

	for i := 0; i < 200; i++ {
		content := oldContent
		if i%2 == 0 {
			content = newContent
		}
		if err := AtomicWriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()

	if bad != "" {
		t.Fatalf("reader observed torn content: %q...", bad)
	}

	// No temp droppings left behind.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".auxly-tmp-") {
			t.Fatalf("leftover temp file: %s", e.Name())
		}
	}
}

func TestAtomicWriteCreatesAndOverwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prefs.md")

	if err := AtomicWriteFile(path, []byte("v1"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWriteFile(path, []byte("v2"), 0644); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "v2" {
		t.Fatalf("got %q, want v2", data)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
