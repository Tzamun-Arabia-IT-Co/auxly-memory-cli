package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

var hooksCmd = &cobra.Command{
	Use:   "hooks",
	Short: "Install/remove agent hooks that auto-capture session facts (opt-in)",
}

// hooksAgent backs --agent on both install and uninstall. A single package
// var is safe here since a CLI process runs exactly one subcommand.
var hooksAgent string

// supportedHookAgents lists every agent wireable via `--agent`; keep in sync
// with the router below and `hooks status`'s row list.
var supportedHookAgents = []string{"claude", "codex", "gemini", "kimi", "antigravity"}

var hooksInstallCmd = &cobra.Command{
	Use:          "install",
	Short:        "Wire an agent's session-end hook to run `auxly capture` (default: Claude Code)",
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := installHookForAgent(hooksAgent); err != nil {
			return err
		}
		// Explicit install is fresh consent — clear any prior auto-wire opt-out.
		if home, herr := os.UserHomeDir(); herr == nil {
			clearAutoHookOptOut(home, hooksAgent)
		}
		return nil
	},
}

var hooksUninstallCmd = &cobra.Command{
	Use:          "uninstall",
	Short:        "Remove the auxly capture hook for an agent",
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := uninstallHookForAgent(hooksAgent); err != nil {
			return err
		}
		// A deliberate uninstall must stick — record it so the next setup/connect
		// auto-wire doesn't silently re-add this agent.
		if home, herr := os.UserHomeDir(); herr == nil {
			recordAutoHookOptOut(home, hooksAgent)
		}
		return nil
	},
}

var hooksStatusCmd = &cobra.Command{
	Use:          "status",
	Short:        "Show which agents have auxly capture wired",
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runHooksStatus()
	},
}

func init() {
	hooksInstallCmd.Flags().StringVar(&hooksAgent, "agent", "claude", "agent to wire: claude, codex, gemini, kimi, antigravity")
	hooksUninstallCmd.Flags().StringVar(&hooksAgent, "agent", "claude", "agent to unwire: claude, codex, gemini, kimi, antigravity")
	hooksCmd.AddCommand(hooksInstallCmd)
	hooksCmd.AddCommand(hooksUninstallCmd)
	hooksCmd.AddCommand(hooksStatusCmd)
	rootCmd.AddCommand(hooksCmd)
}

// installHookForAgent routes to the agent-specific installer. Claude and the
// shell-wrapper agents print their own outcome here; codex's installer
// already prints its own (see hooks_codex.go), so it isn't repeated.
func installHookForAgent(agent string) error {
	switch agent {
	case "claude":
		path, err := claudeSettingsPath()
		if err != nil {
			return err
		}
		changed, err := installCaptureHook(path)
		if err != nil {
			return err
		}
		if changed {
			fmt.Println("✅ Stop hook installed — facts from each Claude Code session flow into `auxly pending`.")
			fmt.Println("   Remove any time with `auxly hooks uninstall`.")
		} else {
			fmt.Println("✓ Stop hook already installed — nothing to do.")
		}
		return nil
	case "codex":
		return installCodexHook()
	case "gemini", "kimi", "antigravity":
		already, err := shellWrapperInstalled(agent)
		if err != nil {
			return err
		}
		if err := installShellWrapper(agent); err != nil {
			return err
		}
		path, _ := shellRCPath()
		if already {
			fmt.Printf("✓ %s capture wrapper already installed in %s — nothing to do.\n", agent, path)
		} else {
			fmt.Printf("✅ %s capture wrapper added to %s — restart your shell (or `source %s`).\n", agent, path, path)
			fmt.Println("   Honest caveat: this captures raw terminal output via `script`, not a structured transcript.")
		}
		return nil
	default:
		return fmt.Errorf("unsupported --agent %q — supported: %s", agent, strings.Join(supportedHookAgents, ", "))
	}
}

// uninstallHookForAgent is installHookForAgent's mirror image.
func uninstallHookForAgent(agent string) error {
	switch agent {
	case "claude":
		path, err := claudeSettingsPath()
		if err != nil {
			return err
		}
		removed, err := uninstallCaptureHook(path)
		if err != nil {
			return err
		}
		if removed {
			fmt.Println("🗑️  auxly capture hook removed.")
		} else {
			fmt.Println("✓ No auxly capture hook found — nothing to remove.")
		}
		return nil
	case "codex":
		return uninstallCodexHook()
	case "gemini", "kimi", "antigravity":
		removed, err := uninstallShellWrapper(agent)
		if err != nil {
			return err
		}
		if removed {
			fmt.Printf("🗑️  %s capture wrapper removed.\n", agent)
		} else {
			fmt.Printf("✓ No %s capture wrapper found — nothing to remove.\n", agent)
		}
		return nil
	default:
		return fmt.Errorf("unsupported --agent %q — supported: %s", agent, strings.Join(supportedHookAgents, ", "))
	}
}

// runHooksStatus prints one row per supported agent: WIRED (hook/wrapper
// present), manual (needs attention — detail says why), or not-installed.
func runHooksStatus() error {
	fmt.Println("🔌 Capture hook status")
	fmt.Printf("   %-8s %-14s %s\n", "AGENT", "STATUS", "DETAIL")

	// claude: settings.json Stop hook presence.
	claudeStatus, claudeDetail := "not-installed", "run `auxly hooks install`"
	if path, err := claudeSettingsPath(); err == nil {
		if installed, ierr := captureHookInstalled(path); ierr != nil {
			claudeStatus, claudeDetail = "manual", ierr.Error()
		} else if installed {
			claudeStatus, claudeDetail = "WIRED", path
		}
	}
	fmt.Printf("   %-8s %-14s %s\n", "claude", claudeStatus, claudeDetail)

	// codex: the parallel notify-hook check, reused as-is. codexHookStatus
	// returns a typed state, so "manual" (a foreign notify program `hooks
	// install --agent codex` refuses to overwrite) is a direct comparison —
	// no string-matching the human-readable detail.
	state, detail := codexHookStatus()
	codexStatus := "not-installed"
	switch state {
	case codexHookWired:
		codexStatus = "WIRED"
	case codexHookForeign:
		codexStatus = "manual"
	}
	fmt.Printf("   %-8s %-14s %s\n", "codex", codexStatus, detail)

	// gemini/kimi/antigravity: shell-wrapper marked block presence in the rc file.
	for _, agent := range shellWrapperAgents {
		status, detail := "not-installed", fmt.Sprintf("run `auxly hooks install --agent %s`", agent)
		installed, err := shellWrapperInstalled(agent)
		if err != nil {
			status, detail = "manual", err.Error()
		} else if installed {
			path, _ := shellRCPath()
			status, detail = "WIRED", path
		}
		fmt.Printf("   %-8s %-14s %s\n", agent, status, detail)
	}
	return nil
}

func claudeSettingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

// claudeInstalled reports whether Claude Code looks present on this machine —
// gated on the ~/.claude config DIR existing, not on a `claude` binary being
// on PATH. installCaptureHook creates settings.json from scratch when it's
// missing, so a PATH-only signal would make auto-wire create a fresh
// ~/.claude for an agent that was never actually set up here.
func claudeInstalled(home string) bool {
	info, err := os.Stat(filepath.Join(home, ".claude"))
	return err == nil && info.IsDir()
}

// codexInstalled mirrors claudeInstalled: gated on CODEX_HOME (or ~/.codex)
// already existing, since installCodexHookQuiet creates config.toml from
// scratch when the file is absent.
func codexInstalled() bool {
	info, err := os.Stat(codexHomeDir())
	return err == nil && info.IsDir()
}

// geminiInstalled reports whether the `gemini` CLI is on PATH.
func geminiInstalled() bool {
	_, err := exec.LookPath("gemini")
	return err == nil
}

// kimiInstalled reports whether the `kimi` CLI is on PATH, or its config dir
// (current ~/.kimi-code or legacy ~/.kimi) exists.
func kimiInstalled(home string) bool {
	if _, err := exec.LookPath("kimi"); err == nil {
		return true
	}
	for _, dir := range []string{".kimi-code", ".kimi"} {
		if info, err := os.Stat(filepath.Join(home, dir)); err == nil && info.IsDir() {
			return true
		}
	}
	return false
}

// antigravityInstalled reports whether the `agy` CLI (antigravity's actual
// binary name) is on PATH, or ~/.antigravity exists.
func antigravityInstalled(home string) bool {
	if _, err := exec.LookPath("agy"); err == nil {
		return true
	}
	info, err := os.Stat(filepath.Join(home, ".antigravity"))
	return err == nil && info.IsDir()
}

// autoWireCleanHooks wires the capture hook for every hook-capable agent that
// is actually present on this machine, during `auxly setup`/`auxly connect`:
// claude and codex get their native settings-file hook; gemini, kimi and
// antigravity — which have no session-end hook — get the ~/.zshrc shell
// wrapper (see hooks_shell.go). Editing ~/.zshrc for an installed agent is a
// deliberate product decision: auto-wire everything installed, not just the
// "clean" settings-file agents. Returns the agent names actually wired (nil
// if none). Never fails setup/connect: a Codex foreign-notify conflict is
// swallowed here (a manual `auxly hooks install --agent codex` still
// surfaces it as an error).
func autoWireCleanHooks(home string) []string {
	switch strings.ToLower(os.Getenv("AUXLY_NO_AUTO_HOOKS")) {
	case "1", "true", "yes":
		return nil
	}

	var wired []string
	if claudeInstalled(home) && !autoHookOptedOut(home, "claude") {
		if path, err := claudeSettingsPath(); err == nil {
			if changed, err := installCaptureHook(path); err == nil && changed {
				wired = append(wired, "claude")
			}
		}
	}
	if codexInstalled() && !autoHookOptedOut(home, "codex") {
		if changed, err := installCodexHookQuiet(); err == nil && changed {
			wired = append(wired, "codex")
		}
	}

	shellAgentPresent := map[string]bool{
		"gemini":      geminiInstalled(),
		"kimi":        kimiInstalled(home),
		"antigravity": antigravityInstalled(home),
	}
	for _, agent := range shellWrapperAgents {
		if !shellAgentPresent[agent] || autoHookOptedOut(home, agent) {
			continue
		}
		already, err := shellWrapperInstalled(agent)
		if err != nil || already {
			continue
		}
		if err := installShellWrapper(agent); err == nil {
			wired = append(wired, agent)
		}
	}
	return wired
}

// autoHookOptOutPath is the machine-global ledger of agents the user has
// EXPLICITLY unwired. Auto-wire (setup/connect) skips anything listed here, so a
// deliberate `hooks uninstall` sticks instead of silently reappearing next
// setup — the trust guard for a feature that edits ~/.zshrc on its own.
func autoHookOptOutPath(home string) string {
	return filepath.Join(home, ".auxly", "autohook-optout")
}

func autoHookOptOutSet(home string) map[string]bool {
	set := map[string]bool{}
	data, err := os.ReadFile(autoHookOptOutPath(home))
	if err != nil {
		return set
	}
	for _, line := range strings.Split(string(data), "\n") {
		if a := strings.TrimSpace(line); a != "" {
			set[a] = true
		}
	}
	return set
}

func autoHookOptedOut(home, agent string) bool {
	return autoHookOptOutSet(home)[agent]
}

// recordAutoHookOptOut marks agent as deliberately unwired (called on an
// explicit `hooks uninstall`). Idempotent.
func recordAutoHookOptOut(home, agent string) {
	set := autoHookOptOutSet(home)
	if set[agent] {
		return
	}
	set[agent] = true
	writeAutoHookOptOut(home, set)
}

// clearAutoHookOptOut removes agent from the ledger (called on an explicit
// `hooks install` — re-installing is fresh consent). Idempotent.
func clearAutoHookOptOut(home, agent string) {
	set := autoHookOptOutSet(home)
	if !set[agent] {
		return
	}
	delete(set, agent)
	writeAutoHookOptOut(home, set)
}

func writeAutoHookOptOut(home string, set map[string]bool) {
	agents := make([]string, 0, len(set))
	for a := range set {
		agents = append(agents, a)
	}
	sort.Strings(agents)
	path := autoHookOptOutPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return // best-effort: the ledger is a convenience, never fail a hook op on it
	}
	body := ""
	if len(agents) > 0 {
		body = strings.Join(agents, "\n") + "\n"
	}
	_ = os.WriteFile(path, []byte(body), 0644)
}

// captureHookCommand is the marker by which install/uninstall recognize their
// own entry — never touch anything else in the user's settings.
const captureHookCommand = "auxly capture --stop-hook"

// settingsHasCaptureStopHook reports whether an already-parsed settings.json
// carries our Stop hook entry — the read-only half of install's idempotency
// check, factored out so `hooks status` can reuse it.
func settingsHasCaptureStopHook(settings map[string]any) bool {
	hooks, _ := settings["hooks"].(map[string]any)
	stop, _ := hooks["Stop"].([]any)
	for _, entry := range stop {
		if entryHasCaptureCommand(entry) {
			return true
		}
	}
	return false
}

// captureHookInstalled reads path and reports whether the Stop hook is
// already wired. A missing settings file means "not installed", not an error.
func captureHookInstalled(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	settings := map[string]any{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return false, fmt.Errorf("%s is not valid JSON: %w", path, err)
	}
	return settingsHasCaptureStopHook(settings), nil
}

// installCaptureHook merges a Stop hook into settings.json (read-merge-write,
// never clobber). Returns false when the hook is already present (idempotent).
func installCaptureHook(path string) (bool, error) {
	settings := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		if jerr := json.Unmarshal(data, &settings); jerr != nil {
			return false, fmt.Errorf("%s is not valid JSON — fix it first, refusing to clobber: %w", path, jerr)
		}
	}
	if settingsHasCaptureStopHook(settings) {
		return false, nil
	}

	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	stop, _ := hooks["Stop"].([]any)
	stop = append(stop, map[string]any{
		"hooks": []any{map[string]any{
			"type":    "command",
			"command": captureHookCommand,
			"timeout": 120,
		}},
	})
	hooks["Stop"] = stop
	settings["hooks"] = hooks

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return false, err
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return false, err
	}
	return true, os.WriteFile(path, append(data, '\n'), 0644)
}

// uninstallCaptureHook removes exactly the entries carrying our command,
// preserving every other hook byte-for-byte (via the same parse/merge).
func uninstallCaptureHook(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	settings := map[string]any{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return false, fmt.Errorf("%s is not valid JSON — refusing to touch it: %w", path, err)
	}
	hooks, _ := settings["hooks"].(map[string]any)
	stop, _ := hooks["Stop"].([]any)
	if len(stop) == 0 {
		return false, nil
	}
	kept := make([]any, 0, len(stop))
	removed := false
	for _, entry := range stop {
		if entryHasCaptureCommand(entry) {
			removed = true
			continue
		}
		kept = append(kept, entry)
	}
	if !removed {
		return false, nil
	}
	if len(kept) > 0 {
		hooks["Stop"] = kept
	} else {
		delete(hooks, "Stop")
	}
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return false, err
	}
	return true, os.WriteFile(path, append(out, '\n'), 0644)
}

func entryHasCaptureCommand(entry any) bool {
	m, ok := entry.(map[string]any)
	if !ok {
		return false
	}
	inner, _ := m["hooks"].([]any)
	for _, h := range inner {
		hm, ok := h.(map[string]any)
		if !ok {
			continue
		}
		if cmd, _ := hm["command"].(string); strings.Contains(cmd, captureHookCommand) {
			return true
		}
	}
	return false
}
