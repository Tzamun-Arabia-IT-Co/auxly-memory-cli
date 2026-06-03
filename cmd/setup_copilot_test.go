package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestCopilotCLI_MCPConfigSchema verifies the GitHub Copilot CLI target writes the
// schema Copilot actually expects: a top-level "mcpServers" map whose entry carries
// type:"local" and tools:["*"] in addition to command/args/env.
func TestCopilotCLI_MCPConfigSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp-config.json")
	target := ideTarget{Path: path, AppName: "GitHub Copilot CLI", BaseDir: dir, ProviderID: "copilot"}

	serverDef := localServerDef("/usr/local/bin/auxly", "/home/u/.auxly/memory", "copilot")
	serverDef["type"] = "local"
	serverDef["tools"] = []interface{}{"*"}

	if app := writeMCPConfigEntry(target, serverDef); app == "" {
		t.Fatal("writeMCPConfigEntry returned empty (skip/error) for the Copilot target")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("invalid JSON written: %v", err)
	}
	servers, ok := cfg["mcpServers"].(map[string]any)
	if !ok {
		t.Fatalf("expected top-level mcpServers map, got: %s", data)
	}
	entry, ok := servers["auxly-memory"].(map[string]any)
	if !ok {
		t.Fatalf("expected auxly-memory entry under mcpServers, got: %s", data)
	}
	if entry["type"] != "local" {
		t.Errorf("Copilot entry needs type=local, got %v", entry["type"])
	}
	if entry["command"] == nil || entry["args"] == nil {
		t.Errorf("Copilot entry missing command/args: %v", entry)
	}
	tools, ok := entry["tools"].([]any)
	if !ok || len(tools) != 1 || tools[0] != "*" {
		t.Errorf("Copilot entry needs tools=[\"*\"], got %v", entry["tools"])
	}
}

// TestCopilotTarget_Registered confirms knownIDETargets includes the Copilot CLI
// config path so `auxly setup` actually wires it.
func TestCopilotTarget_Registered(t *testing.T) {
	home := t.TempDir()
	var found bool
	for _, tg := range knownIDETargets(home) {
		if tg.ProviderID == "copilot" && filepath.Base(tg.Path) == "mcp-config.json" {
			found = true
		}
	}
	if !found {
		t.Error("knownIDETargets must include the GitHub Copilot CLI mcp-config.json target")
	}
}
