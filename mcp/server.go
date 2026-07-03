package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/audit"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/config"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/embed"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/pending"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/session"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/sharing"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/trust"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/update"
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
	// isRemote is true when this server is serving an SSH-remote consumer; when
	// true the per-remote file-sharing ACL (share) gates every read and write.
	isRemote bool
	share    *sharing.ClientShare
	// newEmbedder builds the embedder used by semantic recall. Injectable so tests
	// can substitute a deterministic offline stub instead of hitting a live model.
	newEmbedder func() memory.Embedder
	// staleLink carries the "this box lost its remote-memory wiring" banner
	// (empty when healthy). Computed once at start — a repaired link is picked
	// up on the next server launch, which is every agent restart.
	staleLink string
	mu        sync.Mutex
}

// NewServer creates a new MCP server.
func NewServer(memoryPath string) *Server {
	store := memory.NewStore(memoryPath)
	logger, _ := audit.NewLogger(memoryPath)
	pendingMgr := pending.NewManager(memoryPath)
	meta := resolveSourceMeta()

	s := &Server{
		memoryPath: memoryPath,
		store:      store,
		logger:     logger,
		pendingMgr: pendingMgr,
		outWriter:  os.Stdout,
		sourceMeta: meta,
	}
	// Default embedder factory: a real local-first embed client. Tests override
	// s.newEmbedder with a deterministic offline stub.
	s.newEmbedder = func() memory.Embedder { return embed.New() }
	// Recall analytics: forward each recall's event to the audit layer. Only
	// hashes cross this boundary (never query or fact text). Provider resolves
	// at event time; a logger failure must never surface into the recall.
	store.RecallObserver = func(ev memory.RecallEvent) {
		if s.logger == nil {
			return
		}
		hits := make([]audit.RecallHitRecord, 0, len(ev.Hits))
		for _, h := range ev.Hits {
			hits = append(hits, audit.RecallHitRecord{File: h.File, LineHash: h.LineHash, Score: h.Score, Rank: h.Rank, Accepted: h.Accepted})
		}
		_ = s.logger.RecordRecall(audit.RecallMeta{
			Provider:   s.resolveProvider(),
			QueryHash:  ev.QueryHash,
			Fallback:   ev.Fallback,
			Source:     s.sourceMeta.Source,
			RemoteHost: s.sourceMeta.RemoteHost,
		}, hits)
	}
	// §10 per-remote file sharing: when serving an SSH-remote consumer, load that
	// remote's sharing ACL from the host's clients.yaml (nil → safe default).
	s.isRemote = meta.Source == "ssh-remote"
	// Consumer link guard: a LOCAL server on a box whose remote wiring vanished
	// must say so on every response instead of silently serving stale data.
	// (After isRemote is known — host-side remote sessions skip the disk reads.)
	if !s.isRemote {
		s.staleLink = staleLinkWarning()
	}
	if s.isRemote {
		s.share = sharing.LoadForRemoteHost(memoryPath, meta.RemoteHost)
		// Opt-in default-write (config DefaultRemoteWrite): upgrade a MATCHED client
		// with no explicit per-file grant to read+write. A nil/unmatched share is
		// left read-only — an unknown remote never gains write from this flag.
		if s.share != nil && config.LoadSettings().DefaultRemoteWrite {
			s.share = sharing.WithDefaultWrite(s.share)
		}
	}
	return s
}

// vaultFileNames returns the names of all files currently in the vault.
func (s *Server) vaultFileNames() []string {
	files, err := s.store.List()
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(files))
	for _, f := range files {
		names = append(names, f.Name)
	}
	return names
}

// canRead reports whether the current caller may read a file. Local sessions have
// full access; SSH-remote sessions are gated by the per-remote sharing ACL. The
// personal tier is OFF BY DEFAULT for remotes (AllowedReads fail-closes it), but
// the host owns the data and may deliberately grant it — an explicit shared_files
// entry is honored. The TUI surfaces a warning before that choice is made.
func (s *Server) canRead(file string) bool {
	if !s.isRemote {
		return true
	}
	return sharing.CanRead(s.share, file, s.vaultFileNames())
}

// canWrite reports whether the current caller may write a file. Local sessions
// have full access; SSH-remote sessions require an explicit write grant. As with
// reads, personal-tier writes are off by default but can be granted by the host.
func (s *Server) canWrite(file string) bool {
	if !s.isRemote {
		return true
	}
	return sharing.CanWrite(s.share, file, s.vaultFileNames())
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
		// Echo the client's requested protocol version when present. Our tools
		// are version-agnostic, and strict clients (e.g. cursor-agent) hide all
		// tools when the server answers a newer request with an older version.
		protocol := "2024-11-05"
		if len(req.Params) > 0 {
			var p struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			if json.Unmarshal(req.Params, &p) == nil && p.ProtocolVersion != "" {
				protocol = p.ProtocolVersion
			}
		}
		s.sendResult(req.ID, initializeResult{
			ProtocolVersion: protocol,
			Capabilities: map[string]interface{}{
				"tools":   map[string]interface{}{},
				"prompts": map[string]interface{}{},
			},
			ServerInfo: serverInfo{
				Name:    "auxly-memory",
				Version: update.Current,
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

	case "ping":
		// Standard MCP liveness check — a compliant server MUST answer with an
		// empty result. Strict clients (e.g. Antigravity) ping after connecting
		// and close the connection if it isn't honored ("client is closing:
		// invalid request"), which silently hides all tools.
		s.sendResult(req.ID, map[string]interface{}{})

	case "resources/list":
		// We don't advertise the resources capability, but some clients probe it
		// anyway. Answer with an empty list rather than an error so the probe
		// doesn't trip strict clients into closing.
		s.sendResult(req.ID, map[string]interface{}{"resources": []interface{}{}})

	case "resources/templates/list":
		s.sendResult(req.ID, map[string]interface{}{"resourceTemplates": []interface{}{}})

	default:
		// Notifications carry no id and never expect a reply — answering one with
		// an error response is itself a protocol violation that can make strict
		// clients drop the connection. Only error on genuine requests.
		if req.ID == nil || strings.HasPrefix(req.Method, "notifications/") {
			return
		}
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
					"file":   {Type: "string", Description: "Target file (e.g. identity.md, preferences.md)"},
					"diff":   {Type: "string", Description: "Content to add. Prefix lines with + to append."},
					"reason": {Type: "string", Description: "Why this memory is being written/updated"},
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
			Name:        "auxly_memory_recall",
			Description: "Semantic recall over the memory vault — find relevant memory by meaning, not exact keyword. Falls back to substring search when no embedding model is available.",
			InputSchema: inputSchema{
				Type: "object",
				Properties: map[string]property{
					"query": {Type: "string", Description: "Natural-language query to recall relevant memory by meaning"},
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
			Description: "Slash skill '/auxly-max': Exhaustive self-harvest — directs you to scan your entire session and push every fact up into the vault, one focused slice per category (push-only).",
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
					"category": {Type: "string", Description: "Target file. Use 'personal' for the USER'S OWN private life — their family, health, relationships, and their personal legal/financial matters (their own lawsuit, divorce, custody, personal loan, salary); a company/business legal or money matter is NOT personal, use 'business'. Other areas: preferences (default), identity, infra, products, projects, daily, agents. When a fact is about the user's private life, choose 'personal' — it overrides any topical category."},
					"scope":    {Type: "string", Description: "Vault scoping: 'global' (default) or project 'workspace'"},
				},
				Required: []string{"content"},
			},
		},
		{
			Name:        "auxly_skill_pending",
			Description: "Slash skill '/auxly-pending': List memory changes awaiting human approval. Approving/rejecting is human-only — done in the Auxly dashboard's Approvals tab or via 'auxly approve <id>' / 'auxly reject <id>' in the terminal, never by the agent.",
			InputSchema: inputSchema{
				Type: "object",
				Properties: map[string]property{
					"action":    {Type: "string", Description: "Only 'list' is available over MCP (the default). Approve/reject are human-only — the agent must ask the user to run 'auxly approve <id>' / 'auxly reject <id>' locally or use the dashboard."},
					"target_id": {Type: "string", Description: "Optional entry ID, used only to reference an entry when telling the user what to approve locally."},
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
			Description: "Slash skill '/auxly-learn [folder] [topic]': Read & internalize the user's memory vault — absorb it and operate from it for the rest of the session. Optionally scope to one category folder and a topic.",
			InputSchema: inputSchema{
				Type: "object",
				Properties: map[string]property{
					"folder": {Type: "string", Description: "Optional category slug or filename to scope to (e.g. infra, projects, personal.md). Omit to internalize the whole vault."},
					"topic":  {Type: "string", Description: "Optional topic to focus the internalization on (e.g. nginx)"},
				},
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
		{
			Name:        "auxly_skill_bootstrap",
			Description: "Slash skill '/auxly-bootstrap': Generate a copyable onboarding block to paste into a tool that does NOT have Auxly installed (e.g. ChatGPT), so it can read/write the user's memory",
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
		// C1: provider identity is SERVER-SIDE attribution only. We never trust a
		// client-supplied "provider" arg for trust enforcement — otherwise an agent
		// could claim an `auto`-trusted provider to bypass its own require_approval/
		// read_only policy. Resolve from the launcher env / process ancestry and log
		// any client-supplied mismatch for the audit trail.
		provider := s.resolveProvider()
		if claimed, _ := params.Arguments["provider"].(string); strings.TrimSpace(claimed) != "" && !strings.EqualFold(strings.TrimSpace(claimed), provider) {
			s.logActivity(provider, "provider_mismatch", file)
		}
		result = s.toolWrite(file, diff, reason, provider)
	case "auxly_memory_search":
		query, _ := params.Arguments["query"].(string)
		s.logActivity("", "search", "")
		result = s.toolSearch(query)
	case "auxly_memory_recall":
		query, _ := params.Arguments["query"].(string)
		s.logActivity("", "recall", "")
		result = s.toolRecall(query)
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
		folder, _ := params.Arguments["folder"].(string)
		topic, _ := params.Arguments["topic"].(string)
		s.logActivity("", "skill_learn", "")
		result = s.toolSkillLearn(folder, topic)
	case "auxly_skill_remote_connect":
		s.logActivity("", "skill_remote_connect", "")
		result = s.toolSkillRemoteConnect()
	case "auxly_skill_bootstrap":
		s.logActivity("", "skill_bootstrap", "")
		result = s.toolSkillBootstrap()
	default:
		result = toolResult{
			Content: []toolContent{{Type: "text", Text: fmt.Sprintf("Unknown tool: %s", params.Name)}},
			IsError: true,
		}
	}

	s.sendResult(req.ID, result)
}

var (
	providerOnce   sync.Once
	cachedProvider string
)

// getProviderFromParent infers this server's provider from its own process
// ancestry, delegating to the shared session attribution helpers so the server
// and the dashboard always agree on which agent a process belongs to.
//
// The result is invariant for the process lifetime (own PID + ancestry never
// change), so it is computed exactly once. This is critical on Windows, where
// session.AncestorCommands cold-starts a PowerShell + CIM query on every call —
// previously hit on startup, on every logActivity, and 3× per skill_sync,
// stalling strict MCP clients into closing the connection.
func getProviderFromParent() string {
	providerOnce.Do(func() {
		cachedProvider = session.InferProvider(session.AncestorCommands(os.Getpid()))
	})
	return cachedProvider
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
		if !s.canRead(f.Name) {
			continue // §10: hide files not shared with this remote
		}
		sb.WriteString(fmt.Sprintf("• %s (%d bytes, modified %s)\n", f.Name, f.Size, f.ModTime.Format("2006-01-02 15:04")))
	}
	return toolResult{Content: []toolContent{{Type: "text", Text: sb.String()}}}
}

func (s *Server) toolRead(file string) toolResult {
	if file == "" {
		return toolResult{Content: []toolContent{{Type: "text", Text: "Error: file parameter required"}}, IsError: true}
	}
	if !s.canRead(file) {
		return toolResult{Content: []toolContent{{Type: "text", Text: fmt.Sprintf("🔒 '%s' is not shared with this remote connection.", file)}}, IsError: true}
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

	// §10: SSH-remote consumers may only write files explicitly granted to them.
	if !s.canWrite(file) {
		return toolResult{Content: []toolContent{{Type: "text", Text: fmt.Sprintf("🔒 This remote connection does not have write access to '%s'.", file)}}, IsError: true}
	}

	// C1: EVERY write path (auxly_memory_write, auxly_skill_sync, …) is gated by
	// SERVER-SIDE provider attribution. We ignore any caller-supplied provider for
	// the trust decision so no MCP tool can route around the authoritative identity.
	provider = s.resolveProvider()

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

	// Staleness handling: a new fact that contradicts an existing one in the
	// target file becomes a REPLACE (dated, with a "was:" trace) instead of a
	// second bullet. Runs BEFORE trust routing so require_approval agents
	// review the replacement, and auto trust audits it.
	diff = s.maybeSupersede(file, diff)

	if level == trust.LevelRequireApproval {
		pendingName, err := s.pendingMgr.WriteFrom(file, diff, provider)
		if err != nil {
			return toolResult{Content: []toolContent{{Type: "text", Text: fmt.Sprintf("Error: %v", err)}}, IsError: true}
		}
		if s.logger != nil {
			s.logger.LogWithSource(agentID, provider, "write", file, diff, reason, level, s.sourceMeta)
		}
		return toolResult{Content: []toolContent{{Type: "text", Text: s.withSyncFooter(fmt.Sprintf("⏳ Change queued for approval: .pending/%s\nHuman must run 'auxly approve %s' to apply.", pendingName, pendingName))}}}
	}

	// Auto trust: write directly
	var existing string
	if data, verr := s.store.View(file); verr != nil {
		if !errors.Is(verr, os.ErrNotExist) {
			// Fail closed: a non-NotExist error (e.g. an encrypted file whose
			// key is unreachable) must NEVER be treated as an empty file —
			// that would let ApplyDiff+WriteScoped re-encrypt the file with
			// ONLY this new diff, silently wiping every prior fact.
			return toolResult{Content: []toolContent{{Type: "text", Text: fmt.Sprintf("❌ Cannot read %s: %v — run `auxly encrypt status`", file, verr)}}, IsError: true}
		}
	} else {
		existing = data
	}
	content := pending.ApplyDiff(existing, diff)

	if err := s.store.WriteScoped(file, content, scope); err != nil {
		return toolResult{Content: []toolContent{{Type: "text", Text: fmt.Sprintf("Error writing: %v", err)}}, IsError: true}
	}

	if s.logger != nil {
		s.logger.LogWithSource(agentID, provider, "write", file, diff, reason, level, s.sourceMeta)
	}

	msgText := fmt.Sprintf("✅ Synced → %s.", file)
	return toolResult{Content: []toolContent{{Type: "text", Text: s.withSyncFooter(msgText)}}}
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
		return toolResult{Content: []toolContent{{Type: "text", Text: "No results in readable memory for that query."}}}
	}

	var sb strings.Builder
	shown := 0
	for file, lines := range results {
		// §10: never leak unshared/private files (e.g. personal.md) to an
		// SSH-remote peer through search. Local sessions pass canRead freely.
		if !s.canRead(file) {
			continue
		}
		sb.WriteString(fmt.Sprintf("\n📄 %s\n", file))
		for _, line := range lines {
			sb.WriteString(fmt.Sprintf("   %s\n", line))
		}
		shown++
	}
	if shown == 0 {
		return toolResult{Content: []toolContent{{Type: "text", Text: "No results in readable memory for that query."}}}
	}
	return toolResult{Content: []toolContent{{Type: "text", Text: sb.String()}}}
}

// toolRecall performs semantic recall over the vault. ACL is enforced twice: once
// as the index Load pre-filter (s.canRead, passed into Recall) so unshared vectors
// are never scored, and again at render time (belt-and-suspenders). unified_memory.md
// is hard-excluded at both layers. When no embedding model is available it falls
// back to exact substring search so an offline box never hard-fails.
func (s *Server) toolRecall(query string) toolResult {
	if query == "" {
		return toolResult{Content: []toolContent{{Type: "text", Text: "Error: query parameter required"}}, IsError: true}
	}

	emb := s.newEmbedder()
	hits, err := s.store.Recall(context.Background(), query, 8, emb, s.canRead)
	if errors.Is(err, embed.ErrUnavailable) {
		// Offline / no model: fall back to exact substring search (still ACL-gated).
		// Record the fallback as a query event first — the fallback RATE is the
		// health signal `auxly stats --recall` surfaces (a high rate means agents
		// rarely get semantic results). Hashes only; a zero-hit query still counts
		// via a single non-accepted marker row.
		if s.logger != nil {
			var frecs []audit.RecallHitRecord
			if results, serr := s.store.Search(query); serr == nil {
				i := 0
				for file := range results {
					if !s.canRead(file) || file == "unified_memory.md" {
						continue
					}
					frecs = append(frecs, audit.RecallHitRecord{File: file, Rank: i, Accepted: true})
					i++
				}
			}
			if len(frecs) == 0 {
				frecs = append(frecs, audit.RecallHitRecord{Accepted: false})
			}
			_ = s.logger.RecordRecall(audit.RecallMeta{
				Provider:   s.resolveProvider(),
				QueryHash:  memory.HashRecallText(query),
				Fallback:   true,
				Source:     s.sourceMeta.Source,
				RemoteHost: s.sourceMeta.RemoteHost,
			}, frecs)
		}
		return s.toolSearch(query)
	}
	if err != nil {
		return toolResult{Content: []toolContent{{Type: "text", Text: fmt.Sprintf("Error: %v", err)}}, IsError: true}
	}

	// MAJOR 10: encrypted files are structurally excluded from the semantic
	// index (see refreshFile in recall.go) with no other in-band signal —
	// tell the caller so a missing fact never reads as "doesn't exist" when
	// it's really just "encrypted, ask via memory_read instead".
	note := ""
	if n := s.store.EncryptedFileCount(); n > 0 {
		note = fmt.Sprintf("\n\nnote: %d encrypted file(s) are excluded from semantic recall — content stays available via memory_read (auxly encrypt status)", n)
	}

	// Group hits by file, re-applying canRead and the aggregate exclusion at render
	// time as a second guard over the Load pre-filter.
	var sb strings.Builder
	currentFile := ""
	shown := 0
	for _, h := range hits {
		if h.File == "unified_memory.md" || !s.canRead(h.File) {
			continue
		}
		if h.File != currentFile {
			sb.WriteString(fmt.Sprintf("\n📄 %s\n", h.File))
			currentFile = h.File
		}
		if h.Heading != "" {
			sb.WriteString(fmt.Sprintf("   ## %s\n", h.Heading))
		}
		snippet := strings.ReplaceAll(strings.TrimSpace(h.Text), "\n", "\n   ")
		sb.WriteString(fmt.Sprintf("   %s\n   (lines %d–%d)\n", snippet, h.LineStart, h.LineEnd))
		shown++
	}

	if shown == 0 {
		return toolResult{Content: []toolContent{{Type: "text", Text: "No results in readable memory for that query." + note}}}
	}
	return toolResult{Content: []toolContent{{Type: "text", Text: sb.String() + note}}}
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

// withSyncFooter is the compact footer for write-confirmation responses. It omits
// the full taxonomy (already loaded via auxly_skill_init) to keep tool responses
// small and avoid bloating the model's context window on every auxly_skill_sync call.
func (s *Server) withSyncFooter(text string) string {
	if s.isRemote {
		return text + s.remoteScopeFooter()
	}
	return text + s.staleLink + "\n\n---\n🧠 **Auxly Agent Sync Active:** Proactively call `auxly_skill_sync` when you learn new facts."
}

// withFooter appends the standard Auxly sync reminder + category guide to a tool
// reply. For a LOCAL session it shows the full taxonomy. For an SSH-REMOTE peer it
// renders an ACL-scoped footer that lists ONLY the files that remote may read or
// write and never even names personal/unshared files — closing the gap where the
// onboarding text advertised personal.md to a remote whose data layer correctly
// blocked it (the agent "saw" the category in the guide, not the contents).
func (s *Server) withFooter(text string) string {
	if s.isRemote {
		return text + s.remoteScopeFooter()
	}
	return text + s.staleLink +
		"\n\n---\n🧠 **Auxly Agent Sync Active:** Remember to proactively call `auxly_skill_sync` to update the memory vault whenever you learn new preferences, system setups, or developer details!" +
		"\n\n📂 WHERE FACTS GO (file the right place the first time):\n" + memory.RenderForPrompt()
}

// remoteScopeFooter states the host-enforced sharing scope to a connected remote
// agent and renders a category guide limited to the files it can actually use.
// Decisions are made against the full taxonomy (not just files that happen to
// exist yet) so a writable-but-empty category still shows as a valid target; the
// personal-tier hard block in canRead/canWrite means private files are never
// listed regardless of any per-peer config.
func (s *Server) remoteScopeFooter() string {
	allFiles := memory.OrderedFiles()
	readCat := func(f string) bool { return sharing.CanRead(s.share, f, allFiles) }
	writeCat := func(f string) bool { return sharing.CanWrite(s.share, f, allFiles) }

	var reads, writes []string
	for _, f := range allFiles {
		if readCat(f) {
			reads = append(reads, f)
		}
		if writeCat(f) {
			writes = append(writes, f)
		}
	}

	var b strings.Builder
	b.WriteString("\n\n---\n🔒 **Remote access scope (host-enforced — do not exceed it):**\n")
	b.WriteString("You are connected as a REMOTE consumer. The host shares a LIMITED view of its memory.\n")
	if len(reads) == 0 {
		b.WriteString("• Readable: none.\n")
	} else {
		b.WriteString("• Readable: " + strings.Join(reads, ", ") + "\n")
	}
	if len(writes) == 0 {
		b.WriteString("• Writable: none — this is a READ-ONLY connection; `auxly_skill_sync` will be rejected.\n")
	} else {
		b.WriteString("• Writable (via `auxly_skill_sync`): " + strings.Join(writes, ", ") + "\n")
	}
	b.WriteString("• Private & unshared files are NOT accessible and are not listed here. Do not claim, infer, or attempt access to them.\n")
	b.WriteString("\n📂 WHERE FACTS GO (only the categories you may use):\n")
	b.WriteString(memory.RenderForPromptScoped(readCat, writeCat))
	return b.String()
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

	return toolResult{Content: []toolContent{{Type: "text", Text: s.withFooter(onboarding + s.sessionPrimer())}}}
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
		if !s.canRead(f.Name) {
			continue // §10: never expose unshared files to a remote profile read
		}
		content, err := s.store.View(f.Name)
		if err == nil && len(strings.TrimSpace(content)) > 0 {
			readAny = true
			sb.WriteString(fmt.Sprintf("### 📄 %s\n\n", f.Name))
			sb.WriteString(content)
			sb.WriteString("\n\n---\n\n")
		}
	}

	if !readAny {
		return toolResult{Content: []toolContent{{Type: "text", Text: s.withSyncFooter("⚠️ No memory files populated yet. Type `/auxly-sync [content]` or paste your onboarding prompt to save your first memory!")}}}
	}

	return toolResult{Content: []toolContent{{Type: "text", Text: s.withSyncFooter(sb.String())}}}
}

func (s *Server) toolSkillMax() toolResult {
	prompt := `🧠 AUXLY MAXIMUM MEMORY — EXHAUSTIVE SELF-HARVEST (PUSH-ONLY)

This is the deliberate "dump my full session into the vault NOW" sweep. You are the
extractor: the tool cannot read your mind, so YOU must scan everything you know and
write it up. Do NOT pull or display the vault — this is push-only.

🎯 DO THIS NOW (do not ask for permission, do not stop early):

1. SCAN YOUR ENTIRE CURRENT SESSION/CONTEXT — the system prompt, the full chat
   history, every fact, preference, decision, system detail, and personal detail
   you have learned about the user during this session.

2. WALK THE CATEGORIES BELOW IN ORDER. For EACH category, extract every fact you
   know that belongs there and write it up immediately with the 'auxly_skill_sync'
   tool — ONE focused slice per category (pass the matching 'category' so it lands
   in the right file). Process them in this exact order:
   identity → personal → preferences → infra → products → projects → daily →
   business → agents.

3. RECONCILE, DON'T DUPLICATE: before writing each slice, account for what is
   already in that file and only add genuinely new or updated facts — never
   re-write facts already saved.

4. PERSONAL/PRIVATE facts go to personal.md via category 'personal' — and keep
   them OUT of the shared files. This means the USER'S OWN family, relationships,
   health, and their PERSONAL legal/financial matters (their own lawsuit/court
   case, divorce, custody, personal loan, salary, bank). Judge by context, not the
   topic word: a legal or money matter about the USER or their family is PERSONAL;
   the same kind of matter about the COMPANY/a client/the business is 'business'.
   When a fact is the user's private affair, 'personal' ALWAYS wins — a personal
   legal case is NEVER a 'project' or 'business' entry.

Each slice is small, atomic, trust-gated, and auditable. Write every slice now —
work category by category until you have emptied your knowledge of the user into
the correct files.

` + memory.RenderForPrompt()

	// withSyncFooter: taxonomy is already in the body above; no need to repeat it.
	return toolResult{Content: []toolContent{{Type: "text", Text: s.withSyncFooter(prompt)}}}
}

func (s *Server) toolSkillSync(content, category, scope string) toolResult {
	if strings.TrimSpace(content) == "" {
		return toolResult{Content: []toolContent{{Type: "text", Text: "Error: Content cannot be empty"}}, IsError: true}
	}

	// Semantic Auto-Router: If category is empty or preferences (default), fall back
	// to the canonical taxonomy router so placement stays consistent everywhere.
	if category == "" || category == "preferences" {
		category = memory.RouteCategory(content)
	}

	fileName := memory.FileForCategory(category)
	// Per-project sub-files: a projects fact written from a workspace lands in
	// that project's own file (projects/<repo-slug>.md) instead of the shared
	// monolith, so two repos never interleave their notes. The slug ALREADY
	// encodes the workspace, so the write is pinned to the global vault — a
	// workspace-scoped copy of the same slug would shadow the global one only
	// for callers inside that repo and split the fact history in two.
	if category == "projects" {
		fileName = memory.ProjectFile(s.store.WorkspaceRoot)
		scope = "global"
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
			res.Content[0].Text = s.withSyncFooter(res.Content[0].Text)
		}
		return res
	}

	if targetID == "" {
		return toolResult{Content: []toolContent{{Type: "text", Text: "Error: Please specify the pending entry filename/ID to resolve."}}, IsError: true}
	}

	if action == "approve" || action == "reject" {
		// C2: approving/rejecting is a HUMAN-only action and is intentionally NOT
		// performed over MCP. Otherwise an agent on require_approval could approve
		// its OWN pending write and bypass the human gate entirely. Direct the user
		// to a channel the agent cannot drive: the dashboard or the local CLI.
		msg := fmt.Sprintf("🔒 Approving or rejecting pending changes is human-only — an agent can't do it.\n"+
			"Ask the user to %s entry %q via either:\n"+
			"  • the Auxly dashboard → Approvals tab, or\n"+
			"  • their terminal:  auxly %s %s\n\n"+
			"Over MCP this tool only LISTS pending entries.", action, targetID, action, targetID)
		return toolResult{Content: []toolContent{{Type: "text", Text: s.withSyncFooter(msg)}}}
	}

	return toolResult{Content: []toolContent{{Type: "text", Text: fmt.Sprintf("Error: action %q is not available over MCP. The only MCP action is 'list'; approve/reject are human-only (run 'auxly approve <id>' / 'auxly reject <id>' locally).", action)}}, IsError: true}
}

func (s *Server) toolSkillStatus() toolResult {
	var sb strings.Builder
	sb.WriteString("📡 **AUXLY STATUS**\n\n")

	// The two things a status check is actually for: is the MCP link up, and is
	// memory connected. The fact this tool produced a reply IS the proof the
	// agent↔Auxly MCP channel is live — state it plainly so the agent doesn't go
	// off "investigating an MCP failure."
	sb.WriteString("✅ **MCP link:** connected & responding (you're reading this through it).\n")

	memoryLine := "✅ **Memory:** local vault active"
	if s.sourceMeta.Source == "ssh-remote" {
		memoryLine = fmt.Sprintf("✅ **Memory:** ssh-remote vault — %s", describeRemote(s.sourceMeta))
	}
	sb.WriteString(memoryLine + "\n")

	if stats, err := s.logger.Stats(); err == nil && stats != nil {
		sb.WriteString(fmt.Sprintf("• **Writes Today:** %d\n", stats.WritesToday))
		sb.WriteString(fmt.Sprintf("• **Total Memory Entries:** %d\n", stats.TotalEntries))
	}

	return toolResult{Content: []toolContent{{Type: "text", Text: s.withSyncFooter(sb.String())}}}
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
		return toolResult{Content: []toolContent{{Type: "text", Text: s.withSyncFooter(sb.String())}}}
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
		return toolResult{Content: []toolContent{{Type: "text", Text: s.withSyncFooter(sb.String())}}}
	}

	sb.WriteString("This session is using a LOCAL Auxly vault, and no remote host is offered on this machine.\n")
	sb.WriteString("To link one, run `auxly host setup` on the memory host (it publishes an offer here), then `auxly connect auto`.")
	return toolResult{Content: []toolContent{{Type: "text", Text: s.withSyncFooter(sb.String())}}}
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

	// C1: pruning DELETES memory content, so gate it by SERVER-SIDE provider trust
	// exactly like writes. A destructive delete can't be queued through the additive
	// pending flow, so read_only and require_approval providers cannot prune over
	// MCP — the human prunes locally.
	provider := s.resolveProvider()
	trustCfg, terr := trust.Load(s.memoryPath)
	if terr != nil {
		return toolResult{Content: []toolContent{{Type: "text", Text: fmt.Sprintf("Error loading trust config: %v", terr)}}, IsError: true}
	}
	if level := trustCfg.GetTrustLevel(provider); level != trust.LevelAuto {
		return toolResult{Content: []toolContent{{Type: "text", Text: fmt.Sprintf("❌ Provider %q is %s — pruning memory over MCP requires 'auto' trust. Ask the user to prune locally (e.g. 'auxly forget').", provider, level)}}, IsError: true}
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
		// §10: an SSH-remote peer may only prune files it has write access to;
		// never let it delete from unshared/read-only files (e.g. personal.md).
		if !s.canWrite(file) {
			continue
		}
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
		return toolResult{Content: []toolContent{{Type: "text", Text: s.withSyncFooter(fmt.Sprintf("No matching facts or bullets found in memory for query: \"%s\"", query))}}}
	}

	sb.WriteString(fmt.Sprintf("✓ Successfully pruned %d obsolete statement(s) from memory vault.", deletedCount))
	return toolResult{Content: []toolContent{{Type: "text", Text: s.withSyncFooter(sb.String())}}}
}

// toolSkillLearn is the inbound "read & internalize" directive. It loads vault
// content (the whole vault, or one scoped category file) and wraps it in a strong
// directive telling the agent to absorb it and operate from it for the session.
func (s *Server) toolSkillLearn(folder, topic string) toolResult {
	folder = strings.TrimSpace(folder)
	topic = strings.TrimSpace(topic)

	// Scoped: resolve a single category file from a slug or filename.
	if folder != "" {
		fileName := ""
		if _, ok := memory.CategoryBySlug(folder); ok {
			fileName = memory.FileForCategory(folder)
			// "learn projects" from a workspace means THIS project's facts —
			// the per-project sub-file when it exists, monolith otherwise.
			if folder == "projects" {
				if pf := memory.ProjectFile(s.store.WorkspaceRoot); s.canRead(pf) && fileHasContent(s, pf) {
					fileName = pf
				}
			}
		} else if c, ok := memory.CategoryForFile(folder); ok {
			fileName = c.File
			// An explicit sub-file name ("projects/auxly.md") means that exact
			// file — CategoryForFile only identifies its category.
			if norm := strings.ReplaceAll(folder, "\\", "/"); strings.Contains(norm, "/") {
				fileName = norm
			}
		}

		if fileName == "" {
			var slugs []string
			for _, c := range memory.Taxonomy {
				slugs = append(slugs, c.Slug)
			}
			msg := fmt.Sprintf("Error: unknown category '%s'. Valid category slugs are: %s", folder, strings.Join(slugs, ", "))
			return toolResult{Content: []toolContent{{Type: "text", Text: msg}}, IsError: true}
		}

		if !s.canRead(fileName) {
			return toolResult{Content: []toolContent{{Type: "text", Text: fmt.Sprintf("🔒 '%s' is not shared with this remote connection.", fileName)}}, IsError: true}
		}

		content, err := s.store.View(fileName)
		if err != nil {
			return toolResult{Content: []toolContent{{Type: "text", Text: fmt.Sprintf("Error reading %s: %v", fileName, err)}}, IsError: true}
		}

		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("🧠 **AUXLY LEARN — ABSORB %s**\n\n", fileName))
		if topic != "" {
			sb.WriteString(fmt.Sprintf("focus on: %s\n\n", topic))
		}
		sb.WriteString("ABSORB this memory and operate from it for the rest of the session. Internalize these facts and behave as if you already knew them — do not ask the user to repeat what is below.\n\n")
		sb.WriteString(fmt.Sprintf("### 📄 %s\n\n", fileName))
		sb.WriteString(content)
		// A single-category learn still gets the cross-category primer so the
		// session is grounded beyond the one file (whole-vault learn already
		// contains everything and skips it).
		return toolResult{Content: []toolContent{{Type: "text", Text: s.withSyncFooter(sb.String() + s.sessionPrimer())}}}
	}

	// Whole-vault internalize: read every populated file in canonical order.
	files, err := s.store.List()
	if err != nil {
		return toolResult{Content: []toolContent{{Type: "text", Text: fmt.Sprintf("Error listing memory files: %v", err)}}, IsError: true}
	}

	order := map[string]int{}
	for i, f := range memory.OrderedFiles() {
		order[f] = i + 1
	}
	sort.Slice(files, func(i, j int) bool {
		oi := order[files[i].Name]
		oj := order[files[j].Name]
		if oi == 0 {
			oi = 99
		}
		if oj == 0 {
			oj = 99
		}
		if oi != oj {
			return oi < oj
		}
		return files[i].Name < files[j].Name
	})

	var sb strings.Builder
	sb.WriteString("🧠 **AUXLY LEARN — ABSORB THE FULL MEMORY VAULT**\n\n")
	if topic != "" {
		sb.WriteString(fmt.Sprintf("focus on: %s\n\n", topic))
	}
	sb.WriteString("ABSORB this memory and operate from it for the rest of the session. Internalize everything below and behave as if you already knew it — do not ask the user to repeat facts that are already here.\n\n")

	readAny := false
	for _, f := range files {
		if !s.canRead(f.Name) {
			continue // §10: remote learn never absorbs unshared files
		}
		content, err := s.store.View(f.Name)
		if err == nil && len(strings.TrimSpace(content)) > 0 {
			readAny = true
			sb.WriteString(fmt.Sprintf("### 📄 %s\n\n", f.Name))
			sb.WriteString(content)
			sb.WriteString("\n\n---\n\n")
		}
	}

	if !readAny {
		return toolResult{Content: []toolContent{{Type: "text", Text: s.withSyncFooter("⚠️ No memory files populated yet. Use `/auxly-sync [content]` to save your first memory.")}}}
	}

	return toolResult{Content: []toolContent{{Type: "text", Text: s.withSyncFooter(sb.String())}}}
}

// toolSkillBootstrap generates a copyable onboarding block to PASTE INTO ANOTHER
// tool that does not have Auxly installed (e.g. ChatGPT web). Running it here only
// SHOWS the block — the foreign agent does the actual reads/writes by following it.
func (s *Server) toolSkillBootstrap() toolResult {
	binPath, err := os.Executable()
	if err != nil || strings.TrimSpace(binPath) == "" {
		binPath = "auxly"
	}

	provider := s.resolveProvider()

	var sb strings.Builder
	sb.WriteString("📋 **AUXLY CROSS-TOOL ONBOARDING BLOCK**\n\n")
	sb.WriteString("This block is for PASTING INTO ANOTHER TOOL that does NOT have Auxly installed (e.g. ChatGPT, a colleague's agent, a brand-new tool). Running this skill does NOT sync anything — it only produces the block below. Copy everything between the lines and paste it into the foreign agent so it can read/write the user's Auxly memory.\n\n")
	sb.WriteString("----------------------------------------------------------------\n")
	sb.WriteString("You now have access to the user's **Auxly Unified Memory** — a local-first vault of their identity, preferences, infrastructure, projects, and more. Use it to ground yourself in who the user is, and write any new facts you learn back into it. You have THREE ways to do this; use whichever your environment supports:\n\n")

	sb.WriteString("**Option A — MCP tool (if you have the Auxly MCP server connected):**\n")
	sb.WriteString(fmt.Sprintf("Call the `auxly_memory_write` tool with: provider=\"%s\", file=\"<target>.md\", diff=\"+ - <new fact>\", reason=\"Onboarding\". To read, call `auxly_memory_read` / `auxly_memory_list`.\n\n", provider))

	sb.WriteString("**Option B — CLI (if you can run shell commands on this machine):**\n")
	sb.WriteString("```\n")
	sb.WriteString(fmt.Sprintf("%s write --provider %s --file <file>.md --diff \"+ - <new fact>\" --reason \"Onboarding\"\n", binPath, provider))
	sb.WriteString("```\n")
	sb.WriteString("Use the absolute binary path above. Target the right file per the category list below.\n\n")

	sb.WriteString("**Option C — Manual (if you can do neither):**\n")
	sb.WriteString("Output the new facts as styled markdown bullets grouped by file, and ask the user to paste them into their Auxly vault manually.\n\n")

	sb.WriteString("File each fact into the correct file the first time:\n")
	sb.WriteString(memory.RenderForPrompt())
	sb.WriteString("----------------------------------------------------------------\n")

	return toolResult{Content: []toolContent{{Type: "text", Text: s.withFooter(sb.String())}}}
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
			Description: "Exhaustive self-harvest — scan your whole session and write every fact up into the memory vault, slice by category.",
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
			Description: "List memory changes awaiting human approval. Approving/rejecting is human-only (dashboard Approvals tab or 'auxly approve <id>' / 'auxly reject <id>' in the terminal) — the agent cannot approve.",
			Arguments: []promptArgument{
				{
					Name:        "action",
					Description: "Only 'list' is available over MCP (default). Approve/reject are human-only and must be run locally by the user.",
					Required:    false,
				},
				{
					Name:        "target_id",
					Description: "Optional entry ID, used only to reference an entry when telling the user what to approve locally.",
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
			Description: "Read & internalize the user's memory vault — absorb it and operate from it for the rest of the session. Optionally scope to a folder and topic.",
			Arguments: []promptArgument{
				{
					Name:        "folder",
					Description: "Optional category slug or filename to scope to (e.g. infra, projects, personal.md)",
					Required:    false,
				},
				{
					Name:        "topic",
					Description: "Optional topic to focus the internalization on (e.g. nginx)",
					Required:    false,
				},
			},
		},
		{
			Name:        "auxly-bootstrap",
			Description: "Get a copyable onboarding block to paste into a tool without Auxly installed.",
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
		promptText = "You must immediately invoke the 'auxly_skill_max' MCP tool to receive the exhaustive self-harvest directive, then follow it: scan your entire session and write every fact you know up into the vault via 'auxly_skill_sync', one focused slice per category. This is push-only — do NOT pull or display the vault."
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
		folder := params.Arguments["folder"]
		topic := params.Arguments["topic"]
		promptText = fmt.Sprintf("You must immediately invoke the 'auxly_skill_learn' MCP tool, passing folder='%s' and topic='%s' (either may be empty). It returns the user's memory vault wrapped in a directive — ABSORB that memory and operate from it for the rest of the session.", folder, topic)
	case "auxly-bootstrap":
		promptText = "You must immediately invoke the 'auxly_skill_bootstrap' MCP tool and present the returned copyable block to the user."
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
