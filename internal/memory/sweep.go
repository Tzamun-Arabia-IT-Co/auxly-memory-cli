package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/vaultcrypt"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/templates"
)

// Orphan sweep: vault roots accumulate files the taxonomy cannot govern —
// hand-dropped notes (infrastructure.md next to infra.md), stale exports,
// leftovers from older versions. IsOrganizableFile excludes them, so Consolidate
// can never see the mess and Split/Contradictions never touch it. The sweep is
// the cleanup path: detect those files, re-file their bullets into taxonomy
// targets through the SAME review-gated pending queue every other organize mode
// uses, and report (never destructively touch) anything non-memory.
//
// Safety shape mirrors split-projects (split_projects_run.go): two-phase, so
// rejecting an addition can never lose a fact —
//   - phase 1 queues ONLY +line additions into taxonomy targets;
//   - phase 2 (a later run) queues -line deletions from an orphan, and only
//     for bullets whose normalized form is PROVABLY present in a target read
//     at plan time.
// Known accepted residual (identical to split's MovedProjectBullets): an
// intervening Consolidate could dedupe a target copy between plan time and the
// orphan deletion's approval. In practice the orphan line was a duplicate
// anyway; nothing unique is lost.

// sweepHooks' Progress callbacks bracket the slow parts of PlanSweepRun.
type SweepHooks struct {
	BackedUp     func(path string)
	PlanningFile func(name string) // per-orphan, before its bullets join the model input
	Planning     func()            // once, before the model call
}

// SweepOpts selects the LLM transport for the re-file call: AgentPath != ""
// runs a CLI agent subprocess (same contract as OrganizeVaultWithAgentOpts),
// otherwise the Direct LLM endpoint from internal/llm.
type SweepOpts struct {
	AgentName string
	AgentPath string
	Model     string
}

// SweepResult is the computed outcome of one sweep run — everything the CLI
// and the TUI need to queue pendings, run the guarded empty-file removal, and
// report a summary. CleanupWrites (already-re-homed bullets) and Writes (this
// run's re-file proposals) are independent, like SplitProjectsResult's.
type SweepResult struct {
	CleanupWrites    []PendingWrite // deletions from orphans whose content is provably re-homed
	Writes           []PendingWrite // additions into taxonomy targets
	RemoveEmpty      []string       // md orphans empty on disk — caller removes via RemoveEmptyVaultFiles after the pending-target guard
	SkippedCount     int            // model bullets matching no original, or naming a non-allowlisted target
	NoBulletFiles    []string       // md orphans with prose but no bullets — reported, never auto-emptied
	EncryptedSkipped []string       // encrypted-at-rest orphans — left alone this run
	OtherOrphans     []string       // non-md root orphans (.bak, .txt, …) — report only, never touched
	NothingToSweep   bool           // no md orphans found (OtherOrphans may still be set)
}

// sweepWhitelist returns the root-entry names that are NOT orphans: everything
// the embedded templates seed (agent instruction files, the two yaml configs,
// the seeded memory files — derived at runtime so a new template in a release
// is automatically legitimate) plus the files other auxly commands own.
func sweepWhitelist() map[string]bool {
	allowed := map[string]bool{
		"AGENTS.md":       true, // setup/connect write it
		"providers.md":    true, // protocol doc
		unifiedMemoryFile: true, // generated aggregate (never a source of truth)
	}
	if entries, err := templates.FS.ReadDir("."); err == nil {
		for _, e := range entries {
			if !e.IsDir() && e.Name() != "embed.go" {
				allowed[e.Name()] = true
			}
		}
	}
	return allowed
}

// sweepOwnedPrefixes are root entries auxly itself owns regardless of suffix —
// the audit SQLite database and its -shm/-wal sidecars are auxly's own index,
// not junk to report.
var sweepOwnedPrefixes = []string{"audit.db"}

// OrphanRootFiles enumerates vault-root entries outside every governed
// surface: not taxonomy files (CategoryForFile), not projects/ (a dir), not
// dot-dirs/dotfiles (.pending, .backup, …), not whitelisted setup files. The
// rest split into md orphans (sweep can re-file them) and everything else
// (.bak, .txt, … — reported only). Store.List is NOT used: it hides non-.md
// entries, and the sweep must see .bak files to report them.
//
// Reads s.Root directly — the global root is where orphans live and where
// pending approvals land, so workspace-shadowed copies must never confuse it.
func (s *Store) OrphanRootFiles() (mdFiles, others []string, err error) {
	entries, err := os.ReadDir(s.Root)
	if err != nil {
		return nil, nil, err
	}
	allowed := sweepWhitelist()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") {
			continue // projects/, .pending/, .backup/, .index/, … and dotfiles
		}
		if allowed[name] {
			continue
		}
		if _, ok := CategoryForFile(name); ok {
			continue // exact-match, case-sensitive like every other taxonomy gate
		}
		owned := false
		for _, p := range sweepOwnedPrefixes {
			if strings.HasPrefix(name, p) {
				owned = true
				break
			}
		}
		if owned {
			continue
		}
		if strings.EqualFold(filepath.Ext(name), ".md") {
			mdFiles = append(mdFiles, name)
		} else {
			others = append(others, name)
		}
	}
	sort.Strings(mdFiles)
	sort.Strings(others)
	return mdFiles, others, nil
}

// sweepTargets is the mechanical allowlist of re-file destinations: the
// organizable taxonomy files minus inbox.md (a staging inbox, not a fact
// destination) plus EXISTING projects/<slug>.md sub-files. Enforced
// post-parse on everything the model returns — a model-invented name can
// never be queued, so the sweep cannot mint the next generation of orphans.
func (s *Store) sweepTargets() map[string]bool {
	targets := map[string]bool{}
	for _, f := range OrganizableFiles() {
		if f != "inbox.md" {
			targets[f] = true
		}
	}
	if files, err := s.List(); err == nil {
		for _, f := range files {
			if strings.HasPrefix(f.Name, "projects/") && strings.HasSuffix(f.Name, ".md") {
				targets[f.Name] = true
			}
		}
	}
	return targets
}

// sweepIsHusk reports whether raw carries no re-fileable content: every line
// is blank or a markdown heading. This is the removal predicate for orphan
// files — a file whose every fact has been approved into taxonomy targets
// keeps only its title, and a title of content that lives elsewhere is not a
// fact. A backup of the full original exists from the run that queued its
// deletions, so removal stays reversible.
func sweepIsHusk(raw []byte) bool {
	for _, l := range strings.Split(string(raw), "\n") {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		return false
	}
	return true
}

// writeRawBackup snapshots raw (possibly ciphertext) bytes next to the other
// recovery points under <memPath>/.backup/. Shared by BackupProjectsMonolith
// and the sweep. NOTE: seconds-resolution timestamps mean two backups of the
// same base name within one second overwrite — AtomicWriteFile makes that a
// clean last-wins, never a torn file.
func writeRawBackup(memPath, baseName string, raw []byte) (string, error) {
	backup := filepath.Join(memPath, ".backup", baseName+"-"+time.Now().Format("20060102-150405")+".md")
	if err := AtomicWriteFile(backup, raw, 0o644); err != nil {
		return "", err
	}
	return backup, nil
}

// BackupVaultFile snapshots any vault file's RAW on-disk bytes before sweep
// pendings are queued against it — ciphertext stays ciphertext, exactly like
// BackupProjectsMonolith's contract.
func (s *Store) BackupVaultFile(memPath, name string) (path string, encrypted bool, err error) {
	raw, enc, rerr := s.ReadRawVaultBytes(name)
	if rerr != nil {
		return "", false, fmt.Errorf("read %s: %w", name, rerr)
	}
	backup, werr := writeRawBackup(memPath, strings.TrimSuffix(name, ".md"), raw)
	if werr != nil {
		return "", false, fmt.Errorf("backup %s first: %w", name, werr)
	}
	return backup, enc, nil
}

// RemoveEmptyVaultFiles removes already-empty md orphans, re-verifying
// emptiness INSIDE the same LockVault critical section as the removal so a
// write landing between plan and now can never be destroyed. Only names this
// package itself enumerated as orphans may be passed in (single root-level
// path segments); anything else is refused. Returns the names actually
// removed. The caller is responsible for the pending-queue guard (entries
// still targeting a file must keep it — pending can only empty a file, never
// delete one, but approving an addition would silently resurrect it).
func (s *Store) RemoveEmptyVaultFiles(names []string) []string {
	var removed []string
	for _, name := range names {
		if name == "" || filepath.Base(name) != name || strings.HasPrefix(name, ".") {
			continue // defense in depth: root-level, non-hidden names only
		}
	}
	unlock, err := LockVault(s.Root)
	if err != nil {
		return nil
	}
	defer unlock()
	for _, name := range names {
		if name == "" || filepath.Base(name) != name || strings.HasPrefix(name, ".") {
			continue
		}
		path := filepath.Join(s.Root, name)
		data, rerr := os.ReadFile(path)
		if rerr != nil || !sweepIsHusk(data) {
			continue // gone, or no longer a husk — not ours to remove
		}
		if rmerr := os.Remove(path); rmerr != nil {
			continue
		}
		s.pruneIndexedFile(path)
		removed = append(removed, name)
	}
	return removed
}

// sweepSystemPrompt is the re-file contract. The prompt deliberately carries
// NO target file bodies — only the taxonomy guide and existing project file
// names — so a CLI-agent run's argv never includes decrypted taxonomy
// content (the encrypted-target refusal matrix Consolidate needs cannot
// arise here; encrypted ORPHANS are simply skipped before the call).
func sweepSystemPrompt(extraProjectTargets []string) string {
	var b strings.Builder
	b.WriteString(`You are sweeping ORPHAN memory files — loose notes that ended up outside the vault's structure — back into the established taxonomy.

RESPONSE CONTRACT — reply with EXACTLY ONE JSON object, nothing else:
{"moves": [{"target": "<file>", "bullets": ["<bullet>", ...]}, ...]}

RULES:
- Pick the single best file for each bullet from this guide:
`)
	b.WriteString(RenderForPrompt())
	if len(extraProjectTargets) > 0 {
		b.WriteString("- These existing per-project files are also valid targets: " + strings.Join(extraProjectTargets, ", ") + "\n")
	}
	b.WriteString(`- OMIT any bullet no file genuinely fits — it stays where it is; never force a bad match.
- COPY EVERY BULLET VERBATIM — never reword, merge, split, annotate, or drop.`)
	return b.String()
}

// PlanSweepRun computes what one sweep run should queue and clean — the ONE
// shared implementation behind `auxly organize --sweep` and the TUI's Sweep
// orphans mode. Side effects (backups) happen here; queuing pendings is the
// caller's (see PendingWrite doc). On a planning failure the cleanup writes
// computed so far are still returned — the caller queues them regardless,
// exactly like runSplitProjects.
func (s *Store) PlanSweepRun(ctx context.Context, memPath string, opts SweepOpts, hooks *SweepHooks) (SweepResult, error) {
	return s.planSweep(ctx, memPath, hooks, func(c context.Context, sys, user string) (organizeRun, OrganizeResult, bool) {
		return s.runOrganizeModel(c, opts.AgentName, opts.AgentPath, opts.Model, sys, user)
	})
}

// planSweep is PlanSweepRun with the model call injected as an executor —
// the same testability seam PlanProjectsSplit has (see splitExec in
// split_projects_test.go).
func (s *Store) planSweep(ctx context.Context, memPath string, hooks *SweepHooks, exec organizeExecutor) (SweepResult, error) {
	var result SweepResult

	mdFiles, others, err := s.OrphanRootFiles()
	if err != nil {
		return result, fmt.Errorf("scan vault root: %w", err)
	}
	result.OtherOrphans = others
	if len(mdFiles) == 0 {
		result.NothingToSweep = true
		return result, nil
	}

	// Presence set: normalized bullets already readable in some allowlisted
	// target, read from the GLOBAL root (a workspace shadow must not falsify
	// the "provably re-homed" proof — deletions land on the global copy).
	// Encrypted targets are skipped: without decrypting we cannot prove
	// presence, and unproven means un-deleted (conservative by design).
	targets := s.sweepTargets()
	present := map[string]bool{}
	var projectTargets []string
	for t := range targets {
		if strings.HasPrefix(t, "projects/") {
			projectTargets = append(projectTargets, t)
		}
		raw, terr := os.ReadFile(filepath.Join(s.Root, filepath.FromSlash(t)))
		if terr != nil || vaultcrypt.IsEncrypted(raw) {
			continue
		}
		for _, b := range bulletLines(string(raw)) {
			present[normalizeBullet(b)] = true
		}
	}
	sort.Strings(projectTargets)

	// Snapshot every md orphan from the global root: raw bytes for the
	// backup, verbatim bullets for matching. Encrypted orphans are recorded
	// and skipped — the sweep never decrypts anything.
	type orphanSnap struct {
		name    string
		bullets []string // verbatim trimmed bullet lines
	}
	var snaps []orphanSnap
	seenNorm := map[string]bool{} // cross-orphan dedup: ApplyDiff's fuzzy dedup is per-target only
	for _, name := range mdFiles {
		raw, rerr := os.ReadFile(filepath.Join(s.Root, name))
		if rerr != nil {
			continue // raced away between scan and read — next run gets it
		}
		if sweepIsHusk(raw) {
			result.RemoveEmpty = append(result.RemoveEmpty, name)
			continue
		}
		if vaultcrypt.IsEncrypted(raw) {
			result.EncryptedSkipped = append(result.EncryptedSkipped, name)
			continue
		}
		bullets := bulletLines(string(raw))
		if len(bullets) == 0 {
			result.NoBulletFiles = append(result.NoBulletFiles, name)
			continue
		}
		if hooks != nil && hooks.PlanningFile != nil {
			hooks.PlanningFile(name)
		}
		backup, _, berr := s.BackupVaultFile(memPath, name)
		if berr != nil {
			return result, berr
		}
		if hooks != nil && hooks.BackedUp != nil {
			hooks.BackedUp(backup)
		}
		var kept []string
		for _, b := range bullets {
			n := normalizeBullet(b)
			if seenNorm[n] {
				continue // duplicate across orphans — first copy is enough
			}
			seenNorm[n] = true
			kept = append(kept, b)
		}
		snaps = append(snaps, orphanSnap{name: name, bullets: kept})
	}

	// Phase 2 first: queue deletions for bullets already provably re-homed,
	// and keep them OUT of the model input (a bullet both deleted and
	// re-added elsewhere would mint a cross-file duplicate).
	var input []string
	var movedByOrphan []PendingWrite
	for _, snap := range snaps {
		var delDiff strings.Builder
		var stay []string
		for _, b := range snap.bullets {
			if present[normalizeBullet(b)] {
				delDiff.WriteString("-" + b + "\n") // verbatim: ApplyDiff matches deletions by exact trim
			} else {
				stay = append(stay, b)
			}
		}
		if delDiff.Len() > 0 {
			movedByOrphan = append(movedByOrphan, PendingWrite{TargetFile: snap.name, Diff: delDiff.String(), Count: strings.Count(delDiff.String(), "\n")})
		}
		input = append(input, stay...)
	}
	result.CleanupWrites = movedByOrphan

	if len(input) == 0 {
		// Everything readable was either already re-homed (cleanup above) or
		// not re-fileable (empty/no-bullet/encrypted) — no model call needed.
		return result, nil
	}

	if hooks != nil && hooks.Planning != nil {
		hooks.Planning()
	}
	user := "Here are the orphan bullets to re-file:\n\n" + strings.Join(input, "\n")
	run, res, proceed := exec(ctx, sweepSystemPrompt(projectTargets), user)
	if !proceed {
		return result, fmt.Errorf("sweep model call failed: %s", res.Message)
	}

	raw := strings.TrimSpace(run.jsonContent)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	var out struct {
		Moves []struct {
			Target  string   `json:"target"`
			Bullets []string `json:"bullets"`
		} `json:"moves"`
	}
	if jerr := json.Unmarshal([]byte(repairAgentJSON(raw)), &out); jerr != nil {
		return result, fmt.Errorf("sweep response is not the contracted JSON: %w", jerr)
	}

	orig := map[string]string{} // normalized → verbatim original bullet
	for _, b := range input {
		orig[normalizeBullet(b)] = b
	}
	moves := map[string][]string{} // target → verbatim bullets
	skipped := 0
	matchedAny := false
	seenMove := map[string]bool{}
	for _, mv := range out.Moves {
		target := strings.TrimSpace(mv.Target)
		for _, mb := range mv.Bullets {
			mb = strings.TrimSpace(mb)
			n := normalizeBullet(mb)
			verbatim, ok := orig[n]
			if !ok || seenMove[n] {
				skipped++ // model rewording, duplicate, or unknown bullet
				continue
			}
			// The model echoed a real input bullet — the response is not
			// garbage, whether or not its destination is allowlisted.
			matchedAny = true
			seenMove[n] = true
			if !targets[target] {
				skipped++ // model-invented destination — never queueable
				continue
			}
			moves[target] = append(moves[target], verbatim)
		}
	}
	if !matchedAny {
		return result, fmt.Errorf("sweep REJECTED: model output matched none of the %d input bullet(s) — response looks like garbage", len(input))
	}
	result.SkippedCount = skipped

	var targetNames []string
	for t := range moves {
		targetNames = append(targetNames, t)
	}
	sort.Strings(targetNames)
	for _, t := range targetNames {
		var addDiff strings.Builder
		for _, b := range moves[t] {
			addDiff.WriteString("+" + b + "\n")
		}
		result.Writes = append(result.Writes, PendingWrite{TargetFile: t, Diff: addDiff.String(), Count: len(moves[t])})
	}
	return result, nil
}
