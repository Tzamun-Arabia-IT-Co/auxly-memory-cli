package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/spf13/cobra"
)

// shellWrapperAgents are the agents with no native session-end hook — wired
// via a shell function around the CLI instead of a settings file.
var shellWrapperAgents = []string{"gemini", "kimi"}

var hooksPrintWrapperCmd = &cobra.Command{
	Use:          "print-wrapper <agent>",
	Short:        "Print the shell wrapper block for an agent (paste into shells other than zsh/bash)",
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		agent := args[0]
		if !slices.Contains(shellWrapperAgents, agent) {
			return fmt.Errorf("no shell wrapper for %q — supported: %s", agent, strings.Join(shellWrapperAgents, ", "))
		}
		fmt.Print(shellWrapperBlock(agent, runtime.GOOS))
		return nil
	},
}

func init() {
	hooksCmd.AddCommand(hooksPrintWrapperCmd)
}

// shellRCPath resolves the rc file wrapper installs write into. AUXLY_HOOK_RC
// overrides it (tests, or shells other than zsh/bash — pair with
// `hooks print-wrapper` to paste manually). Otherwise $SHELL's basename picks
// it — zsh -> ~/.zshrc, bash -> ~/.bashrc — since that's the shell that will
// actually source the rc file, regardless of OS (a Linux zsh user's wrapper
// belongs in .zshrc, not .bashrc). Unknown/empty $SHELL falls back to the old
// GOOS default: darwin's modern default shell is zsh, everywhere else bash.
// Best-effort: callers print the resolved path so the user always knows what
// got touched.
func shellRCPath() (string, error) {
	if p := os.Getenv("AUXLY_HOOK_RC"); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch filepath.Base(os.Getenv("SHELL")) {
	case "zsh":
		return filepath.Join(home, ".zshrc"), nil
	case "bash":
		return filepath.Join(home, ".bashrc"), nil
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, ".zshrc"), nil
	}
	return filepath.Join(home, ".bashrc"), nil
}

// wrapperMarkers returns the marker comment pair that delimits agent's block,
// so install/uninstall/status only ever touch bytes between them.
func wrapperMarkers(agent string) (start, end string) {
	return fmt.Sprintf("# >>> auxly capture (%s) >>>", agent), fmt.Sprintf("# <<< auxly capture (%s) <<<", agent)
}

// wrapperBlockTemplate is filled in with (start marker, script variant
// label, script invocation line, end marker), in that order, then every
// literal "{{AGENT}}" is replaced with the real agent name.
//
// No `|| command {{AGENT}} "$@"` fallback after the script invocation: script
// propagates the wrapped command's exit status, so a fallback there would
// re-run (double-execute) {{AGENT}} on ANY nonzero exit — including a
// deliberate failure from a destructive/paid operation. If `script` itself
// isn't installed, that's checked upfront instead, once, before anything
// else runs.
const wrapperBlockTemplate = `%s
# auxly: wraps {{AGENT}} to capture the session transcript via ` + "`script`" + ` (%s).
# Honest limitation: {{AGENT}} has no session-end hook, so this captures raw
# terminal output, not a structured transcript — see README "Auto-capture".
{{AGENT}}() {
  if ! command -v script >/dev/null 2>&1; then
    command {{AGENT}} "$@"
    return
  fi
  local __auxly_log __auxly_status
  __auxly_log="$(mktemp -t auxly-{{AGENT}})"
  %s
  __auxly_status=$?
  auxly capture --transcript "$__auxly_log" --provider {{AGENT}} >/dev/null 2>&1
  rm -f "$__auxly_log"
  return $__auxly_status
}
%s
`

// shellWrapperBlock renders the marked wrapper block for agent. The `script`
// command's argv shape differs BSD (macOS) vs util-linux (everything else we
// target), so the block is generated per-GOOS at install time rather than
// detected at runtime inside the shell function itself.
func shellWrapperBlock(agent, goos string) string {
	start, end := wrapperMarkers(agent)
	variant := "macOS/BSD script: script -q <log> <cmd> <args...>"
	scriptLine := `script -q "$__auxly_log" command {{AGENT}} "$@" 2>/dev/null`
	if goos != "darwin" {
		variant = `Linux/util-linux script: script -q -e -c "cmd" <log>`
		// util-linux script's -c takes ONE string, re-parsed through a shell —
		// so a bare "$*" would let `gemini "fix this; also X"` execute
		// `also X`, and backticks/$() in an argument would run arbitrary
		// commands. printf '%q' re-quotes each argument first, so the
		// re-parse reconstructs the original argv instead of re-splitting it.
		// -e makes script return the wrapped command's exit status (util-linux
		// only propagates it with this flag; BSD script does by default).
		scriptLine = `script -q -e -c "command {{AGENT}} $(printf '%q ' "$@")" "$__auxly_log" 2>/dev/null`
	}
	block := fmt.Sprintf(wrapperBlockTemplate, start, variant, scriptLine, end)
	return strings.ReplaceAll(block, "{{AGENT}}", agent)
}

// shellWrapperInstalled reports whether agent's marked block is already in
// the rc file. A missing rc file means "not installed", not an error; one
// that exists but can't be read (e.g. a directory, or permissions) is.
func shellWrapperInstalled(agent string) (bool, error) {
	path, err := shellRCPath()
	if err != nil {
		return false, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("%s: %w", path, err)
	}
	start, _ := wrapperMarkers(agent)
	return strings.Contains(string(data), start), nil
}

// installShellWrapper appends agent's marked block to the rc file. It only
// ever appends — existing bytes are never rewritten, so uninstall can restore
// them byte-identical. Idempotent: a second call is a no-op.
func installShellWrapper(agent string) error {
	path, err := shellRCPath()
	if err != nil {
		return err
	}
	installed, err := shellWrapperInstalled(agent)
	if err != nil {
		return err // refuse: can't confirm it's safe to append
	}
	if installed {
		return nil
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	block := shellWrapperBlock(agent, runtime.GOOS)
	// A blank separator line only when appending to existing content — a
	// freshly created rc file shouldn't start with one.
	if fi, statErr := os.Stat(path); statErr == nil && fi.Size() > 0 {
		block = "\n" + block
	}
	_, err = f.WriteString(block)
	return err
}

// uninstallShellWrapper removes exactly agent's marked block (plus the blank
// separator line install added), leaving every other byte untouched. Returns
// removed=false, err=nil when no block was found.
func uninstallShellWrapper(agent string) (bool, error) {
	path, err := shellRCPath()
	if err != nil {
		return false, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("%s: %w", path, err)
	}
	content := string(data)
	start, end := wrapperMarkers(agent)
	startIdx := strings.Index(content, start)
	if startIdx == -1 {
		return false, nil
	}
	relEnd := strings.Index(content[startIdx:], end)
	if relEnd == -1 {
		return false, fmt.Errorf("%s: found the start marker for %s without a matching end marker — refusing to guess, remove it by hand", path, agent)
	}
	endIdx := startIdx + relEnd + len(end)

	before := strings.TrimSuffix(content[:startIdx], "\n")
	after := strings.TrimPrefix(content[endIdx:], "\n")
	return true, os.WriteFile(path, []byte(before+after), 0644)
}
