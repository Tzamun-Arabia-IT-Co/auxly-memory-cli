package session

import (
	"path/filepath"
	"strings"
)

// InferProvider maps a process-ancestry chain (nearest ancestor first, each
// entry a command path or command line) to an Auxly provider id. It is the
// single source of truth for brand attribution: both the MCP server (for its
// own self-attribution) and the dashboard (for reconciling live servers that
// never registered a session record) call it, so they can never disagree.
//
// Returns "" when no brand keyword matches, letting callers apply their own
// default (the server and dashboard both fall back to "claude").
func InferProvider(ancestors []string) string {
	for _, parentPath := range ancestors {
		baseLower := strings.ToLower(filepath.Base(parentPath))
		pathLower := strings.ToLower(parentPath)

		switch {
		case strings.Contains(pathLower, "cursor.app") || strings.Contains(baseLower, "cursor"):
			return "cursor"
		case strings.Contains(pathLower, "codex.app") || strings.Contains(baseLower, "codex"):
			return "codex"
		case strings.Contains(pathLower, "kimi") || strings.Contains(baseLower, "kimi"):
			return "kimi"
		case strings.Contains(pathLower, "antigravity ide.app") || strings.Contains(pathLower, "antigravityide.app") || strings.Contains(pathLower, "/applications/antigravity ide"):
			return "antigravity-ide"
		case strings.Contains(pathLower, "antigravity.app") || strings.Contains(pathLower, "/applications/antigravity") || strings.Contains(baseLower, "antigravity-agent"):
			return "antigravity-agent"
		case strings.Contains(pathLower, "antigravity-cli") || strings.Contains(baseLower, "antigravity-cli") || strings.Contains(baseLower, "antigravity"):
			return "antigravity-cli"
		case strings.Contains(pathLower, "gemini") || strings.Contains(baseLower, "gemini"):
			return "gemini"
		case strings.Contains(pathLower, "claude.app") || strings.Contains(pathLower, "/applications/claude"):
			return "claude"
		case strings.Contains(baseLower, "claude-code") || strings.Contains(baseLower, "claudecode") || strings.Contains(baseLower, "claude"):
			return "claude-code"
		}
	}
	return ""
}
