package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/safepath"
)

// PendingWrite is one target-file diff a split/contradiction run wants
// queued as a pending change. Actually queuing it (pending.Manager.WriteFrom)
// is left to the caller: internal/pending already imports internal/memory,
// so queuing pendings from inside this package would be an import cycle.
// Both cmd/organize.go and tui/organize.go loop over these and call
// WriteFrom themselves.
type PendingWrite struct {
	TargetFile string
	Diff       string
	Count      int // bullets/findings this diff represents, for caller messages
}

// SplitProjectsHooks lets a caller surface progress AROUND the slow LLM
// planning call inside PlanSplitProjectsRun — e.g. the CLI prints "backed
// up" / "planning..." before/after the parts that actually take time, the
// same live feedback runSplitProjects always gave. Every field is optional
// (nil is a no-op); the TUI shows its own spinner instead and passes nil.
type SplitProjectsHooks struct {
	BackedUp func(path string, encrypted bool)
	Planning func()
	Seeded   func(subFile string)
}

// SplitProjectsResult is the full computed outcome of one projects.md split
// run — everything both `auxly organize --split-projects` and the TUI's
// Split projects mode need to queue pendings and report a summary.
// Two-phase, matching PlanProjectsSplitWithAgent's doc: CleanupWrite
// (bullets an earlier approved split already moved) and Writes (this run's
// new groupings) are independent — either can be nil/empty.
type SplitProjectsResult struct {
	CleanupWrite   *PendingWrite  // non-nil: queue this to remove already-moved bullets/sections from projects.md
	Writes         []PendingWrite // one per new projects/<slug>.md this run groups (one per section in header mode)
	SeededFiles    []string       // sub-files created encrypted-at-rest this run
	GeneralCount   int            // bullets staying in projects.md (cross-project/unattributable) — LLM path only
	SkippedCount   int            // model bullets matching no original bullet (stay in projects.md) — LLM path only
	CleanupOnly    bool           // no new grouping this run; CleanupWrite (if any) is the whole job
	NothingToSplit bool           // no bullets anywhere could be attributed to a project — LLM path only; header mode never sets this (a `## ` section always has somewhere to go)
	HeaderMode     bool           // this run used the deterministic header-section split, not the LLM bullet path
}

// PlanSplitProjectsRun computes what one projects.md split run should queue
// — the ONE shared implementation behind both `auxly organize
// --split-projects` (cmd/organize.go's runSplitProjects) and the TUI's Split
// projects mode, so the matching/backup/seeding logic exists exactly once.
// It performs every side effect except queuing pendings (see PendingWrite
// doc) — backing up projects.md and seeding encrypted sub-files at the same
// points the original single-caller version always did.
func (s *Store) PlanSplitProjectsRun(ctx context.Context, memPath string, hooks *SplitProjectsHooks) (SplitProjectsResult, error) {
	var result SplitProjectsResult

	// Header-structured projects.md (## sections) splits deterministically —
	// see planSplitProjectsRunHeaderMode. A flat file (no `## ` header at
	// all) falls through to the LLM bullet-attribution path below, unchanged.
	content, err := s.View("projects.md")
	if err != nil {
		return result, fmt.Errorf("read projects.md: %w", err)
	}
	if sections := splitProjectsByHeaders(content); len(sections) > 0 {
		return s.planSplitProjectsRunHeaderMode(memPath, hooks, sections)
	}

	// Phase 2 first: bullets an earlier approved split already moved.
	moved, merr := s.MovedProjectBullets()
	if merr == nil && len(moved) > 0 {
		path, encrypted, err := s.BackupProjectsMonolith(memPath)
		if err != nil {
			return result, err
		}
		if hooks != nil && hooks.BackedUp != nil {
			hooks.BackedUp(path, encrypted)
		}
		var delDiff strings.Builder
		for _, b := range moved {
			delDiff.WriteString("-" + b + "\n")
		}
		result.CleanupWrite = &PendingWrite{TargetFile: "projects.md", Diff: delDiff.String(), Count: len(moved)}
	}

	if hooks != nil && hooks.Planning != nil {
		hooks.Planning()
	}
	plan, err := s.PlanProjectsSplitWithAgent(ctx, "Direct LLM", "", "")
	if err != nil {
		if len(moved) > 0 && strings.Contains(err.Error(), "no bullets to split") {
			// Everything remaining was already moved — cleanup above (if any)
			// is the whole job this run.
			result.CleanupOnly = true
			return result, nil
		}
		return result, err
	}
	if len(plan.Groups) == 0 {
		result.NothingToSplit = true
		return result, nil
	}
	if len(moved) == 0 {
		path, encrypted, berr := s.BackupProjectsMonolith(memPath)
		if berr != nil {
			return result, berr
		}
		if hooks != nil && hooks.BackedUp != nil {
			hooks.BackedUp(path, encrypted)
		}
	}

	// MAJOR 9: if projects.md is encrypted at rest, each NEW sub-file must be
	// seeded as an empty ENCRYPTED file before its first pending addition is
	// queued — encryption state lives in the file, so a sub-file created only
	// when its first pending gets approved would default to plaintext and
	// stay that way forever.
	_, projectsEncrypted, encErr := s.ReadRawVaultBytes("projects.md")
	if encErr != nil {
		return result, fmt.Errorf("check projects.md encryption: %w", encErr)
	}

	var slugs []string
	for slug := range plan.Groups {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)
	for _, slug := range slugs {
		subFile := "projects/" + slug + ".md"
		created, serr := s.SeedEncryptedProjectSubFile(memPath, subFile, projectsEncrypted)
		if serr != nil {
			return result, serr
		}
		if created {
			result.SeededFiles = append(result.SeededFiles, subFile)
			if hooks != nil && hooks.Seeded != nil {
				hooks.Seeded(subFile)
			}
		}
		bullets := plan.Groups[slug]
		var addDiff strings.Builder
		for _, b := range bullets {
			addDiff.WriteString("+" + b + "\n")
		}
		result.Writes = append(result.Writes, PendingWrite{TargetFile: subFile, Diff: addDiff.String(), Count: len(bullets)})
	}
	result.GeneralCount = len(plan.General)
	result.SkippedCount = len(plan.Skipped)
	return result, nil
}

// planSplitProjectsRunHeaderMode is PlanSplitProjectsRun's deterministic path
// for a projects.md organized with `## ` headers (see splitProjectsByHeaders
// in split_projects.go): no model call, so it moves each section
// byte-for-byte and needs none of the LLM path's bold-stripping tolerance.
// Same two-phase shape as the LLM path — phase 2 (sections an earlier run
// already got approved) is computed and queued first, phase 1 (this run's
// new section additions) second — just built from
// MovedProjectSections/projectSection instead of MovedProjectBullets/bullets.
func (s *Store) planSplitProjectsRunHeaderMode(memPath string, hooks *SplitProjectsHooks, sections []projectSection) (SplitProjectsResult, error) {
	result := SplitProjectsResult{HeaderMode: true}

	// Phase 2 first: sections an earlier approved split already moved.
	moved, merr := s.MovedProjectSections()
	if merr == nil && len(moved) > 0 {
		path, encrypted, err := s.BackupProjectsMonolith(memPath)
		if err != nil {
			return result, err
		}
		if hooks != nil && hooks.BackedUp != nil {
			hooks.BackedUp(path, encrypted)
		}
		var delDiff strings.Builder
		for _, sec := range moved {
			for _, l := range sectionLines(sec.body) {
				delDiff.WriteString("-" + l + "\n")
			}
		}
		result.CleanupWrite = &PendingWrite{TargetFile: "projects.md", Diff: delDiff.String(), Count: len(moved)}
	}

	movedSlugs := map[string]bool{}
	for _, sec := range moved {
		movedSlugs[sec.slug] = true
	}
	var remaining []projectSection
	for _, sec := range sections {
		if !movedSlugs[sec.slug] {
			remaining = append(remaining, sec)
		}
	}
	if len(remaining) == 0 {
		// Everything remaining was already moved — cleanup above (if any) is
		// the whole job this run.
		result.CleanupOnly = true
		return result, nil
	}

	if len(moved) == 0 {
		path, encrypted, berr := s.BackupProjectsMonolith(memPath)
		if berr != nil {
			return result, berr
		}
		if hooks != nil && hooks.BackedUp != nil {
			hooks.BackedUp(path, encrypted)
		}
	}

	// Same MAJOR 9 seeding rule as the LLM path: an encrypted projects.md
	// means every NEW sub-file must be seeded empty-and-encrypted before its
	// first pending addition is queued.
	_, projectsEncrypted, encErr := s.ReadRawVaultBytes("projects.md")
	if encErr != nil {
		return result, fmt.Errorf("check projects.md encryption: %w", encErr)
	}

	for _, sec := range remaining {
		subFile := "projects/" + sec.slug + ".md"
		created, serr := s.SeedEncryptedProjectSubFile(memPath, subFile, projectsEncrypted)
		if serr != nil {
			return result, serr
		}
		if created {
			result.SeededFiles = append(result.SeededFiles, subFile)
			if hooks != nil && hooks.Seeded != nil {
				hooks.Seeded(subFile)
			}
		}
		lines := strings.Split(sec.body, "\n")
		var addDiff strings.Builder
		for _, l := range lines {
			addDiff.WriteString("+" + l + "\n")
		}
		result.Writes = append(result.Writes, PendingWrite{TargetFile: subFile, Diff: addDiff.String(), Count: len(lines)})
	}
	return result, nil
}

// BackupProjectsMonolith snapshots projects.md before any split pendings are
// queued — a migration deserves a recovery point. Reads the RAW on-disk
// bytes (not View, which decrypts): if projects.md is encrypted at rest, the
// backup must stay ciphertext too, never a plaintext shadow copy.
func (s *Store) BackupProjectsMonolith(memPath string) (path string, encrypted bool, err error) {
	raw, enc, rerr := s.ReadRawVaultBytes("projects.md")
	if rerr != nil {
		return "", false, fmt.Errorf("read projects.md: %w", rerr)
	}
	backup := filepath.Join(memPath, ".backup", "projects-"+time.Now().Format("20060102-150405")+".md")
	if werr := AtomicWriteFile(backup, raw, 0o644); werr != nil {
		return "", false, fmt.Errorf("backup projects.md first: %w", werr)
	}
	return backup, enc, nil
}

// SeedEncryptedProjectSubFile pre-creates subFile as an empty ENCRYPTED file
// (state-lives-in-file trick, matching seedEncryptedPersonalMD) when
// projectsEncrypted is true and the sub-file doesn't exist yet on disk —
// otherwise approving its first pending addition would create it as
// plaintext and it would stay that way forever. No-op (created=false) when
// projects.md isn't encrypted or the sub-file already exists.
func (s *Store) SeedEncryptedProjectSubFile(memPath, subFile string, projectsEncrypted bool) (created bool, err error) {
	if !projectsEncrypted || s.Exists(subFile) {
		return false, nil
	}
	subPath, perr := safepath.ResolveSafe(memPath, subFile)
	if perr != nil {
		return false, fmt.Errorf("resolve %s: %w", subFile, perr)
	}
	// The empty seed replaces whatever is at subPath — serialize with every
	// other vault writer and re-check existence INSIDE the lock, or a write
	// landing between the check above and this one gets clobbered.
	unlock, lerr := LockVault(memPath)
	if lerr != nil {
		return false, lerr
	}
	defer unlock()
	if s.Exists(subFile) {
		return false, nil
	}
	if merr := os.MkdirAll(filepath.Dir(subPath), 0755); merr != nil {
		return false, fmt.Errorf("create projects dir: %w", merr)
	}
	if werr := s.WriteVaultFile(subPath, []byte{}, 0o644, true); werr != nil {
		return false, fmt.Errorf("seed encrypted %s: %w", subFile, werr)
	}
	return true, nil
}
