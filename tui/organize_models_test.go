package tui

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestCodexCacheParsing verifies filtering by visibility "list", sorting by
// priority ascending (stable), handling missing files, and respecting CODEX_HOME.
func TestCodexCacheParsing(t *testing.T) {
	// 1. Visibility filter & priority order (stable)
	rawJSON := []byte(`{
		"models": [
			{"slug": "gpt-beta", "visibility": "list", "priority": 10},
			{"slug": "gpt-hidden", "visibility": "hidden", "priority": 1},
			{"slug": "gpt-alpha", "visibility": "list", "priority": 2},
			{"slug": "gpt-gamma", "visibility": "list", "priority": 2},
			{"slug": "gpt-internal", "visibility": "internal", "priority": 0}
		]
	}`)

	slugs, err := parseCodexModelsCache(rawJSON)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	wantSlugs := []string{"gpt-alpha", "gpt-gamma", "gpt-beta"}
	if !reflect.DeepEqual(slugs, wantSlugs) {
		t.Fatalf("parseCodexModelsCache = %v, want %v", slugs, wantSlugs)
	}

	// 2. CODEX_HOME respected (temp dir)
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	wantPath := filepath.Join(codexHome, "models_cache.json")
	if gotPath := codexCachePath(); gotPath != wantPath {
		t.Fatalf("codexCachePath with CODEX_HOME = %q, want %q", gotPath, wantPath)
	}

	validCache := []byte(`{"models":[{"slug":"gpt-6.1-sol","visibility":"list","priority":1}]}`)
	if err := os.WriteFile(wantPath, validCache, 0644); err != nil {
		t.Fatal(err)
	}

	discovered, err := defaultDiscoverCodexModels()
	if err != nil {
		t.Fatalf("defaultDiscoverCodexModels failed: %v", err)
	}
	if !reflect.DeepEqual(discovered, []string{"gpt-6.1-sol"}) {
		t.Fatalf("discovered = %v, want [gpt-6.1-sol]", discovered)
	}

	// 3. Fallback when CODEX_HOME is unset, HOME is respected
	t.Setenv("CODEX_HOME", "")
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	wantHomePath := filepath.Join(fakeHome, ".codex", "models_cache.json")
	if gotPath := codexCachePath(); gotPath != wantHomePath {
		t.Fatalf("codexCachePath with HOME = %q, want %q", gotPath, wantHomePath)
	}

	// 4. Missing file returns error so discovery falls back
	t.Setenv("CODEX_HOME", t.TempDir()) // empty dir, models_cache.json does not exist
	if _, err := defaultDiscoverCodexModels(); err == nil {
		t.Fatal("expected error for missing models_cache.json, got nil")
	}
}

// TestAgyOutputParsing verifies that lines without a tab (e.g. "Fetching available models...")
// are ignored and the first tab/whitespace field of lines with a tab is extracted.
func TestAgyOutputParsing(t *testing.T) {
	output := "Fetching available models...\n" +
		"gemini-4-argon-high\tGemini 4 Argon (High)\n" +
		"gemini-3.8-flash-high\tGemini 3.8 Flash (High)\n" +
		"claude-sonnet-5-5-high\tClaude Sonnet 5.5 (High)\n" +
		"\n" +
		"Some trailing log message without tab\n"

	models := parseAgyModelsOutput(output)
	want := []string{"gemini-4-argon-high", "gemini-3.8-flash-high", "claude-sonnet-5-5-high"}
	if !reflect.DeepEqual(models, want) {
		t.Fatalf("parseAgyModelsOutput = %v, want %v", models, want)
	}
}

// TestModelsForProvider_Discovery verifies that stubbed discovery results are
// prefixed with "(default)" and suffixed with "custom…", and stubbed failure
// falls back silently to the static list.
func TestModelsForProvider_Discovery(t *testing.T) {
	clearAgentModelsCache()
	defer clearAgentModelsCache()

	oldCodex := discoverCodexModels
	defer func() { discoverCodexModels = oldCodex }()

	p := orgProvider{kind: "agent", id: "codex"}

	// 1. Stubbed discovery result is used and prefixed with "(default)" and suffixed with "custom…"
	discoverCodexModels = func() ([]string, error) {
		return []string{"gpt-6.1-sol", "gpt-6-luna"}, nil
	}

	discovered := discoverAgentModels(p)
	want := []string{"(default)", "gpt-6.1-sol", "gpt-6-luna", "custom…"}
	if !reflect.DeepEqual(discovered, want) {
		t.Fatalf("discoverAgentModels = %v, want %v", discovered, want)
	}
	if got := modelsForProvider(p); !reflect.DeepEqual(got, want) {
		t.Fatalf("modelsForProvider after discovery = %v, want %v", got, want)
	}

	// 2. Stubbed failure falls back silently to the static list
	clearAgentModelsCache()
	discoverCodexModels = func() ([]string, error) {
		return nil, errors.New("codex binary error")
	}

	fallback := discoverAgentModels(p)
	wantFallback := []string{"(default)", "gpt-6.1-sol", "gpt-6-luna", "gpt-6-astra", "custom…"}
	if !reflect.DeepEqual(fallback, wantFallback) {
		t.Fatalf("discoverAgentModels on failure = %v, want %v", fallback, wantFallback)
	}
	if got := modelsForProvider(p); !reflect.DeepEqual(got, wantFallback) {
		t.Fatalf("modelsForProvider on failure = %v, want %v", got, wantFallback)
	}
}

// TestModelValueForProvider locks the parsing rules:
// - A label starting with "(" returns "" (agent default).
// - "claude-haiku-5-5 (fast)" -> "claude-haiku-5-5".
// - Otherwise the first whitespace-separated field.
func TestModelValueForProvider(t *testing.T) {
	agentP := orgProvider{kind: "agent", id: "claude"}

	cases := []struct {
		label string
		want  string
	}{
		{"claude-haiku-5-5 (fast)", "claude-haiku-5-5"},
		{"(default)", ""},
		{"(uses its configured model)", ""},
		{"claude-sonnet-5-5 (recommended)", "claude-sonnet-5-5"},
		{"claude-opus-5-5", "claude-opus-5-5"},
		{"claude-fable-5-1", "claude-fable-5-1"},
		{"custom: my-model-123", "my-model-123"},
		{"custom…", ""},
		{"", ""},
	}

	for _, tc := range cases {
		got := modelValueForProvider(agentP, tc.label)
		if got != tc.want {
			t.Errorf("modelValueForProvider(%q) = %q, want %q", tc.label, got, tc.want)
		}
	}
}

// TestCustomModelEntry verifies that selecting "custom…", pressing enter, typing a model ID,
// and pressing enter sets the picked model and updates the displayed label, while esc cancels.
func TestCustomModelEntry(t *testing.T) {
	clearAgentModelsCache()
	defer clearAgentModelsCache()

	store := organizeTestStore(t)
	m := newOrganizeModel(store, store.Root, nil)
	m.providers = []orgProvider{
		{kind: "agent", id: "claude", label: "Claude Code", command: "/bin/echo"},
	}
	m.provIdx = 0
	m.resetProviderModels()

	// 1. Move focus to Model block and down to "custom…"
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	for i := 0; i < len(m.models)-1; i++ {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	}

	if m.models[m.modelIdx] != "custom…" {
		t.Fatalf("expected cursor at 'custom…', got %q", m.models[m.modelIdx])
	}

	// 2. Press Enter to open custom input field
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.modelEditing {
		t.Fatal("expected modelEditing = true after Enter on 'custom…'")
	}
	if !m.capturesInput() {
		t.Fatal("expected capturesInput() = true while modelEditing is active")
	}

	// 3. Type model ID: "my-custom-model"
	for _, ch := range "my-custom-model" {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}})
	}
	if m.customInput.Value() != "my-custom-model" {
		t.Fatalf("customInput value = %q, want 'my-custom-model'", m.customInput.Value())
	}

	// 4. Press Enter to accept
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.modelEditing {
		t.Fatal("expected modelEditing = false after Enter")
	}
	if m.picked != "my-custom-model" {
		t.Fatalf("m.picked = %q, want 'my-custom-model'", m.picked)
	}
	if m.models[m.modelIdx] != "custom: my-custom-model" {
		t.Fatalf("displayed model label = %q, want 'custom: my-custom-model'", m.models[m.modelIdx])
	}

	_, _, planModel := m.planTarget()
	if planModel != "my-custom-model" {
		t.Fatalf("planTarget model = %q, want 'my-custom-model'", planModel)
	}

	// 5. Esc cancels: open custom entry again, type something, then esc
	m.models = []string{"(default)", "custom…"}
	m.modelIdx = 1
	m.picked = ""

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.modelEditing {
		t.Fatal("expected modelEditing = true")
	}
	for _, ch := range "cancelled-model" {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}})
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.modelEditing {
		t.Fatal("expected modelEditing = false after Esc")
	}
	if m.picked == "cancelled-model" {
		t.Fatal("picked model should not be set to cancelled-model after Esc")
	}

	// 6. Empty enter cancels
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.modelEditing {
		t.Fatal("expected modelEditing = true")
	}
	m.customInput.SetValue("")
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.modelEditing {
		t.Fatal("expected modelEditing = false after empty Enter")
	}
	if m.picked == "cancelled-model" {
		t.Fatal("picked model should not be set after empty Enter")
	}
}
