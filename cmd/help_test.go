package cmd

import (
	"strings"
	"testing"
)

// TestHelpListsEveryCommand walks the real registered command set and fails if
// any visible command is missing from the custom help output — six commands
// (export, index, organize, trust, usage, statusline) shipped undiscoverable
// once; this makes that a CI failure instead.
func TestHelpListsEveryCommand(t *testing.T) {
	// Machine/stream or plumbing commands intentionally absent from the
	// human-facing help.
	exempt := map[string]bool{
		"mcp-server":  true, // MCP stdio endpoint — launched by agents, not humans
		"connect-mcp": true, // SSH launcher plumbing written into agent configs
		"completion":  true, // cobra built-in
		"help":        true, // cobra built-in
	}

	help := helpText()
	for _, c := range rootCmd.Commands() {
		name := c.Name()
		if exempt[name] || c.Hidden {
			continue
		}
		// Every command entry is rendered with the same cyan wrapper — anchoring
		// on it avoids false positives from prose containing short names.
		if !strings.Contains(help, "\033[38;5;38m"+name+"\033[0m") {
			t.Errorf("command %q is registered but missing from `auxly --help` — add it to helpText()", name)
		}
	}
}
