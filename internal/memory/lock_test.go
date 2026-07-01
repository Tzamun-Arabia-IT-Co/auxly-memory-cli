package memory

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

// TestLockVaultSerializesGoroutines: N goroutines doing read-modify-write on one
// file under LockVault must lose no increments.
func TestLockVaultSerializesGoroutines(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "counter.md")
	os.WriteFile(path, []byte("0"), 0644)

	const n = 25
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock, err := LockVault(root)
			if err != nil {
				t.Error(err)
				return
			}
			defer unlock()
			data, _ := os.ReadFile(path)
			v, _ := strconv.Atoi(string(data))
			if err := AtomicWriteFile(path, []byte(strconv.Itoa(v+1)), 0644); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()

	data, _ := os.ReadFile(path)
	if string(data) != strconv.Itoa(n) {
		t.Fatalf("lost increments: got %s, want %d", data, n)
	}
}

// TestLockVaultStaleTakeover: a lock file left by a crashed process (old mtime)
// must not block writers forever.
func TestLockVaultStaleTakeover(t *testing.T) {
	root := t.TempDir()
	lockPath := filepath.Join(root, lockFileName)
	os.WriteFile(lockPath, []byte("99999 crashed\n"), 0600)
	old := time.Now().Add(-2 * lockStaleAfter)
	os.Chtimes(lockPath, old, old)

	start := time.Now()
	unlock, err := LockVault(root)
	if err != nil {
		t.Fatalf("stale lock not taken over: %v", err)
	}
	unlock()
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("takeover too slow: %v", elapsed)
	}
}

// TestLockVaultBlocksOtherProcess: while THIS process holds the vault lock, a
// second process must wait; its write lands only after release. Re-execs the
// test binary as the second process (helper below).
func TestLockVaultBlocksOtherProcess(t *testing.T) {
	if os.Getenv("AUXLY_TEST_LOCK_HELPER") != "" {
		t.Skip()
	}
	root := t.TempDir()
	logPath := filepath.Join(root, "order.log")

	unlock, err := LockVault(root)
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(os.Args[0], "-test.run", "TestLockHelperProcess", "-test.v")
	cmd.Env = append(os.Environ(), "AUXLY_TEST_LOCK_HELPER=1", "AUXLY_TEST_LOCK_ROOT="+root)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	// Give the child time to start contending, then record our marker and release.
	time.Sleep(500 * time.Millisecond)
	f, _ := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	fmt.Fprintln(f, "parent-release")
	f.Close()
	unlock()

	if err := cmd.Wait(); err != nil {
		t.Fatalf("helper process failed: %v", err)
	}

	data, _ := os.ReadFile(logPath)
	want := "parent-release\nchild-acquired\n"
	if string(data) != want {
		t.Fatalf("lock did not serialize processes; log:\n%s", data)
	}
}

// TestLockHelperProcess is the re-exec target for the cross-process test — not a
// real test when run in the normal suite.
func TestLockHelperProcess(t *testing.T) {
	if os.Getenv("AUXLY_TEST_LOCK_HELPER") == "" {
		t.Skip("helper: only runs re-exec'd from TestLockVaultBlocksOtherProcess")
	}
	root := os.Getenv("AUXLY_TEST_LOCK_ROOT")
	unlock, err := LockVault(root)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	f, _ := os.OpenFile(filepath.Join(root, "order.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	fmt.Fprintln(f, "child-acquired")
	f.Close()
}
