package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOpencodePluginGeneratedContent verifies:
// Generated plugin text contains both event names and the 8000 threshold (string assertions).
func TestOpencodePluginGeneratedContent(t *testing.T) {
	fakeAuxly := "/usr/local/bin/auxly"
	content, err := generateOpencodePlugin(fakeAuxly)
	if err != nil {
		t.Fatalf("generateOpencodePlugin failed: %v", err)
	}

	// Must contain both event names
	if !strings.Contains(content, "session.idle") {
		t.Errorf("plugin missing 'session.idle' event check, got content:\n%s", content)
	}
	if !strings.Contains(content, "session.status") {
		t.Errorf("plugin missing 'session.status' event check, got content:\n%s", content)
	}

	// Must contain 8000 threshold
	if !strings.Contains(content, "8000") {
		t.Errorf("plugin missing 8000 char threshold check, got content:\n%s", content)
	}

	// Must contain the injected auxly path
	if !strings.Contains(content, fakeAuxly) {
		t.Errorf("plugin missing injected auxly path %q, got content:\n%s", fakeAuxly, content)
	}
}

// TestOpencodeInstallUninstallStatus verifies:
// opencode install/uninstall/status in a temp HOME: file created with the injected binary path,
// idempotent, uninstall removes only our file.
func TestOpencodeInstallUninstallStatus(t *testing.T) {
	fakeHome := t.TempDir()
	pluginPath := opencodePluginPath(fakeHome)
	fakeAuxly := filepath.Join(fakeHome, "bin", "auxly")

	// 1. Initial status: not installed
	status, detail := opencodeHookStatus(fakeHome)
	if status != "not-installed" {
		t.Fatalf("expected status 'not-installed', got %q (%s)", status, detail)
	}

	// Plant another foreign file in plugins dir to verify uninstall removes ONLY our file
	otherPlugin := filepath.Join(filepath.Dir(pluginPath), "other-plugin.js")
	if err := os.MkdirAll(filepath.Dir(otherPlugin), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(otherPlugin, []byte("// other plugin\n"), 0644); err != nil {
		t.Fatalf("write other plugin: %v", err)
	}

	// 2. Install: file created with the injected binary path
	changed, err := installOpencodeHookAtWithExe(pluginPath, fakeAuxly)
	if err != nil {
		t.Fatalf("installOpencodeHookAtWithExe: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true on fresh install")
	}

	data, err := os.ReadFile(pluginPath)
	if err != nil {
		t.Fatalf("read plugin: %v", err)
	}
	if !strings.Contains(string(data), fakeAuxly) {
		t.Fatalf("plugin does not contain injected binary path %q", fakeAuxly)
	}

	// Other file must still exist untouched
	if _, err := os.Stat(otherPlugin); err != nil {
		t.Fatalf("other plugin was touched or removed: %v", err)
	}

	// 3. Status: WIRED
	status, detail = opencodeHookStatus(fakeHome)
	if status != "WIRED" {
		t.Fatalf("expected status 'WIRED', got %q (%s)", status, detail)
	}
	if detail != pluginPath {
		t.Fatalf("expected detail %q, got %q", pluginPath, detail)
	}

	// 4. Idempotent install: rewriting only if content differs
	infoBefore, err := os.Stat(pluginPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	changed, err = installOpencodeHookAtWithExe(pluginPath, fakeAuxly)
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if changed {
		t.Fatal("expected changed=false on idempotent re-install")
	}
	infoAfter, err := os.Stat(pluginPath)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	if infoBefore.ModTime() != infoAfter.ModTime() {
		t.Fatalf("idempotent install should not rewrite file if content is identical")
	}

	// 5. Uninstall removes only our file
	removed, err := uninstallOpencodeHookAt(pluginPath)
	if err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if !removed {
		t.Fatal("expected removed=true on uninstall")
	}
	if _, err := os.Stat(pluginPath); !os.IsNotExist(err) {
		t.Fatalf("plugin file was not removed")
	}

	// Foreign file must still exist
	if _, err := os.Stat(otherPlugin); err != nil {
		t.Fatalf("uninstall must remove ONLY our file, but otherPlugin is missing: %v", err)
	}

	// 6. Status after uninstall: not-installed
	status, detail = opencodeHookStatus(fakeHome)
	if status != "not-installed" {
		t.Fatalf("expected status 'not-installed' after uninstall, got %q (%s)", status, detail)
	}

	// 7. Idempotent uninstall: no error
	removed, err = uninstallOpencodeHookAt(pluginPath)
	if err != nil {
		t.Fatalf("idempotent uninstall err: %v", err)
	}
	if removed {
		t.Fatal("expected removed=false on second uninstall")
	}
}
