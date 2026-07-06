package memory

import (
	"strings"
	"testing"
)

func TestRouteCategory(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"family routes to personal", "my wife is pregnant", "personal"},
		{"son routes to personal", "my son started school", "personal"},
		{"server routes to infra", "the server at 10.0.0.1 runs docker", "infra"},
		{"repo routes to projects", "pushed to the git repo", "projects"},
		{"founder routes to identity", "The user is the founder and CEO", "identity"},
		{"product routes to products", "the new platform shipped", "products"},
		{"journal routes to daily", "today I accomplished the migration", "daily"},
		{"unmatched falls back to preferences", "I like clean abstractions", "preferences"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RouteCategory(tt.content); got != tt.want {
				t.Errorf("RouteCategory(%q) = %q, want %q", tt.content, got, tt.want)
			}
		})
	}
}

func TestFileForCategory(t *testing.T) {
	if got := FileForCategory("infra"); got != "infra.md" {
		t.Errorf("FileForCategory(infra) = %q, want infra.md", got)
	}
	if got := FileForCategory("personal"); got != "personal.md" {
		t.Errorf("FileForCategory(personal) = %q, want personal.md", got)
	}
	// unknown slug falls back to the default category's file
	if got := FileForCategory("does-not-exist"); got != "preferences.md" {
		t.Errorf("FileForCategory(unknown) = %q, want preferences.md", got)
	}
}

func TestPersonalTier(t *testing.T) {
	if !IsPersonalFile("personal.md") {
		t.Error("personal.md should be a personal-tier file")
	}
	if IsPersonalFile("infra.md") {
		t.Error("infra.md should be shared, not personal")
	}
	if IsPersonalFile("identity.md") {
		t.Error("identity.md should be shared, not personal")
	}
	// inbox.md is personal too — quick-capture notes are private (off for
	// remotes by default). tasks.md is shared.
	if !IsPersonalFile("inbox.md") {
		t.Error("inbox.md should be personal-tier (private notes)")
	}
	if IsPersonalFile("tasks.md") {
		t.Error("tasks.md should be shared, not personal")
	}
	personal := PersonalFiles()
	want := map[string]bool{"personal.md": true, "inbox.md": true}
	if len(personal) != len(want) {
		t.Errorf("PersonalFiles() = %v, want keys %v", personal, want)
	}
	for _, f := range personal {
		if !want[f] {
			t.Errorf("PersonalFiles() has unexpected %q", f)
		}
	}
}

func TestTaxonomyIntegrity(t *testing.T) {
	seenSlug := map[string]bool{}
	seenFile := map[string]bool{}
	for _, c := range Taxonomy {
		if seenSlug[c.Slug] {
			t.Errorf("duplicate slug: %s", c.Slug)
		}
		if seenFile[c.File] {
			t.Errorf("duplicate file: %s", c.File)
		}
		seenSlug[c.Slug] = true
		seenFile[c.File] = true
		if c.Tier != TierShared && c.Tier != TierPersonal {
			t.Errorf("category %s has invalid tier %q", c.Slug, c.Tier)
		}
		if !strings.HasSuffix(c.File, ".md") {
			t.Errorf("category %s file %q must end in .md", c.Slug, c.File)
		}
	}
	if _, ok := CategoryBySlug(DefaultCategorySlug); !ok {
		t.Errorf("DefaultCategorySlug %q not present in taxonomy", DefaultCategorySlug)
	}
}

func TestRenderForPrompt(t *testing.T) {
	out := RenderForPrompt()
	for _, c := range Taxonomy {
		if c.Operational {
			// Operational files (inbox/tasks) are working files, not fact
			// destinations — they must NOT appear in the routing guide.
			if strings.Contains(out, c.File) {
				t.Errorf("RenderForPrompt should omit operational file %s", c.File)
			}
			continue
		}
		if !strings.Contains(out, c.File) {
			t.Errorf("RenderForPrompt missing %s", c.File)
		}
	}
	if !strings.Contains(out, "PRIVATE") {
		t.Error("RenderForPrompt should flag the personal tier as PRIVATE")
	}
}
