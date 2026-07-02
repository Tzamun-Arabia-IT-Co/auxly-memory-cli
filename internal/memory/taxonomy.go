package memory

import (
	"strings"
	"unicode"
)

// Tier is the ownership/privacy class of a memory category.
//
// Personal categories are private to the user and gated per-remote (see the
// per-remote file-sharing selection); shared categories are exposable to remotes
// by default. This is the second axis, orthogonal to the category itself.
type Tier string

const (
	TierShared   Tier = "shared"
	TierPersonal Tier = "personal"
)

// Category is one memory bucket: a markdown file with a fixed purpose, a tier,
// and the keyword set used by the write-time auto-router fallback.
type Category struct {
	Slug        string   // stable id, e.g. "infra"
	File        string   // backing file, e.g. "infra.md"
	Tier        Tier     // shared | personal
	Description string   // one-line purpose, injected into prompts
	Keywords    []string // substrings that route a fact here (fallback router)
}

// Taxonomy is the CANONICAL, ordered category list — the single source of truth.
//
// Every consumer (the sync auto-router, the /auxly-max slice harvest, the
// /auxly-learn folder validation, the organize re-classification prompt, and the
// skill footer) MUST derive from this slice so they can never drift. Order is the
// fixed slice/display order; personal is checked early so private facts are not
// swallowed by a broader category.
var Taxonomy = []Category{
	{
		Slug:        "identity",
		File:        "identity.md",
		Tier:        TierShared,
		Description: "who the user is — name, role, professional bio, persona",
		Keywords:    []string{"ceo", "founder", "chairman", "fundraising", "raising"},
	},
	{
		Slug:        "personal",
		File:        "personal.md",
		Tier:        TierPersonal,
		Description: "PRIVATE life — the USER'S OWN family, relationships, health, and their PERSONAL legal/financial matters (their own lawsuit, court case, divorce, custody, personal loan, salary, bank). NOT a company/business legal or financial matter — judge by context, not the topic word: if it is about the user as an individual or their family it is PERSONAL; if it is about the company/a client/the business it is NOT.",
		Keywords:    []string{"wife", "husband", "son", "daughter", "child", "children", "kids", "family", "pregnan", "baby", "newborn", "marriage", "married", "divorce", "custody", "alimony", "spouse", "fiance", "girlfriend", "boyfriend", "mother", "father", "sibling", "health", "medical"},
	},
	{
		Slug:        "preferences",
		File:        "preferences.md",
		Tier:        TierShared,
		Description: "coding style, workflow, editor/tool choices",
		Keywords:    nil, // default bucket — no keywords; everything unmatched lands here
	},
	{
		Slug:        "infra",
		File:        "infra.md",
		Tier:        TierShared,
		Description: "servers, IPs, OS, networking, hardware, services",
		Keywords:    []string{"server", "ip", "port", "vpn", "firewall", "pfsense", "gpu", "rtx", "docker", "vllm", "ollama", "n8n", "siem", "wazuh", "dns", "cloudflare", "gitlab", "hosting", "vps", "ovh", "cameras", "nvr", "frigate"},
	},
	{
		Slug:        "products",
		File:        "products.md",
		Tier:        TierShared,
		Description: "the user's products / portfolio",
		Keywords:    []string{"platform", "product", "portfolio", "app"},
	},
	{
		Slug:        "projects",
		File:        "projects.md",
		Tier:        TierShared,
		Description: "repos, active work, workspaces, project status — directory-backed: facts written from a workspace land in that project's own projects/<slug>.md; projects.md keeps cross-project notes",
		Keywords:    []string{"repo", "git", "project", "workspace", "folder", "directory"},
	},
	{
		Slug:        "daily",
		File:        "daily.md",
		Tier:        TierShared,
		Description: "dated journal / session work log",
		Keywords:    []string{"accomplished", "completed", "journal", "today", "log", "milestone", "done"},
	},
	{
		Slug:        "business",
		File:        "business.md",
		Tier:        TierShared,
		Description: "company/organizational matters — strategy, revenue, investors, pricing, AND the COMPANY's legal, financial, and contractual dealings (a company/client legal or money matter, not the user's own personal one)",
		Keywords:    []string{"strategy", "revenue", "investor", "valuation", "market", "competitor", "pricing"},
	},
	{
		Slug:        "agents",
		File:        "agents.md",
		Tier:        TierShared,
		Description: "AI-agent activity and onboarding events",
		Keywords:    []string{"agent", "onboarding", "mcp", "claude", "cursor", "codex", "gemini", "copilot"},
	},
}

// DefaultCategorySlug is where unmatched content lands.
const DefaultCategorySlug = "preferences"

// FileForCategory maps a category slug to its backing file. Unknown slugs fall
// back to the default category's file.
func FileForCategory(slug string) string {
	for _, c := range Taxonomy {
		if c.Slug == slug {
			return c.File
		}
	}
	return FileForCategory(DefaultCategorySlug)
}

// CategoryForFile returns the category backing a given file name. The projects
// category is directory-backed: any projects/<slug>.md belongs to it, so every
// tier/ACL/organize gate treats per-project sub-files exactly like projects.md.
func CategoryForFile(file string) (Category, bool) {
	norm := strings.ReplaceAll(file, "\\", "/")
	for _, c := range Taxonomy {
		if c.File == norm {
			return c, true
		}
	}
	if strings.HasPrefix(norm, "projects/") && strings.HasSuffix(norm, ".md") && !strings.Contains(strings.TrimPrefix(norm, "projects/"), "/") {
		return CategoryBySlug("projects")
	}
	return Category{}, false
}

// ProjectFile returns the per-project sub-file for a workspace root:
// projects/<slug>.md, where the slug is the workspace directory name sanitized
// to lowercase alphanumerics and dashes. No workspace → projects/general.md,
// so a global-context write still lands in a reviewable per-file bucket.
func ProjectFile(workspaceRoot string) string {
	slug := ProjectSlug(workspaceRoot)
	if slug == "" {
		return "projects/general.md"
	}
	return "projects/" + slug + ".md"
}

// ProjectSlug derives the project slug from a workspace root path ("" when no
// workspace). Lowercase alnum+dash, collapsed; "auxly-memory" ← /Users/x/projects/auxly-memory.
// Store.WorkspaceRoot is the marker dir INSIDE the repo (<repo>/.auxly/memory),
// so that suffix is stripped first — the slug names the repo, not the marker.
func ProjectSlug(workspaceRoot string) string {
	base := strings.TrimSpace(workspaceRoot)
	if base == "" {
		return ""
	}
	base = strings.ReplaceAll(base, "\\", "/")
	base = strings.TrimRight(base, "/")
	base = strings.TrimSuffix(base, "/.auxly/memory")
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(base) {
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

// CategoryBySlug returns the category for a slug.
func CategoryBySlug(slug string) (Category, bool) {
	for _, c := range Taxonomy {
		if c.Slug == slug {
			return c, true
		}
	}
	return Category{}, false
}

// RouteCategory picks the best category slug for free-text content using the
// keyword sets, in taxonomy order. This is the WRITE-TIME FALLBACK used when the
// agent does not specify a category; the taxonomy is also exposed to the agent so
// it can choose deliberately. Returns DefaultCategorySlug when nothing matches.
func RouteCategory(content string) string {
	// Tokenize on non-alphanumeric runes and match keywords as token PREFIXES.
	// Prefix (not raw substring) so stems like "pregnan" still match "pregnant"
	// while avoiding internal false positives such as "ip" inside "shipped".
	tokens := strings.FieldsFunc(strings.ToLower(content), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	for _, c := range Taxonomy {
		for _, kw := range c.Keywords {
			for _, tok := range tokens {
				if strings.HasPrefix(tok, kw) {
					return c.Slug
				}
			}
		}
	}
	return DefaultCategorySlug
}

// PersonalFiles returns the files in the personal tier (private bucket).
func PersonalFiles() []string {
	var out []string
	for _, c := range Taxonomy {
		if c.Tier == TierPersonal {
			out = append(out, c.File)
		}
	}
	return out
}

// SharedFiles returns the files in the shared tier (default-exposable).
func SharedFiles() []string {
	var out []string
	for _, c := range Taxonomy {
		if c.Tier == TierShared {
			out = append(out, c.File)
		}
	}
	return out
}

// IsPersonalFile reports whether a file belongs to the personal tier.
func IsPersonalFile(file string) bool {
	if c, ok := CategoryForFile(file); ok {
		return c.Tier == TierPersonal
	}
	return false
}

// IsEditableFile reports whether a file is a user-editable memory file — i.e. a
// canonical taxonomy category. Per-agent instruction/rules files (CLAUDE.md,
// CODEX.md, GEMINI.md, …), the protocol doc (providers.md), and the generated
// aggregate (unified_memory.md) are NOT in the taxonomy and stay read-only in
// the dashboard, so a hand-edit can never clobber a generated or rules file.
func IsEditableFile(file string) bool {
	_, ok := CategoryForFile(file)
	return ok
}

// IsOrganizableFile reports whether the on-demand organize/re-file pass may read
// and rewrite a file. Only USER-MEMORY taxonomy files qualify. Excluded:
//   - non-taxonomy setup/instruction files (CLAUDE.md, CODEX.md, GEMINI.md,
//     AGENTS.md, providers.md) and the generated aggregate (unified_memory.md) —
//     these are agent configuration/protocol, not the user's memory;
//   - the `agents` category (agents.md) — agent activity/onboarding bookkeeping
//     and provider setup, not user facts. Organize must never reshuffle it.
//
// This is the single gate used to filter BOTH the vault payload sent to the model
// and the proposed changes applied back, so a setup file can never be rewritten.
func IsOrganizableFile(file string) bool {
	c, ok := CategoryForFile(file)
	if !ok {
		return false
	}
	return c.Slug != "agents"
}

// OrganizableFiles returns the taxonomy files the organize pass may touch.
func OrganizableFiles() []string {
	var out []string
	for _, c := range Taxonomy {
		if c.Slug != "agents" {
			out = append(out, c.File)
		}
	}
	return out
}

// RenderForPrompt produces the canonical taxonomy block injected into skill
// prompts, the organize re-classification prompt, and the shared skill footer, so
// every agent files facts in the right place the first time.
func RenderForPrompt() string {
	return renderTaxonomy(nil, nil)
}

// RenderForPromptScoped renders the category guide limited to the files a caller
// may actually use. `include` reports whether a file is visible at all (rows that
// fail it are omitted entirely — a remote never even learns the file exists);
// `writable` reports whether it may also be written, used to annotate each row
// "read-only" vs "read & write". Passing nil for both reproduces the full,
// unannotated local-session guide (see RenderForPrompt).
func RenderForPromptScoped(include, writable func(file string) bool) string {
	return renderTaxonomy(include, writable)
}

func renderTaxonomy(include, writable func(file string) bool) string {
	var b strings.Builder
	b.WriteString("AUXLY MEMORY CATEGORIES (file : what belongs there):\n")
	for _, c := range Taxonomy {
		if include != nil && !include(c.File) {
			continue // remote scope: hide files this caller cannot access
		}
		tag := ""
		switch {
		case include == nil && c.Tier == TierPersonal:
			tag = "  [PRIVATE — off by default for remotes]"
		case writable != nil && writable(c.File):
			tag = "  [shared: read & write]"
		case writable != nil:
			tag = "  [shared: read-only]"
		}
		b.WriteString("- ")
		b.WriteString(c.File)
		b.WriteString(" : ")
		b.WriteString(c.Description)
		b.WriteString(tag)
		b.WriteString("\n")
	}
	return b.String()
}

// OrderedFiles returns the canonical display/slice order of files. Used by the
// memory profile display and the /auxly-max slice-by-category harvest order.
func OrderedFiles() []string {
	out := make([]string, 0, len(Taxonomy))
	for _, c := range Taxonomy {
		out = append(out, c.File)
	}
	return out
}
