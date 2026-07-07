package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestVSCode_MCPConfigSchema verifies the VS Code target writes the schema VS
// Code actually reads: a top-level "servers" key (NOT "mcpServers"), with the
// entry carrying command/args/env.
func TestVSCode_MCPConfigSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	target := ideTarget{Path: path, AppName: "VS Code", BaseDir: dir, ProviderID: "vscode"}

	serverDef := localServerDef("/usr/local/bin/auxly", "/home/u/.auxly/memory", "vscode")

	if app, err := writeMCPConfigEntry(target, serverDef); app == "" || err != nil {
		t.Fatalf("writeMCPConfigEntry returned empty/skip (err=%v) for the VS Code target", err)
	}

	var cfg map[string]any
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("invalid JSON written: %v", err)
	}
	if _, wrong := cfg["mcpServers"]; wrong {
		t.Errorf("VS Code must NOT use mcpServers (it ignores it): %s", data)
	}
	servers, ok := cfg["servers"].(map[string]any)
	if !ok {
		t.Fatalf("expected top-level servers map, got: %s", data)
	}
	entry, ok := servers["auxly-memory"].(map[string]any)
	if !ok {
		t.Fatalf("expected auxly-memory under servers, got: %s", data)
	}
	if entry["command"] == nil || entry["args"] == nil {
		t.Errorf("VS Code entry missing command/args: %v", entry)
	}
}

// TestVSCode_MigratesLegacyMcpServersEntry verifies that a stale auxly-memory
// written by an older (wrong-keyed) auxly under "mcpServers" is moved to
// "servers", while any unrelated mcpServers entry the user has is preserved.
func TestVSCode_MigratesLegacyMcpServersEntry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	pre := map[string]any{
		"mcpServers": map[string]any{
			"auxly-memory": map[string]any{"command": "old"},
			"other-tool":   map[string]any{"command": "keepme"},
		},
	}
	b, _ := json.Marshal(pre)
	os.WriteFile(path, b, 0o644)

	target := ideTarget{Path: path, AppName: "VS Code", BaseDir: dir, ProviderID: "vscode"}
	if _, err := writeMCPConfigEntry(target, localServerDef("/bin/auxly", "/m", "vscode")); err != nil {
		t.Fatal(err)
	}

	var cfg map[string]any
	data, _ := os.ReadFile(path)
	json.Unmarshal(data, &cfg)

	servers := cfg["servers"].(map[string]any)
	if _, ok := servers["auxly-memory"]; !ok {
		t.Errorf("auxly-memory should be under servers now: %s", data)
	}
	legacy, ok := cfg["mcpServers"].(map[string]any)
	if !ok {
		t.Fatalf("unrelated mcpServers entries must be preserved: %s", data)
	}
	if _, gone := legacy["auxly-memory"]; gone {
		t.Errorf("stale auxly-memory under mcpServers should be removed: %s", data)
	}
	if _, kept := legacy["other-tool"]; !kept {
		t.Errorf("unrelated other-tool must be kept: %s", data)
	}
}

// TestVSCodeTarget_Registered confirms knownIDETargets wires the VS Code
// user-level mcp.json.
func TestVSCodeTarget_Registered(t *testing.T) {
	home := t.TempDir()
	var found bool
	for _, tg := range knownIDETargets(home) {
		if tg.ProviderID == "vscode" && filepath.Base(tg.Path) == "mcp.json" &&
			filepath.Base(filepath.Dir(tg.Path)) == "User" {
			found = true
		}
	}
	if !found {
		t.Error("knownIDETargets is missing the VS Code User/mcp.json target")
	}
}
