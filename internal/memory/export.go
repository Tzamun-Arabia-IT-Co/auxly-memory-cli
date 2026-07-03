package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ExportResult summarises a completed memory export.
type ExportResult struct {
	Dir   string   // the timestamped export folder created
	Files []string // the tagged file names written into it
}

// exportStamp is the compact timestamp used in the export folder + file names.
const exportStamp = "2006-01-02_150405"

// Export copies every memory .md file into a fresh timestamped folder under destBase
// (typically ~/Downloads) so the user can keep or share a snapshot. Each file is TAGGED
// with its original name and the export time in three ways: the folder name, the file
// name (`<name>__<stamp>.md`), and a header comment inside the file. A MANIFEST.txt
// records the set. `now` is injected so the timestamp is deterministic for tests.
//
// Unreadable files are skipped rather than aborting the whole export; a hard failure
// (cannot list, cannot create the folder, cannot write) returns an error.
func (s *Store) Export(destBase string, now time.Time) (ExportResult, error) {
	files, err := s.List()
	if err != nil {
		return ExportResult{}, fmt.Errorf("listing memory files: %w", err)
	}
	if len(files) == 0 {
		return ExportResult{}, fmt.Errorf("no memory files to export")
	}

	stamp := now.Format(exportStamp)
	tagTime := now.Format(time.RFC3339)
	dir := filepath.Join(destBase, "auxly-memory-export-"+stamp)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ExportResult{}, fmt.Errorf("creating export folder: %w", err)
	}

	var manifest strings.Builder
	manifest.WriteString("Auxly memory export\n")
	manifest.WriteString("Exported: " + tagTime + "\n")
	manifest.WriteString("Source:   " + s.Root + "\n\n")

	var written []string
	for _, f := range files {
		if s.fileIsEncrypted(f.Name) {
			// Exporting would create a plaintext copy of an encrypted file —
			// skip it and say so, rather than silently defeating the point
			// of encrypting it.
			manifest.WriteString(fmt.Sprintf("  %-32s  SKIPPED — encrypted at rest\n", f.Name))
			continue
		}
		content, verr := s.View(f.Name)
		if verr != nil {
			continue // skip an unreadable file rather than failing the whole export
		}
		base := strings.TrimSuffix(f.Name, ".md")
		outName := fmt.Sprintf("%s__%s.md", base, stamp)
		header := fmt.Sprintf("<!-- Auxly memory export · file: %s · exported: %s -->\n\n", f.Name, tagTime)
		if werr := os.WriteFile(filepath.Join(dir, outName), []byte(header+content), 0o644); werr != nil {
			_ = os.RemoveAll(dir) // don't leave a half-written export behind
			return ExportResult{}, fmt.Errorf("writing %s: %w", outName, werr)
		}
		written = append(written, outName)
		manifest.WriteString(fmt.Sprintf("  %-32s  %6d bytes  ← %s\n", outName, len(content), f.Name))
	}
	if len(written) == 0 {
		return ExportResult{}, fmt.Errorf("no readable memory files to export")
	}
	if werr := os.WriteFile(filepath.Join(dir, "MANIFEST.txt"), []byte(manifest.String()), 0o644); werr != nil {
		_ = os.RemoveAll(dir) // don't leave a half-written export behind
		return ExportResult{}, fmt.Errorf("writing manifest: %w", werr)
	}
	return ExportResult{Dir: dir, Files: written}, nil
}
