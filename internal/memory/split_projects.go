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
	Source  string              // projects.md content the plan was computed from
}

const splitSystemPrompt = `You are reorganizing ONE file: projects.md — a mixed list of project facts, one bullet per fact.

RESPONSE CONTRACT — reply with EXACTLY ONE JSON object, nothing else:
{"projects": {"<slug>": ["<bullet>", ...]}, "general": ["<bullet>", ...]}

RULES:
- Group each bullet under the single project it belongs to. A slug is lowercase letters/digits/dashes naming that repo or product (e.g. "auxly-memory").
- A bullet that spans several projects, or names no identifiable project, goes to "general".
- COPY EVERY BULLET VERBATIM — never reword, merge, split, annotate, or drop. Every input bullet appears EXACTLY once somewhere in the output.`

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
			inSub[normalizeFact(b)] = true
		}
	}
	var moved []string
	for _, b := range bulletLines(content) {
		if inSub[normalizeFact(b)] {
			moved = append(moved, b)
		}
	}
	return moved, nil
}

// PlanProjectsSplit asks the model to group projects.md bullets by project,
// then MECHANICALLY verifies the grouping is a perfect permutation of the
// input (RULE 0): any dropped, invented, reworded, or duplicated bullet
// rejects the whole plan — there is no force override for a migration.
// Bullets already present in a sub-file (an earlier approved split) and
// duplicate bullets are excluded from the model input: the first are handled
// by the cleanup phase, and duplicates would otherwise be merged by any
// reasonable model and fail the permutation gate forever.
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
		movedSet[normalizeFact(b)] = true
	}
	seen := map[string]bool{}
	var bullets []string
	for _, b := range bulletLines(content) {
		n := normalizeFact(b)
		if movedSet[n] || seen[n] {
			continue
		}
		seen[n] = true
		bullets = append(bullets, b)
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

	plan := ProjectsSplitPlan{Groups: map[string][]string{}, General: out.General, Source: content}
	for key, group := range out.Projects {
		slug := sanitizeSlug(key)
		if slug == "" {
			// Unusable slug — those bullets stay in the monolith rather than
			// landing in a junk-named file.
			plan.General = append(plan.General, group...)
			continue
		}
		plan.Groups[slug] = append(plan.Groups[slug], group...)
	}

	if err := validateSplitPermutation(bullets, plan); err != nil {
		return ProjectsSplitPlan{}, err
	}
	return plan, nil
}

// validateSplitPermutation is the mechanical RULE-0 gate: the output must be a
// perfect permutation of the input bullets (normalized like the dedup layer,
// so cosmetic whitespace can't fail an honest grouping).
func validateSplitPermutation(input []string, plan ProjectsSplitPlan) error {
	counts := map[string]int{}
	for _, b := range input {
		counts[normalizeFact(b)]++
	}
	consume := func(bs []string) error {
		for _, b := range bs {
			n := normalizeFact(b)
			if counts[n] == 0 {
				return fmt.Errorf("split REJECTED: model invented or reworded a bullet: %q", b)
			}
			counts[n]--
		}
		return nil
	}
	var slugs []string
	for slug := range plan.Groups {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)
	for _, slug := range slugs {
		if err := consume(plan.Groups[slug]); err != nil {
			return err
		}
	}
	if err := consume(plan.General); err != nil {
		return err
	}
	for n, c := range counts {
		if c > 0 {
			return fmt.Errorf("split REJECTED: model dropped %d bullet(s), e.g. %q — no fact may be lost in a migration", c, n)
		}
	}
	return nil
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
