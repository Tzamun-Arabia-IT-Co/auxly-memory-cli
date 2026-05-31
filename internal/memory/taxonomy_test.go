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
		{"family routes to personal", "my wife Hanan is pregnant", "personal"},
		{"son routes to personal", "my son started school", "personal"},
		{"server routes to infra", "the server at 192.168.1.1 runs docker", "infra"},
		{"repo routes to projects", "pushed to the git repo", "projects"},
		{"founder routes to identity", "Wael is the founder and CEO", "identity"},
		{"product routes to products", "the Raqeb platform shipped", "products"},
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
	personal := PersonalFiles()
	if len(personal) != 1 || personal[0] != "personal.md" {
		t.Errorf("PersonalFiles() = %v, want [personal.md]", personal)
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
		if !strings.Contains(out, c.File) {
			t.Errorf("RenderForPrompt missing %s", c.File)
		}
	}
	if !strings.Contains(out, "PRIVATE") {
		t.Error("RenderForPrompt should flag the personal tier as PRIVATE")
	}
}
