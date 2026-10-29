package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

var (
	agentModelsCache   = make(map[string][]string)
	agentModelsCacheMu sync.Mutex

	// discoverCodexModels is a package-level function variable so tests can stub it.
	discoverCodexModels = defaultDiscoverCodexModels

	// discoverAntigravityModels is a package-level function variable so tests can stub it.
	discoverAntigravityModels = defaultDiscoverAntigravityModels

	// codexCachePath resolves the models_cache.json path ($CODEX_HOME or ~/.codex).
	codexCachePath = defaultCodexCachePath
)

func defaultCodexCachePath() string {
	if ch := os.Getenv("CODEX_HOME"); ch != "" {
		return filepath.Join(ch, "models_cache.json")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = os.Getenv("HOME")
	}
	return filepath.Join(home, ".codex", "models_cache.json")
}

type codexModelEntry struct {
	Slug       string `json:"slug"`
	Visibility string `json:"visibility"`
	Priority   int    `json:"priority"`
}

type codexCacheFile struct {
	Models []codexModelEntry `json:"models"`
}

func parseCodexModelsCache(data []byte) ([]string, error) {
	var c codexCacheFile
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	var filtered []codexModelEntry
	for _, m := range c.Models {
		if strings.EqualFold(m.Visibility, "list") && strings.TrimSpace(m.Slug) != "" {
			filtered = append(filtered, m)
		}
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		return filtered[i].Priority < filtered[j].Priority
	})
	slugs := make([]string, 0, len(filtered))
	for _, m := range filtered {
		slugs = append(slugs, m.Slug)
	}
	return slugs, nil
}

func defaultDiscoverCodexModels() ([]string, error) {
	path := codexCachePath()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseCodexModelsCache(data)
}

func parseAgyModelsOutput(output string) []string {
	var models []string
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if !strings.Contains(line, "\t") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] != "" {
			models = append(models, fields[0])
		}
	}
	return models
}

func defaultDiscoverAntigravityModels(agyPath string) ([]string, error) {
	bin := agyPath
	if bin == "" {
		bin = "agy"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "models")
	cmd.Stdin = bytes.NewReader(nil)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return parseAgyModelsOutput(string(out)), nil
}

func getCachedAgentModels(key string) ([]string, bool) {
	agentModelsCacheMu.Lock()
	defer agentModelsCacheMu.Unlock()
	m, ok := agentModelsCache[key]
	if ok {
		return append([]string(nil), m...), true
	}
	return nil, false
}

func setCachedAgentModels(key string, models []string) {
	agentModelsCacheMu.Lock()
	defer agentModelsCacheMu.Unlock()
	agentModelsCache[key] = append([]string(nil), models...)
}

func clearAgentModelsCache() {
	agentModelsCacheMu.Lock()
	defer agentModelsCacheMu.Unlock()
	agentModelsCache = make(map[string][]string)
}

func clearAgentModelCache(key string) {
	agentModelsCacheMu.Lock()
	defer agentModelsCacheMu.Unlock()
	delete(agentModelsCache, key)
}

func ensureCustomEntry(models []string) []string {
	if len(models) == 0 {
		return []string{"custom…"}
	}
	for _, m := range models {
		if m == "custom…" || strings.HasPrefix(m, "custom:") {
			return append([]string(nil), models...)
		}
	}
	res := make([]string, len(models)+1)
	copy(res, models)
	res[len(models)] = "custom…"
	return res
}

func needsAgentDiscovery(p orgProvider) bool {
	if p.kind != "agent" {
		return false
	}
	key := orgProviderKey(p.id)
	if key != "codex" && key != "antigravity" {
		return false
	}
	_, ok := getCachedAgentModels(key)
	return !ok
}

func discoverAgentModels(p orgProvider) []string {
	key := orgProviderKey(p.id)
	if cached, ok := getCachedAgentModels(key); ok {
		return cached
	}

	var discovered []string
	var err error

	switch key {
	case "codex":
		discovered, err = discoverCodexModels()
	case "antigravity":
		discovered, err = discoverAntigravityModels(p.command)
	default:
		// claude, gemini, cursor: no discovery (static list only)
	}

	var finalModels []string
	if err == nil && len(discovered) > 0 {
		finalModels = append([]string{"(default)"}, discovered...)
	} else {
		// If discovery fails or returns nothing, use the static list silently.
		if staticList, ok := agentModels[key]; ok && len(staticList) > 0 {
			finalModels = append([]string(nil), staticList...)
		} else {
			finalModels = []string{"(default)"}
		}
	}
	finalModels = ensureCustomEntry(finalModels)
	setCachedAgentModels(key, finalModels)
	return finalModels
}

func (m organizeModel) fetchAgentModelsCmd(p orgProvider) tea.Cmd {
	return func() tea.Msg {
		models := discoverAgentModels(p)
		return orgModelsFetchedMsg{
			provider: p.id,
			success:  true,
			models:   models,
		}
	}
}
