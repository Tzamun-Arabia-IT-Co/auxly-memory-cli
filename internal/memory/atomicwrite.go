package memory

import (
	"os"
	"path/filepath"
)

// AtomicWriteFile writes data to path atomically: write to a temp file in the
// SAME directory, fsync, then rename over the target. A crash or kill mid-write
// can never leave a truncated or interleaved file — readers see either the old
// content or the new, nothing in between. Same-directory temp keeps the rename
// on one filesystem (atomic on POSIX; os.Rename on Windows uses
// MoveFileEx(MOVEFILE_REPLACE_EXISTING)).
//
// Memory files are the user's data — every vault/pending write must go through
// this instead of os.WriteFile.
func AtomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".auxly-tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// Any failure below must not leave the temp file behind.
	fail := func(e error) error {
		tmp.Close()
		os.Remove(tmpName)
		return e
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Chmod(perm); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}
