package detect

import (
	"strings"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/llm"
)

// VerifiedHeadlessProvider reports whether name matches a CLI agent whose
// headless, isolated invocation auxly has actually verified (buildAgentArgs in
// internal/memory carries real flags for it: claude, codex, antigravity/agy,
// gemini, cursor). Anything else must never be picked as an AUTOMATIC
// fallback: the generic branch is a bare `-p` that may open interactive mode
// and hang to the timeout.
func VerifiedHeadlessProvider(name string) bool {
	p := strings.ToLower(name)
	return strings.Contains(p, "claude") ||
		strings.Contains(p, "codex") ||
		strings.Contains(p, "antigravity") ||
		strings.Contains(p, "agy") ||
		strings.Contains(p, "gemini") ||
		strings.Contains(p, "cursor")
}

// ResolveHeadlessLLM picks the LLM backend for a headless run when the user
// made no explicit choice: Direct LLM when one is configured or reachable,
// otherwise the first installed CLI agent with a VERIFIED headless invocation.
// ("", "") means Direct LLM — including the no-agent-no-endpoint case, where
// the caller's model call fails with the endpoint-unreachable error and the
// cmd layer decorates it with setup guidance.
func ResolveHeadlessLLM() (agentName, agentPath string) {
	if llm.ConfiguredOrReachable() {
		return "", ""
	}
	for _, a := range InstalledAgents() {
		isCLI := strings.Contains(a.Name, "CLI") || strings.Contains(a.Name, "Code") || a.Connection == "MCP+Shell" || a.Connection == "Shell"
		if isCLI && a.Command != "" && VerifiedHeadlessProvider(a.Name) {
			return a.Name, a.Command
		}
	}
	return "", ""
}
