package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ProjectsSplitPlan is a validated proposal to split the projects.md monolith
// into per-project sub-files. It is a PLAN only — the caller queues it as
// pending changes for human review; nothing here writes the vault.
type ProjectsSplitPlan struct {
	Groups  map[string][]string // sanitized slug → verbatim bullets moving to projects/<slug>.md
	General []string            // bullets staying in projects.md (cross-project / unattributable)
	Skipped []string            // model bullets that matched no original — dropped, fact stays in projects.md
	Source  string              // projects.md content the plan was computed from
}

const splitSystemPrompt = `You are reorganizing ONE file: projects.md — a mixed list of project facts, one bullet per fact.

RESPONSE CONTRACT — reply with EXACTLY ONE JSON object, nothing else:
{"projects": {"<slug>": ["<bullet>", ...]}, "general": ["<bullet>", ...]}

RULES:
- Group each bullet under the single project it belongs to. A slug is lowercase letters/digits/dashes naming that repo or product (e.g. "auxly-memory").
- A bullet that spans several projects, or names no identifiable project, goes to "general".
- COPY EVERY BULLET VERBATIM — never reword, merge, split, annotate, or drop. Every input bullet appears EXACTLY once somewhere in the output.`

// normalizeBullet is normalizeFact (organize.go) plus tolerance for the
// formatting loss models routinely introduce when copying a bullet back:
// bold/underline emphasis markers stripped. Every bullet comparison in this
// file — the split's own matching AND MovedProjectBullets' cleanup match —
// goes through this one function so the two phases agree on what "the same
// bullet" means.
func normalizeBullet(b string) string {
	b = strings.ReplaceAll(b, "**", "")
	b = strings.ReplaceAll(b, "__", "")
	return normalizeFact(b)
}

// bulletUnit is one projects.md bullet line together with the indentation of
// the ORIGINAL line. bulletLines (organize.go) intentionally flattens that
// indentation for the general dedup path; the split needs it preserved so a
// nested sub-bullet can be traced back to its parent.
type bulletUnit struct {
	text   string // verbatim trimmed bullet line, "- ..." or "* ..."
	indent int
}

// bulletUnits walks projects.md content the same way bulletLines does, but
// keeps each line's leading-whitespace depth instead of discarding it.
func bulletUnits(content string) []bulletUnit {
	var out []bulletUnit
	for _, l := range strings.Split(content, "\n") {
		t := strings.TrimSpace(l)
		if !strings.HasPrefix(t, "- ") && !strings.HasPrefix(t, "* ") {
			continue
		}
		out = append(out, bulletUnit{text: t, indent: len(l) - len(strings.TrimLeft(l, " \t"))})
	}
	return out
}

// bulletParents maps a nested bullet's normalized form to the normalized form
// of the nearest shallower bullet above it (its parent). A top-level bullet,
// or one with no shallower bullet above it, has no entry.
func bulletParents(units []bulletUnit) map[string]string {
	parents := map[string]string{}
	var stack []bulletUnit
	for _, u := range units {
		for len(stack) > 0 && stack[len(stack)-1].indent >= u.indent {
			stack = stack[:len(stack)-1]
		}
		if len(stack) > 0 {
			parents[normalizeBullet(u.text)] = normalizeBullet(stack[len(stack)-1].text)
		}
		stack = append(stack, u)
	}
	return parents
}

// MovedProjectBullets returns the projects.md bullets whose normalized form
// already exists in some projects/ sub-file — i.e. bullets whose split
// addition was ALREADY approved. Only these are ever safe to delete from the
// monolith: presence in a sub-file is the mechanical proof no fact is lost.
func (s *Store) MovedProjectBullets() ([]string, error) {
	content, err := s.View("projects.md")
	if err != nil {
		return nil, err
	}
	files, err := s.List()
	if err != nil {
		return nil, err
	}
	inSub := map[string]bool{}
	for _, f := range files {
		if !strings.HasPrefix(f.Name, "projects/") {
			continue
		}
		sub, verr := s.View(f.Name)
		if verr != nil {
			continue
		}
		for _, b := range bulletLines(sub) {
			inSub[normalizeBullet(b)] = true
		}
	}
	var moved []string
	for _, b := range bulletLines(content) {
		if inSub[normalizeBullet(b)] {
			moved = append(moved, b)
		}
	}
	return moved, nil
}

// PlanProjectsSplit asks the model to group projects.md bullets by project,
// then matches every model-returned bullet back to an original by NORMALIZED
// form — tolerant of the bold/underline emphasis markers models routinely
// drop (and any other cosmetic reformatting normalizeBullet absorbs). The
// bullet actually queued for the sub-file is always the ORIGINAL verbatim
// text, never the model's copy.
//
// A model bullet matching no original is dropped, not fatal: the two-phase
// design already guarantees an un-moved bullet is never deleted, so it simply
// stays in projects.md. These are collected in ProjectsSplitPlan.Skipped.
// Only a response that matches NONE of the input bullets is a hard failure
// (the model returned garbage). A nested bullet always shares its parent's
// fate — moved together, or left together.
//
// Bullets already present in a sub-file (an earlier approved split) and
// duplicate bullets are excluded from the model input: the first are handled
// by the cleanup phase, and duplicates would otherwise be merged by any
// reasonable model.
func (s *Store) PlanProjectsSplit(ctx context.Context, exec organizeExecutor) (ProjectsSplitPlan, error) {
	content, err := s.View("projects.md")
	if err != nil {
		return ProjectsSplitPlan{}, fmt.Errorf("read projects.md: %w", err)
	}
	moved, err := s.MovedProjectBullets()
	if err != nil {
		return ProjectsSplitPlan{}, err
	}
	movedSet := map[string]bool{}
	for _, b := range moved {
		movedSet[normalizeBullet(b)] = true
	}

	units := bulletUnits(content)
	parentOf := bulletParents(units)

	seen := map[string]bool{}
	orig := map[string]string{} // normalized -> original verbatim bullet text
	var bullets []string
	for _, u := range units {
		n := normalizeBullet(u.text)
		if movedSet[n] || seen[n] {
			continue
		}
		seen[n] = true
		orig[n] = u.text
		bullets = append(bullets, u.text)
	}
	if len(bullets) == 0 {
		return ProjectsSplitPlan{}, fmt.Errorf("projects.md has no bullets to split")
	}

	user := "Here is projects.md to split:\n\n" + strings.Join(bullets, "\n")
	run, res, proceed := exec(ctx, splitSystemPrompt, user)
	if !proceed {
		return ProjectsSplitPlan{}, fmt.Errorf("split model call failed: %s", res.Message)
	}

	raw := strings.TrimSpace(run.jsonContent)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	var out struct {
		Projects map[string][]string `json:"projects"`
		General  []string            `json:"general"`
	}
	if err := json.Unmarshal([]byte(repairAgentJSON(raw)), &out); err != nil {
		return ProjectsSplitPlan{}, fmt.Errorf("split response is not the contracted JSON: %w", err)
	}

	dest := map[string]string{} // normalized original -> destination slug ("" = general)
	var skipped []string
	matchedAny := false
	assign := func(modelBullets []string, slug string) {
		for _, mb := range modelBullets {
			mb = strings.TrimSpace(mb)
			n := normalizeBullet(mb)
			if _, ok := orig[n]; !ok {
				skipped = append(skipped, mb)
				continue
			}
			if _, already := dest[n]; already {
				continue // duplicate model bullet — keep first slug
			}
			dest[n] = slug
			matchedAny = true
		}
	}
	var keys []string
	for key := range out.Projects {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		// sanitizeSlug("") folds an unusable slug's bullets to general rather
		// than a junk-named file.
		assign(out.Projects[key], sanitizeSlug(key))
	}
	assign(out.General, "")
	if !matchedAny {
		return ProjectsSplitPlan{}, fmt.Errorf("split REJECTED: model output matched none of the %d input bullet(s) — response looks like garbage", len(bullets))
	}

	// A nested bullet always follows its parent's fate. This walks in source
	// order so a grandchild picks up its parent's already-resolved
	// destination rather than a stale one.
	for _, u := range units {
		n := normalizeBullet(u.text)
		parent, hasParent := parentOf[n]
		if !hasParent {
			continue
		}
		if _, ok := orig[n]; !ok {
			continue // excluded earlier (already moved, or a duplicate original)
		}
		if pd, ok := dest[parent]; ok {
			dest[n] = pd
		} else {
			delete(dest, n) // parent stayed — leave the child too
		}
	}

	plan := ProjectsSplitPlan{Groups: map[string][]string{}, Source: content, Skipped: skipped}
	for _, b := range bullets {
		slug, ok := dest[normalizeBullet(b)]
		if !ok {
			continue // unmatched, or a child left behind with an unmoved parent
		}
		if slug == "" {
			plan.General = append(plan.General, b)
		} else {
			plan.Groups[slug] = append(plan.Groups[slug], b)
		}
	}
	return plan, nil
}

// sanitizeSlug normalizes a model-proposed project key to the slug charset
// (same rules as ProjectSlug). "" when nothing survives.
func sanitizeSlug(key string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(key)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteRune('-')
				lastDash = true
			}
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// PlanProjectsSplitWithAgent mirrors PlanOrganizeWithAgent: same model
// resolution (CLI agent when agentPath != "", else the direct LLM endpoint).
func (s *Store) PlanProjectsSplitWithAgent(ctx context.Context, agentName, agentPath, model string) (ProjectsSplitPlan, error) {
	return s.PlanProjectsSplit(ctx, func(c context.Context, sys, user string) (organizeRun, OrganizeResult, bool) {
		return s.runOrganizeModel(c, agentName, agentPath, model, sys, user)
	})
}
