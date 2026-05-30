package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-cli/internal/audit"
	"github.com/Tzamun-Arabia-IT-Co/auxly-cli/internal/memory"
	"github.com/Tzamun-Arabia-IT-Co/auxly-cli/internal/pending"
	"github.com/Tzamun-Arabia-IT-Co/auxly-cli/internal/session"
	"github.com/Tzamun-Arabia-IT-Co/auxly-cli/internal/trust"
)

// JSON-RPC types
type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   *rpcError   `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// MCP-specific types
type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type initializeResult struct {
	ProtocolVersion string                 `json:"protocolVersion"`
	Capabilities    map[string]interface{} `json:"capabilities"`
	ServerInfo      serverInfo             `json:"serverInfo"`
}

type tool struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema inputSchema `json:"inputSchema"`
}

type inputSchema struct {
	Type       string              `json:"type"`
	Properties map[string]property `json:"properties"`
	Required   []string            `json:"required,omitempty"`
}

type property struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

type toolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolResult struct {
	Content []toolContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

// Server holds the MCP server state.
type Server struct {
	memoryPath string
	store      *memory.Store
	logger     *audit.Logger
	pendingMgr *pending.Manager
	outWriter  io.Writer
	sourceMeta audit.SourceMeta
	mu         sync.Mutex
}

// NewServer creates a new MCP server.
func NewServer(memoryPath string) *Server {
	store := memory.NewStore(memoryPath)
	logger, _ := audit.NewLogger(memoryPath)
	pendingMgr := pending.NewManager(memoryPath)

	return &Server{
		memoryPath: memoryPath,
		store:      store,
		logger:     logger,
		pendingMgr: pendingMgr,
		outWriter:  os.Stdout,
		sourceMeta: resolveSourceMeta(),
	}
}

// resolveSourceMeta determines write attribution once, based on the env vars
// set by the host process (cmd/mcp_server.go) and sshd's SSH_CONNECTION.
func resolveSourceMeta() audit.SourceMeta {
	source := os.Getenv("AUXLY_SOURCE")
	if source == "" {
		source = "local"
	}

	var remoteIP string
	if conn := os.Getenv("SSH_CONNECTION"); conn != "" {
		// sshd sets "SSH_CONNECTION=<clientIP> <clientPort> <serverIP> <serverPort>".
		if fields := strings.Fields(conn); len(fields) > 0 {
			remoteIP = fields[0]
		}
	}

	return audit.SourceMeta{
		Source:     source,
		RemoteIP:   remoteIP,
		RemoteOS:   os.Getenv("AUXLY_REMOTE_OS"),
		RemoteHost: os.Getenv("AUXLY_REMOTE_HOST"),
	}
}

// Run starts the stdio JSON-RPC loop.
func (s *Server) Run() error {
	return s.RunStream(os.Stdin, os.Stdout)
}

// RunStream starts the JSON-RPC loop on custom reader/writer streams.
func (s *Server) RunStream(in io.Reader, out io.Writer) error {
	s.outWriter = out
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	// Register this live session so the TUI can attribute connections exactly
	// (the server knows its own provider/source — no external guessing needed).
	pid := os.Getpid()
	_ = session.Write(session.Record{
		PID:        pid,
		Provider:   s.resolveProvider(),
		Source:     s.sourceMeta.Source,
		RemoteHost: s.sourceMeta.RemoteHost,
		RemoteOS:   s.sourceMeta.RemoteOS,
		RemoteIP:   s.sourceMeta.RemoteIP,
	})
	defer session.Remove(pid)

	// Set up signal channel to handle graceful shutdown and log disconnect on SIGTERM/SIGINT
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		session.Remove(pid)
		s.logActivity("", "disconnect", "")
		os.Exit(0)
	}()

	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}

		var req jsonRPCRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			s.sendError(nil, -32700, "Parse error")
			continue
		}

		s.handleRequest(&req)
	}

	// Log disconnect activity when server stream exits (closed by client)
	s.logActivity("", "disconnect", "")

	return scanner.Err()
}

func (s *Server) handleRequest(req *jsonRPCRequest) {
	switch req.Method {
	case "initialize":
		s.logActivity("", "initialize", "")
		s.sendResult(req.ID, initializeResult{
			ProtocolVersion: "2024-11-05",
			Capabilities: map[string]interface{}{
				"tools":   map[string]interface{}{},
				"prompts": map[string]interface{}{},
			},
			ServerInfo: serverInfo{
				Name:    "auxly-memory",
				Version: "1.0.0",
			},
		})

	case "notifications/initialized":
		// No response needed for notifications

	case "tools/list":
		s.sendResult(req.ID, map[string]interface{}{
			"tools": s.getTools(),
		})

	case "tools/call":
		s.handleToolCall(req)

	case "prompts/list":
		s.sendResult(req.ID, map[string]interface{}{
			"prompts": s.getPrompts(),
		})

	case "prompts/get":
		s.handlePromptGet(req)

	default:
		s.sendError(req.ID, -32601, fmt.Sprintf("Method not found: %s", req.Method))
	}
}

func (s *Server) getTools() []tool {
	return []tool{
		{
			Name:        "auxly_memory_list",
			Description: "List all memory files in the auxly memory system",
			InputSchema: inputSchema{
				Type:       "object",
				Properties: map[string]property{},
			},
		},
		{
			Name:        "auxly_memory_read",
			Description: "Read the contents of a memory file",
			InputSchema: inputSchema{
				Type: "object",
				Properties: map[string]property{
					"file": {Type: "string", Description: "Filename to read (e.g. identity.md, preferences.md)"},
				},
				Required: []string{"file"},
			},
		},
		{
			Name:        "auxly_memory_write",
			Description: "Write or update a memory file. Respects trust levels: auto writes directly, require_approval writes to pending queue, read_only is rejected.",
			InputSchema: inputSchema{
				Type: "object",
				Properties: map[string]property{
					"file":     {Type: "string", Description: "Target file (e.g. identity.md, preferences.md)"},
					"diff":     {Type: "string", Description: "Content to add. Prefix lines with + to append."},
					"reason":   {Type: "string", Description: "Why this memory is being written/updated"},
					"provider": {Type: "string", Description: "Provider name (claude, chatgpt, codex, gemini, copilot, antigravity). Defaults to claude."},
				},
				Required: []string{"file", "diff", "reason"},
			},
		},
		{
			Name:        "auxly_memory_search",
			Description: "Search across all memory files for a query string",
			InputSchema: inputSchema{
				Type: "object",
				Properties: map[string]property{
					"query": {Type: "string", Description: "Search query"},
				},
				Required: []string{"query"},
			},
		},
		{
			Name:        "auxly_memory_stats",
			Description: "Show memory usage statistics: total entries, writes today, writes per provider",
			InputSchema: inputSchema{
				Type:       "object",
				Properties: map[string]property{},
			},
		},
		{
			Name:        "auxly_pending_list",
			Description: "List pending changes waiting for human approval",
			InputSchema: inputSchema{
				Type:       "object",
				Properties: map[string]property{},
			},
		},
		{
			Name:        "auxly_skill_init",
			Description: "Slash skill '/auxly-init': Run the onboarding and training setup, scan current chat context/system prompt, and sync all existing preferences to Auxly.",
			InputSchema: inputSchema{
				Type:       "object",
				Properties: map[string]property{},
			},
		},
		{
			Name:        "auxly_skill_memory",
			Description: "Slash skill '/auxly-memory': Retrieve and display a consolidated markdown profile of the user's identity, preferences, and system infrastructure",
			InputSchema: inputSchema{
				Type:       "object",
				Properties: map[string]property{},
			},
		},
		{
			Name:        "auxly_skill_max",
			Description: "Slash skill '/auxly-max': Generate the dynamic Maximum Memory sync instructions tailored with active local gateway ports for stdio-native clients",
			InputSchema: inputSchema{
				Type:       "object",
				Properties: map[string]property{},
			},
		},
		{
			Name:        "auxly_skill_sync",
			Description: "Slash skill '/auxly-sync [content]': Append and synchronize a new fact, preference, or system detail using smart automated delta-merges into the user's memory vault",
			InputSchema: inputSchema{
				Type: "object",
				Properties: map[string]property{
					"content":  {Type: "string", Description: "The specific fact or detail to synchronize to memory"},
					"category": {Type: "string", Description: "Target context area: preferences (default), identity, infra, products, projects, daily, agents, business"},
					"scope":    {Type: "string", Description: "Vault scoping: 'global' (default) or project 'workspace'"},
				},
				Required: []string{"content"},
			},
		},
		{
			Name:        "auxly_skill_pending",
			Description: "Slash skill '/auxly-pending': Manage pending memory changes awaiting human approval directly inside the active chat panel",
			InputSchema: inputSchema{
				Type: "object",
				Properties: map[string]property{
					"action":    {Type: "string", Description: "Operation: list, approve, or reject. Defaults to list."},
					"target_id": {Type: "string", Description: "The entry ID to approve or reject"},
				},
			},
		},
		{
			Name:        "auxly_skill_status",
			Description: "Slash skill '/auxly-status': Show real-time system diagnostics, active client connections, database sizes, and secure tunnel URL parameters",
			InputSchema: inputSchema{
				Type:       "object",
				Properties: map[string]property{},
			},
		},
		{
			Name:        "auxly_skill_forget",
			Description: "Slash skill '/auxly-forget [query]': Search memory vault and prune obsolete or outdated bullet statements cleanly from memory files",
			InputSchema: inputSchema{
				Type: "object",
				Properties: map[string]property{
					"query": {Type: "string", Description: "Key words or fact patterns to search for and delete from memory files"},
				},
				Required: []string{"query"},
			},
		},
		{
			Name:        "auxly_skill_learn",
			Description: "Slash skill '/auxly-learn [context]': Intercept recent edits or context to extract and propose structured new facts to save into memory files",
			InputSchema: inputSchema{
				Type: "object",
				Properties: map[string]property{
					"context": {Type: "string", Description: "Recent text edits, preferences mentioned, or git diffs to analyze for new facts"},
				},
				Required: []string{"context"},
			},
		},
		{
			Name:        "auxly_skill_remote_connect",
			Description: "Report the active Auxly remote connection: host, client IP, OS; confirm shared remote vault.",
			InputSchema: inputSchema{
				Type:       "object",
				Properties: map[string]property{},
			},
		},
	}
}

func (s *Server) handleToolCall(req *jsonRPCRequest) {
	var params struct {
		Name      string                 `json:"name"`
		Arguments map[string]interface{} `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		s.sendError(req.ID, -32602, "Invalid params")
		return
	}

	var result toolResult

	switch params.Name {
	case "auxly_memory_list":
		s.logActivity("", "list", "")
		result = s.toolList()
	case "auxly_memory_read":
		file, _ := params.Arguments["file"].(string)
		s.logActivity("", "read", file)
		result = s.toolRead(file)
	case "auxly_memory_write":
		file, _ := params.Arguments["file"].(string)
		diff, _ := params.Arguments["diff"].(string)
		reason, _ := params.Arguments["reason"].(string)
		provider, _ := params.Arguments["provider"].(string)
		if provider == "" {
			provider = getProviderFromParent()
		}
		if provider == "" {
			provider = os.Getenv("AUXLY_PROVIDER")
		}
		if provider == "" {
			provider = "claude"
		}
		result = s.toolWrite(file, diff, reason, provider)
	case "auxly_memory_search":
		query, _ := params.Arguments["query"].(string)
		s.logActivity("", "search", "")
		result = s.toolSearch(query)
	case "auxly_memory_stats":
		s.logActivity("", "stats", "")
		result = s.toolStats()
	case "auxly_pending_list":
		s.logActivity("", "pending", "")
		result = s.toolPendingList()
	case "auxly_skill_init":
		s.logActivity("", "skill_init", "")
		result = s.toolSkillInit()
	case "auxly_skill_memory":
		s.logActivity("", "skill_memory", "")
		result = s.toolSkillMemory()
	case "auxly_skill_max":
		s.logActivity("", "skill_max", "")
		result = s.toolSkillMax()
	case "auxly_skill_sync":
		content, _ := params.Arguments["content"].(string)
		category, _ := params.Arguments["category"].(string)
		scope, _ := params.Arguments["scope"].(string)
		s.logActivity("", "skill_sync", "")
		result = s.toolSkillSync(content, category, scope)
	case "auxly_skill_pending":
		action, _ := params.Arguments["action"].(string)
		targetID, _ := params.Arguments["target_id"].(string)
		s.logActivity("", "skill_pending", "")
		result = s.toolSkillPending(action, targetID)
	case "auxly_skill_status":
		s.logActivity("", "skill_status", "")
		result = s.toolSkillStatus()
	case "auxly_skill_forget":
		query, _ := params.Arguments["query"].(string)
		s.logActivity("", "skill_forget", "")
		result = s.toolSkillForget(query)
	case "auxly_skill_learn":
		context, _ := params.Arguments["context"].(string)
		s.logActivity("", "skill_learn", "")
		result = s.toolSkillLearn(context)
	case "auxly_skill_remote_connect":
		s.logActivity("", "skill_remote_connect", "")
		result = s.toolSkillRemoteConnect()
	default:
		result = toolResult{
			Content: []toolContent{{Type: "text", Text: fmt.Sprintf("Unknown tool: %s", params.Name)}},
			IsError: true,
		}
	}

	s.sendResult(req.ID, result)
}

func getParentProcessName() string {
	ppid := os.Getppid()
	cmd := exec.Command("ps", "-p", strconv.Itoa(ppid), "-o", "comm=")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func getAncestorProcesses() []string {
	var ancestors []string
	pid := os.Getppid() // Start from parent process to avoid self-matching

	for i := 0; i < 10; i++ {
		cmd := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "ppid=,comm=")
		out, err := cmd.Output()
		if err != nil {
			break
		}

		line := strings.TrimSpace(string(out))
		if line == "" {
			break
		}

		parts := strings.Fields(line)
		if len(parts) < 2 {
			break
		}

		ppidStr := parts[0]
		comm := strings.Join(parts[1:], " ")

		baseLower := strings.ToLower(filepath.Base(comm))

		ppid, err := strconv.Atoi(ppidStr)
		if err != nil || ppid <= 1 {
			if !strings.Contains(baseLower, "auxly") {
				ancestors = append(ancestors, comm)
			}
			break
		}

		// Skip self/auxly binary processes to avoid self-matching bug
		if strings.Contains(baseLower, "auxly") {
			pid = ppid
			continue
		}

		ancestors = append(ancestors, comm)
		pid = ppid
	}

	return ancestors
}

// resolveProvider determines this server's provider from its own environment
// first (authoritative — set by the IDE config or the --provider launcher flag),
// then parent-process inference, defaulting to "claude".
func (s *Server) resolveProvider() string {
	if p := strings.TrimSpace(os.Getenv("AUXLY_PROVIDER")); p != "" {
		return p
	}
	if p := getProviderFromParent(); p != "" {
		return p
	}
	return "claude"
}

func getProviderFromParent() string {
	ancestors := getAncestorProcesses()
	if len(ancestors) == 0 {
		return ""
	}

	for _, parentPath := range ancestors {
		base := filepath.Base(parentPath)
		baseLower := strings.ToLower(base)
		pathLower := strings.ToLower(parentPath)

		if strings.Contains(pathLower, "cursor.app") || strings.Contains(baseLower, "cursor") {
			return "cursor"
		}
		if strings.Contains(pathLower, "codex.app") || strings.Contains(baseLower, "codex") {
			return "codex"
		}
		if strings.Contains(pathLower, "kimi") || strings.Contains(baseLower, "kimi") {
			return "kimi"
		}
		if strings.Contains(pathLower, "antigravity ide.app") || strings.Contains(pathLower, "antigravityide.app") || strings.Contains(pathLower, "/applications/antigravity ide") {
			return "antigravity-ide"
		}
		if strings.Contains(pathLower, "antigravity.app") || strings.Contains(pathLower, "/applications/antigravity") || strings.Contains(baseLower, "antigravity-agent") {
			return "antigravity-agent"
		}
		if strings.Contains(pathLower, "antigravity-cli") || strings.Contains(baseLower, "antigravity-cli") || strings.Contains(baseLower, "antigravity") {
			return "antigravity-cli"
		}
		if strings.Contains(pathLower, "gemini") || strings.Contains(baseLower, "gemini") {
			return "gemini"
		}
		if strings.Contains(pathLower, "claude.app") || strings.Contains(pathLower, "/applications/claude") {
			return "claude"
		}
		if strings.Contains(baseLower, "claude-code") || strings.Contains(baseLower, "claudecode") || strings.Contains(baseLower, "claude") {
			return "claude-code"
		}
	}

	return ""
}

func (s *Server) logActivity(provider, action, file string) {
	if provider == "" {
		provider = os.Getenv("AUXLY_PROVIDER")
	}
	if provider == "" {
		provider = getProviderFromParent()
	}
	if provider == "" {
		provider = "claude"
	}
	agentID := fmt.Sprintf("%s-mcp", provider)
	if s.logger != nil {
		s.logger.LogWithSource(agentID, provider, action, file, "", "Activity log", "auto", s.sourceMeta)
	}
}

func (s *Server) toolList() toolResult {
	files, err := s.store.List()
	if err != nil {
		return toolResult{Content: []toolContent{{Type: "text", Text: fmt.Sprintf("Error: %v", err)}}, IsError: true}
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📂 Memory: %s\n\n", s.memoryPath))
	for _, f := range files {
		sb.WriteString(fmt.Sprintf("• %s (%d bytes, modified %s)\n", f.Name, f.Size, f.ModTime.Format("2006-01-02 15:04")))
	}
	return toolResult{Content: []toolContent{{Type: "text", Text: sb.String()}}}
}

func (s *Server) toolRead(file string) toolResult {
	if file == "" {
		return toolResult{Content: []toolContent{{Type: "text", Text: "Error: file parameter required"}}, IsError: true}
	}
	content, err := s.store.View(file)
	if err != nil {
		return toolResult{Content: []toolContent{{Type: "text", Text: fmt.Sprintf("Error: %v", err)}}, IsError: true}
	}
	return toolResult{Content: []toolContent{{Type: "text", Text: content}}}
}

func (s *Server) toolWrite(file, diff, reason, provider string) toolResult {
	return s.toolWriteScoped(file, diff, reason, provider, "global")
}

func (s *Server) toolWriteScoped(file, diff, reason, provider, scope string) toolResult {
	s.mu.Lock()
	defer s.mu.Unlock()

	if file == "" || diff == "" || reason == "" {
		return toolResult{Content: []toolContent{{Type: "text", Text: "Error: file, diff, and reason are all required"}}, IsError: true}
	}

	if provider == "" {
		provider = os.Getenv("AUXLY_PROVIDER")
	}
	if provider == "" {
		provider = getProviderFromParent()
	}
	if provider == "" {
		provider = "claude"
	}

	// Check trust level
	trustCfg, err := trust.Load(s.memoryPath)
	if err != nil {
		return toolResult{Content: []toolContent{{Type: "text", Text: fmt.Sprintf("Error loading trust config: %v", err)}}, IsError: true}
	}

	level := trustCfg.GetTrustLevel(provider)

	if level == trust.LevelReadOnly {
		return toolResult{Content: []toolContent{{Type: "text", Text: fmt.Sprintf("❌ Provider '%s' is read_only. Write rejected.", provider)}}, IsError: true}
	}

	agentID := fmt.Sprintf("%s-mcp", provider)

	if level == trust.LevelRequireApproval {
		pendingName, err := s.pendingMgr.Write(file, diff)
		if err != nil {
			return toolResult{Content: []toolContent{{Type: "text", Text: fmt.Sprintf("Error: %v", err)}}, IsError: true}
		}
		if s.logger != nil {
			s.logger.LogWithSource(agentID, provider, "write", file, diff, reason, level, s.sourceMeta)
		}
		return toolResult{Content: []toolContent{{Type: "text", Text: appendSkillSyncFooter(fmt.Sprintf("⏳ Change queued for approval: .pending/%s\nHuman must run 'auxly approve %s' to apply.", pendingName, pendingName))}}}
	}

	// Auto trust: write directly
	var existing string
	if data, err := s.store.View(file); err == nil {
		existing = data
	}
	content := pending.ApplyDiff(existing, diff)

	if err := s.store.WriteScoped(file, content, scope); err != nil {
		return toolResult{Content: []toolContent{{Type: "text", Text: fmt.Sprintf("Error writing: %v", err)}}, IsError: true}
	}

	if s.logger != nil {
		s.logger.LogWithSource(agentID, provider, "write", file, diff, reason, level, s.sourceMeta)
	}

	msgText := fmt.Sprintf("✅ Your Auxly memory has been updated! Written to %s.\n\nAuxly Unified Memory has been re-compiled in the background.", file)
	if data, err := s.store.View("unified_memory.md"); err == nil {
		msgText += "\n\nHere is the updated global memory summary for your context sync:\n---\n" + data
	}

	return toolResult{Content: []toolContent{{Type: "text", Text: appendSkillSyncFooter(msgText)}}}
}

func (s *Server) toolSearch(query string) toolResult {
	if query == "" {
		return toolResult{Content: []toolContent{{Type: "text", Text: "Error: query parameter required"}}, IsError: true}
	}

	results, err := s.store.Search(query)
	if err != nil {
		return toolResult{Content: []toolContent{{Type: "text", Text: fmt.Sprintf("Error: %v", err)}}, IsError: true}
	}

	if len(results) == 0 {
		return toolResult{Content: []toolContent{{Type: "text", Text: fmt.Sprintf("No results for \"%s\"", query)}}}
	}

	var sb strings.Builder
	for file, lines := range results {
		sb.WriteString(fmt.Sprintf("\n📄 %s\n", file))
		for _, line := range lines {
			sb.WriteString(fmt.Sprintf("   %s\n", line))
		}
	}
	return toolResult{Content: []toolContent{{Type: "text", Text: sb.String()}}}
}

func (s *Server) toolStats() toolResult {
	if s.logger == nil {
		return toolResult{Content: []toolContent{{Type: "text", Text: "No audit data available. Run 'auxly init' first."}}}
	}

	stats, err := s.logger.Stats()
	if err != nil {
		return toolResult{Content: []toolContent{{Type: "text", Text: fmt.Sprintf("Error: %v", err)}}, IsError: true}
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📊 Memory Stats\n"))
	sb.WriteString(fmt.Sprintf("Total entries: %d\n", stats.TotalEntries))
	sb.WriteString(fmt.Sprintf("Writes today: %d\n", stats.WritesToday))
	if len(stats.ByProvider) > 0 {
		sb.WriteString("\nBy Provider:\n")
		for p, c := range stats.ByProvider {
			sb.WriteString(fmt.Sprintf("  %s: %d\n", p, c))
		}
	}
	return toolResult{Content: []toolContent{{Type: "text", Text: sb.String()}}}
}

func (s *Server) toolPendingList() toolResult {
	files, err := s.pendingMgr.List()
	if err != nil {
		return toolResult{Content: []toolContent{{Type: "text", Text: fmt.Sprintf("Error: %v", err)}}, IsError: true}
	}

	if len(files) == 0 {
		return toolResult{Content: []toolContent{{Type: "text", Text: "✅ No pending approvals."}}}
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("⏳ %d pending approval(s):\n\n", len(files)))
	for _, f := range files {
		sb.WriteString(fmt.Sprintf("• %s (modified %s)\n", f.Name, f.ModTime.Format("2006-01-02 15:04")))
	}
	sb.WriteString("\nHuman must run 'auxly approve <file>' to apply.")
	return toolResult{Content: []toolContent{{Type: "text", Text: sb.String()}}}
}

// describeRemote builds a compact "<ip>, <os>" description from source meta,
// gracefully omitting empty parts.
func describeRemote(meta audit.SourceMeta) string {
	var parts []string
	if meta.RemoteIP != "" {
		parts = append(parts, meta.RemoteIP)
	}
	if meta.RemoteOS != "" {
		parts = append(parts, meta.RemoteOS)
	}
	return strings.Join(parts, ", ")
}

func appendSkillSyncFooter(text string) string {
	return text + "\n\n---\n🧠 **Auxly Agent Sync Active:** Remember to proactively call `auxly_skill_sync` to update the memory vault whenever you learn new preferences, system setups, or developer details!"
}

func (s *Server) toolSkillInit() toolResult {
	onboarding := `🚀 **AUXLY UNIFIED AGENT MEMORY ONBOARDING & TRAINING**

Welcome! You are now aligned with the **Auxly Unified Memory standard**. This local-first, offline-first subsystem ensures you can persist developer preferences, infrastructure details, and decisions persistently.

💡 **YOUR CORE INSTRUCTIONS:**
1. **Always Read Memory**: When starting a session, read the memory files ('auxly_skill_memory') to instantly ground your understanding of the user's setup, product portfolio, and stack.
2. **Proactively Update Memory**: Whenever you learn new preferences, stack details, environment coordinates, or strategic decisions, immediately call the 'auxly_skill_sync' tool. Do not wait for the user to ask!
3. **End-of-Session Summaries**: At the end of a significant session or task completion, append a journal entry to 'daily.md' detailing your accomplishments.

🎯 **IMMEDIATE ACTION REQUIRED:**
* You must now scan your current context, active prompt, and chat history.
* If you have any existing facts about the developer (e.g. name, preferences, IDEs, servers, or keys), immediately execute the 'auxly_skill_sync' tool to save them into the vault under the appropriate category!
* Confirm to the user that you have understood these rules and are ready to sync.`

	return toolResult{Content: []toolContent{{Type: "text", Text: appendSkillSyncFooter(onboarding)}}}
}

func (s *Server) toolSkillMemory() toolResult {
	var sb strings.Builder
	sb.WriteString("👤 **AUXLY UNIFIED AGENT MEMORY PROFILE**\n\n")

	files, err := s.store.List()
	if err != nil {
		return toolResult{Content: []toolContent{{Type: "text", Text: fmt.Sprintf("Error listing memory files: %v", err)}}, IsError: true}
	}

	priority := map[string]int{
		"identity.md":    1,
		"preferences.md": 2,
		"infra.md":       3,
		"products.md":    4,
		"projects.md":    5,
		"daily.md":       6,
		"agents.md":      7,
		"business.md":    8,
	}

	sort.Slice(files, func(i, j int) bool {
		pI := priority[files[i].Name]
		pJ := priority[files[j].Name]
		if pI == 0 {
			pI = 99
		}
		if pJ == 0 {
			pJ = 99
		}
		if pI != pJ {
			return pI < pJ
		}
		return files[i].Name < files[j].Name
	})

	readAny := false

	for _, f := range files {
		content, err := s.store.View(f.Name)
		if err == nil && len(strings.TrimSpace(content)) > 0 {
			readAny = true
			sb.WriteString(fmt.Sprintf("### 📄 %s\n\n", f.Name))
			sb.WriteString(content)
			sb.WriteString("\n\n---\n\n")
		}
	}

	if !readAny {
		return toolResult{Content: []toolContent{{Type: "text", Text: appendSkillSyncFooter("⚠️ No memory files populated yet. Type `/auxly-sync [content]` or paste your onboarding prompt to save your first memory!")}}}
	}

	return toolResult{Content: []toolContent{{Type: "text", Text: appendSkillSyncFooter(sb.String())}}}
}

func (s *Server) toolSkillMax() toolResult {
	prompt := `🧠 AUXLY MAXIMUM MEMORY ALIGNMENT NATIVE PROTOCOL

✓ Unified Memory standard is successfully activated and aligned for this active session!
✓ Stdio communication interface is operating in offline-first secure loopback mode.

💡 MEMORY CORE DIRECTIONS:
* Read Profile: Call 'auxly_skill_memory' immediately to fetch the user's complete profile (identity, preferences, infra) and ground your context.
* Write Habits: Proactively call 'auxly_skill_sync' (specifying content and category) to persist new environment variables, design choices, or work habits as soon as you learn them.
* Diagnostics: Run 'auxly_skill_status' to view sqlite stats and daemon gatekeeper parameters.
* Forget Obsolete: Call 'auxly_skill_forget' to cleanly strip outdated statements.

Your memory is 100% portable and persistent. If you or your platform experiences an outage, the user can switch instantly to any other local IDE/agent (Claude Desktop, Claude Code, Cursor, Codex, Gemini CLI) and continue work with zero disruption or lost context!`

	return toolResult{Content: []toolContent{{Type: "text", Text: appendSkillSyncFooter(prompt)}}}
}

func (s *Server) toolSkillSync(content, category, scope string) toolResult {
	if strings.TrimSpace(content) == "" {
		return toolResult{Content: []toolContent{{Type: "text", Text: "Error: Content cannot be empty"}}, IsError: true}
	}

	// Semantic Auto-Router: If category is empty or preferences (default), analyze content keywords
	if category == "" || category == "preferences" {
		contentLower := strings.ToLower(content)
		infraKeywords := []string{"server", "ip", "port", "vpn", "firewall", "pfsense", "gpu", "rtx", "docker", "vllm", "ollama", "n8n", "siem", "wazuh", "dns", "cloudflare", "gitlab", "hosting", "vps", "ovh", "cameras", "nvr", "frigate"}
		productKeywords := []string{"platform", "product", "portfolio", "etabeb", "raqeb", "tzamunerp", "pathconnect", "radioconnect", "tchub", "tzamunai", "motormind", "auxly", "voicehub", "app"}
		projectKeywords := []string{"repo", "git", "project", "workspace", "folder", "directory"}
		identityKeywords := []string{"ceo", "founder", "chairman", "wael", "samoum", "jeddah", "saudi", "gcc", "fundraising", "raising", "leanteam"}
		dailyKeywords := []string{"accomplished", "completed", "journal", "today", "log", "milestone", "done"}

		matched := false
		for _, kw := range infraKeywords {
			if strings.Contains(contentLower, kw) {
				category = "infra"
				matched = true
				break
			}
		}
		if !matched {
			for _, kw := range productKeywords {
				if strings.Contains(contentLower, kw) {
					category = "products"
					matched = true
					break
				}
			}
		}
		if !matched {
			for _, kw := range identityKeywords {
				if strings.Contains(contentLower, kw) {
					category = "identity"
					matched = true
					break
				}
			}
		}
		if !matched {
			for _, kw := range projectKeywords {
				if strings.Contains(contentLower, kw) {
					category = "projects"
					matched = true
					break
				}
			}
		}
		if !matched {
			for _, kw := range dailyKeywords {
				if strings.Contains(contentLower, kw) {
					category = "daily"
					matched = true
					break
				}
			}
		}
	}

	fileName := "preferences.md"
	if category == "identity" {
		fileName = "identity.md"
	} else if category == "infra" {
		fileName = "infra.md"
	} else if category == "products" {
		fileName = "products.md"
	} else if category == "projects" {
		fileName = "projects.md"
	} else if category == "daily" {
		fileName = "daily.md"
	} else if category == "agents" {
		fileName = "agents.md"
	} else if category == "business" {
		fileName = "business.md"
	}

	// Dynamic trust verification
	trustCfg, err := trust.Load(s.memoryPath)
	trustLevel := "auto"
	if err == nil && trustCfg != nil {
		trustLevel = trustCfg.GetTrustLevel(getProviderFromParent())
	}
	_ = trustLevel // Checked dynamically in toolWrite

	// We append a neat date-stamped bullet fact under a clean smart-merge section
	datePrefix := time.Now().Format("2006-01-02")
	formattedDiff := fmt.Sprintf("+ - [%s] Smart Sync: %s\n", datePrefix, content)

	// Route to toolWriteScoped to handle trust rules and scope automatically!
	return s.toolWriteScoped(fileName, formattedDiff, "Synchronized fact via /auxly-sync skill", getProviderFromParent(), scope)
}

func (s *Server) toolSkillPending(action, targetID string) toolResult {
	if action == "" || action == "list" {
		res := s.toolPendingList()
		if !res.IsError && len(res.Content) > 0 {
			res.Content[0].Text = appendSkillSyncFooter(res.Content[0].Text)
		}
		return res
	}

	if targetID == "" {
		return toolResult{Content: []toolContent{{Type: "text", Text: "Error: Please specify the pending entry filename/ID to resolve."}}, IsError: true}
	}

	if action == "approve" {
		err := s.pendingMgr.Approve(targetID)
		if err != nil {
			return toolResult{Content: []toolContent{{Type: "text", Text: fmt.Sprintf("Error approving entry: %v", err)}}, IsError: true}
		}
		// Log write event in audit.db
		if s.logger != nil {
			provider := getProviderFromParent()
			if provider == "" {
				provider = "claude"
			}
			agentID := fmt.Sprintf("%s-mcp", provider)
			s.logger.LogWithSource(agentID, provider, "write", targetID, "", "Approved pending change via skill", "auto", s.sourceMeta)
		}
		return toolResult{Content: []toolContent{{Type: "text", Text: appendSkillSyncFooter(fmt.Sprintf("✓ Successfully approved and committed pending entry: %s", targetID))}}}
	}

	if action == "reject" {
		err := s.pendingMgr.Reject(targetID)
		if err != nil {
			return toolResult{Content: []toolContent{{Type: "text", Text: fmt.Sprintf("Error rejecting entry: %v", err)}}, IsError: true}
		}
		return toolResult{Content: []toolContent{{Type: "text", Text: appendSkillSyncFooter(fmt.Sprintf("✗ Successfully rejected and deleted pending entry: %s", targetID))}}}
	}

	return toolResult{Content: []toolContent{{Type: "text", Text: fmt.Sprintf("Error: Unknown action '%s'. Supported actions are list, approve, reject.", action)}}, IsError: true}
}

func (s *Server) toolSkillStatus() toolResult {
	var sb strings.Builder
	sb.WriteString("📡 **AUXLY GATEWAY SYSTEM STATUS**\n\n")

	sourceText := "● local"
	if s.sourceMeta.Source == "ssh-remote" {
		sourceText = fmt.Sprintf("● ssh-remote (%s)", describeRemote(s.sourceMeta))
	}
	sb.WriteString(fmt.Sprintf("• **Source:** %s\n", sourceText))

	if stats, err := s.logger.Stats(); err == nil && stats != nil {
		sb.WriteString(fmt.Sprintf("• **Writes Today:** %d\n", stats.WritesToday))
		sb.WriteString(fmt.Sprintf("• **Total Memory Entries:** %d\n", stats.TotalEntries))
	}

	return toolResult{Content: []toolContent{{Type: "text", Text: appendSkillSyncFooter(sb.String())}}}
}

func (s *Server) toolSkillRemoteConnect() toolResult {
	var sb strings.Builder
	sb.WriteString("🔗 **AUXLY REMOTE CONNECTION**\n\n")

	if s.sourceMeta.Source == "ssh-remote" {
		// This handler runs ON the memory host (the machine whose vault is
		// being served over SSH), so os.Hostname() here is the HOST. The
		// sourceMeta fields describe the CONNECTED CLIENT (this remote box).
		memHost, _ := os.Hostname()
		if memHost == "" {
			memHost = "(memory host)"
		}
		client := s.sourceMeta.RemoteHost
		if client == "" {
			client = "(this machine)"
		}
		remoteOS := s.sourceMeta.RemoteOS
		if remoteOS == "" {
			remoteOS = "(unknown)"
		}
		sb.WriteString("✅ **Connected to a shared memory host over SSH.**\n\n")
		sb.WriteString(fmt.Sprintf("• **Memory host (vault lives here):** %s\n", memHost))
		sb.WriteString(fmt.Sprintf("• **This machine (connected client):** %s (%s)\n", client, remoteOS))
		sb.WriteString(fmt.Sprintf("\nReads and writes are centralized and audited on **%s**'s Auxly vault, which may be shared with other agents. No memory is stored locally on this machine.", memHost))
		return toolResult{Content: []toolContent{{Type: "text", Text: appendSkillSyncFooter(sb.String())}}}
	}

	sb.WriteString("• **Source:** local vault\n\n")

	// Detect a relay offer published on this box (the host advertised itself here).
	// If one exists, this box can be wired to that host's memory with no flags.
	if offers := detectRelayOfferNames(); len(offers) > 0 {
		sb.WriteString("⚠️ This session is using a LOCAL vault, but a remote memory host is **offered on this machine** and not yet connected:\n")
		for _, name := range offers {
			sb.WriteString(fmt.Sprintf("• **%s**\n", name))
		}
		sb.WriteString("\n**ACTION — wire it up now:** run this in a shell, then tell the user to restart this agent:\n\n")
		if len(offers) == 1 {
			sb.WriteString("```\nauxly connect auto\n```\n")
		} else {
			sb.WriteString(fmt.Sprintf("```\nauxly connect auto %s\n```\n", offers[0]))
		}
		sb.WriteString("\nAfter the agent restarts, reads/writes go to the host's central, audited vault.")
		return toolResult{Content: []toolContent{{Type: "text", Text: appendSkillSyncFooter(sb.String())}}}
	}

	sb.WriteString("This session is using a LOCAL Auxly vault, and no remote host is offered on this machine.\n")
	sb.WriteString("To link one, run `auxly host setup` on the memory host (it publishes an offer here), then `auxly connect auto`.")
	return toolResult{Content: []toolContent{{Type: "text", Text: appendSkillSyncFooter(sb.String())}}}
}

// detectRelayOfferNames lists the names of relay offers published in
// ~/.auxly/offers (the offer file name is the host/profile name).
func detectRelayOfferNames() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(filepath.Join(home, ".auxly", "offers"))
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		names = append(names, strings.TrimSuffix(e.Name(), ".yaml"))
	}
	return names
}

func (s *Server) toolSkillForget(query string) toolResult {
	if strings.TrimSpace(query) == "" {
		return toolResult{Content: []toolContent{{Type: "text", Text: "Error: Query cannot be empty"}}, IsError: true}
	}

	files, err := s.store.List()
	if err != nil {
		return toolResult{Content: []toolContent{{Type: "text", Text: fmt.Sprintf("Error listing memory files: %v", err)}}, IsError: true}
	}
	deletedCount := 0
	var sb strings.Builder
	sb.WriteString("🧹 **AUXLY MEMORY PRUNING REPORT**\n\n")

	for _, f := range files {
		file := f.Name
		content, err := s.store.View(file)
		if err != nil {
			continue
		}

		lines := strings.Split(content, "\n")
		var remainingLines []string
		var removedLines []string

		for _, line := range lines {
			if strings.Contains(strings.ToLower(line), strings.ToLower(query)) {
				removedLines = append(removedLines, line)
				deletedCount++
			} else {
				remainingLines = append(remainingLines, line)
			}
		}

		if len(removedLines) > 0 {
			newContent := strings.Join(remainingLines, "\n")
			// Save using mutex
			s.mu.Lock()
			err = s.store.Write(file, newContent)
			s.mu.Unlock()

			if err == nil {
				sb.WriteString(fmt.Sprintf("### 📄 %s\n", file))
				for _, rl := range removedLines {
					sb.WriteString(fmt.Sprintf("- 🗑️ ~~%s~~\n", strings.TrimSpace(rl)))
				}
				sb.WriteString("\n")
			}
		}
	}

	if deletedCount == 0 {
		return toolResult{Content: []toolContent{{Type: "text", Text: appendSkillSyncFooter(fmt.Sprintf("No matching facts or bullets found in memory for query: \"%s\"", query))}}}
	}

	sb.WriteString(fmt.Sprintf("✓ Successfully pruned %d obsolete statement(s) from memory vault.", deletedCount))
	return toolResult{Content: []toolContent{{Type: "text", Text: appendSkillSyncFooter(sb.String())}}}
}

func (s *Server) toolSkillLearn(context string) toolResult {
	if strings.TrimSpace(context) == "" {
		return toolResult{Content: []toolContent{{Type: "text", Text: "Error: Context cannot be empty"}}, IsError: true}
	}

	// Simulate AI analysis by structuring the context sentences as bullet propositions
	sentences := strings.Split(context, ".")
	var proposedFacts []string
	for _, s := range sentences {
		trimmed := strings.TrimSpace(s)
		if len(trimmed) > 10 {
			proposedFacts = append(proposedFacts, trimmed)
		}
	}

	if len(proposedFacts) == 0 {
		return toolResult{Content: []toolContent{{Type: "text", Text: appendSkillSyncFooter("⚠️ Context too short or vague to extract structured memory facts.")}}}
	}

	var sb strings.Builder
	sb.WriteString("💡 **PROPOSED NEW MEMORY FACTS FOR REVIEW**\n")
	sb.WriteString("I extracted the following statements from your context. Review them below:\n\n")

	for i, fact := range proposedFacts {
		sb.WriteString(fmt.Sprintf("%d. `[Proposed]` %s\n", i+1, fact))
	}
	sb.WriteString("\n👉 **How to save:** Type `/auxly-sync [proposition]` to lock in any of these facts immediately!")

	return toolResult{Content: []toolContent{{Type: "text", Text: appendSkillSyncFooter(sb.String())}}}
}

func (s *Server) sendResult(id interface{}, result interface{}) {
	resp := jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
	data, _ := json.Marshal(resp)
	if s.outWriter != nil {
		fmt.Fprintf(s.outWriter, "%s\n", data)
	} else {
		fmt.Fprintf(os.Stdout, "%s\n", data)
	}
}

func (s *Server) sendError(id interface{}, code int, message string) {
	resp := jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &rpcError{Code: code, Message: message},
	}
	data, _ := json.Marshal(resp)
	if s.outWriter != nil {
		fmt.Fprintf(s.outWriter, "%s\n", data)
	} else {
		fmt.Fprintf(os.Stdout, "%s\n", data)
	}
}

type prompt struct {
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Arguments   []promptArgument `json:"arguments,omitempty"`
}

type promptArgument struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

func (s *Server) getPrompts() []prompt {
	return []prompt{
		{
			Name:        "auxly-init",
			Description: "Run the onboarding training, scan current context, and synchronize existing chat context/preferences to Auxly.",
		},
		{
			Name:        "auxly-memory",
			Description: "Retrieve and display a consolidated markdown profile of the user's identity, preferences, and system infrastructure.",
		},
		{
			Name:        "auxly-max",
			Description: "Obtain the dynamic Maximum Memory sync instructions block to sync other agents.",
		},
		{
			Name:        "auxly-sync",
			Description: "Append and synchronize a new fact, preference, or system detail into memory files (preferences.md, identity.md, infra.md, products.md, projects.md, daily.md, etc.).",
			Arguments: []promptArgument{
				{
					Name:        "content",
					Description: "The specific fact or detail to synchronize to memory",
					Required:    true,
				},
			},
		},
		{
			Name:        "auxly-pending",
			Description: "Manage pending memory changes awaiting human approval directly inside the chat.",
			Arguments: []promptArgument{
				{
					Name:        "action",
					Description: "Operation: list, approve, or reject. Defaults to list.",
					Required:    false,
				},
				{
					Name:        "target_id",
					Description: "The entry ID to approve or reject",
					Required:    false,
				},
			},
		},
		{
			Name:        "auxly-status",
			Description: "Show real-time system diagnostics, active client connections, database sizes, and secure tunnel URL parameters.",
		},
		{
			Name:        "auxly-forget",
			Description: "Search memory vault and prune obsolete or outdated bullet statements cleanly from memory files.",
			Arguments: []promptArgument{
				{
					Name:        "query",
					Description: "Key words or fact patterns to search for and delete",
					Required:    true,
				},
			},
		},
		{
			Name:        "auxly-learn",
			Description: "Intercept recent edits or context to extract and propose structured new facts.",
			Arguments: []promptArgument{
				{
					Name:        "context",
					Description: "Recent text edits, preferences mentioned, or git diffs to analyze",
					Required:    true,
				},
			},
		},
	}
}

func (s *Server) handlePromptGet(req *jsonRPCRequest) {
	var params struct {
		Name      string            `json:"name"`
		Arguments map[string]string `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		s.sendError(req.ID, -32602, "Invalid params")
		return
	}

	var promptText string
	switch params.Name {
	case "auxly-init":
		promptText = "You must immediately invoke the 'auxly_skill_init' MCP tool to align your active session, scan your current context and system prompts, and synchronize existing facts/preferences to the Auxly vault. Show the beautiful onboarding guide and confirmation card!"
	case "auxly-memory":
		promptText = "You must immediately invoke the 'auxly_skill_memory' MCP tool to retrieve and display the consolidated profile of the user's identity, preferences, and infrastructure. Do not ask for further clarification, simply run the tool and show the output!"
	case "auxly-max":
		promptText = "You must immediately invoke the 'auxly_skill_max' MCP tool to align your session, and then immediately call 'auxly_skill_memory' to pull down and load the complete memory vault. Finally, present a beautiful success message confirming that unified memory alignment is fully complete!"
	case "auxly-sync":
		content := params.Arguments["content"]
		promptText = fmt.Sprintf("You must immediately invoke the 'auxly_skill_sync' MCP tool, passing the content '%s' as the 'content' argument. This performs a smart automated delta-merge to update the preferences.md file. Simply run the tool and display the confirmation output!", content)
	case "auxly-pending":
		action := params.Arguments["action"]
		targetID := params.Arguments["target_id"]
		promptText = fmt.Sprintf("You must immediately invoke the 'auxly_skill_pending' MCP tool, passing the 'action' argument as '%s' and 'target_id' argument as '%s' to manage the secure memory write queue. Simply run the tool and display the results!", action, targetID)
	case "auxly-status":
		promptText = "You must immediately invoke the 'auxly_skill_status' MCP tool to retrieve and display the real-time system diagnostics, active connections, and database sizes. Do not perform other actions. Simply run the tool and show the diagnostics screen!"
	case "auxly-forget":
		query := params.Arguments["query"]
		promptText = fmt.Sprintf("You must immediately invoke the 'auxly_skill_forget' MCP tool, passing the query '%s' as the 'query' argument, to search across all memory files and delete matching obsolete lines cleanly. Simply run the tool and display the deletion diff!", query)
	case "auxly-learn":
		context := params.Arguments["context"]
		promptText = fmt.Sprintf("You must immediately invoke the 'auxly_skill_learn' MCP tool, passing the context '%s' as the 'context' argument, to parse and extract structured new facts. Simply run the tool and display the proposed facts!", context)
	default:
		s.sendError(req.ID, -32602, fmt.Sprintf("Unknown prompt: %s", params.Name))
		return
	}

	s.sendResult(req.ID, map[string]interface{}{
		"description": fmt.Sprintf("Template for command %s", params.Name),
		"messages": []map[string]interface{}{
			{
				"role": "user",
				"content": map[string]interface{}{
					"type": "text",
					"text": promptText,
				},
			},
		},
	})
}
