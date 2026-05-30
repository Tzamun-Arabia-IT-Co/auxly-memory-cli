package tui

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// agentSession is a live MCP connection to this memory host, discovered by
// scanning running `auxly mcp-server` processes. Each running server process is
// one connected agent session.
type agentSession struct {
	Provider string // claude, claude-code, cursor, ...
	Remote   bool   // true when served to an SSH-remote client
	PID      int    // OS process id (the live server) — shown for local sessions
	Host     string // remote client hostname (remote only)
	IP       string // remote client IP — real box IP from clients.yaml (remote only)
	OS       string // remote client OS (remote only)
}

var (
	reEnvProvider = regexp.MustCompile(`AUXLY_PROVIDER=([^\s]+)`)
	reArgProvider = regexp.MustCompile(`--provider[= ]([^\s]+)`)
	reRemoteHost  = regexp.MustCompile(`--remote-host[= ]([^\s]+)`)
	reRemoteOS    = regexp.MustCompile(`--remote-os[= ]([^\s]+)`)
)

// procInfo is a snapshot of one running process (parent + command line).
type procInfo struct {
	ppid    int
	command string
}

// gatherSessions lists every live `auxly mcp-server` process as a connected
// agent session. Remote sessions are fully attributed from the server's argv
// (--source ssh-remote --provider --remote-host --remote-os); local sessions
// fall back to parent-process inference because the OS hides their environment.
//
// The process snapshot is gathered per-OS (see scanProcs in connections_unix.go
// and connections_windows.go); everything below is platform-independent.
func gatherSessions() []agentSession {
	procs := scanProcs()
	if len(procs) == 0 {
		return nil
	}

	// A server session is an `auxly ... mcp-server` invocation where the binary
	// itself (argv[0]) is auxly — this excludes the macOS Gatekeeper "disclaimer"
	// wrapper that re-launches it (a duplicate), the node-based legacy server,
	// and any shell/scan lines that merely mention mcp-server.
	var servers []int
	for pid, p := range procs {
		if strings.Contains(p.command, "mcp-server") && isAuxlyBinary(firstToken(p.command)) {
			servers = append(servers, pid)
		}
	}
	sort.Ints(servers) // stable display order (map iteration is random)

	clients := readClients() // name -> target lookup for real box IPs

	var sessions []agentSession
	for _, pid := range servers {
		sessions = append(sessions, parseSession(pid, procs[pid].command, procs, clients))
	}
	return sessions
}

// firstToken extracts argv[0] from a full command line, handling a quoted
// leading path (common on Windows, e.g. "C:\Program Files\auxly\auxly.exe").
func firstToken(command string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return ""
	}
	if command[0] == '"' {
		if end := strings.IndexByte(command[1:], '"'); end >= 0 {
			return command[1 : 1+end]
		}
		return command[1:]
	}
	if i := strings.IndexByte(command, ' '); i >= 0 {
		return command[:i]
	}
	return command
}

// parseSession turns one live `auxly mcp-server` process into an agentSession.
// Remote sessions are attributed entirely from argv; local ones fall back to
// the AUXLY_PROVIDER env (when visible) or parent-process inference.
func parseSession(pid int, cmd string, procs map[int]procInfo, clients []clientRow) agentSession {
	s := agentSession{PID: pid}

	if strings.Contains(cmd, "--source ssh-remote") || strings.Contains(cmd, "--source=ssh-remote") {
		s.Remote = true
		if m := reArgProvider.FindStringSubmatch(cmd); m != nil {
			s.Provider = m[1]
		}
		if m := reRemoteHost.FindStringSubmatch(cmd); m != nil {
			s.Host = m[1]
		}
		if m := reRemoteOS.FindStringSubmatch(cmd); m != nil {
			s.OS = m[1]
		}
		s.IP = remoteIPForHost(s.Host, clients)
	} else {
		if m := reEnvProvider.FindStringSubmatch(cmd); m != nil {
			s.Provider = m[1]
		} else if m := reArgProvider.FindStringSubmatch(cmd); m != nil {
			s.Provider = m[1]
		} else {
			s.Provider = inferProviderFromAncestors(pid, procs)
		}
	}

	if s.Provider == "" {
		s.Provider = "claude"
	}
	return s
}

// isAuxlyBinary reports whether the given command token (argv[0]) is the auxly
// executable itself, not a wrapper that happens to mention it.
func isAuxlyBinary(arg0 string) bool {
	base := strings.ToLower(filepath.Base(arg0))
	return base == "auxly" || base == "auxly.exe"
}

// remoteIPForHost resolves a remote client's real box IP from clients.yaml
// (the SSH_CONNECTION IP is the tunnel's localhost, which is meaningless).
func remoteIPForHost(host string, clients []clientRow) string {
	for _, c := range clients {
		if !strings.EqualFold(c.Name, host) {
			continue
		}
		target := c.Target
		if at := strings.LastIndex(target, "@"); at >= 0 {
			target = target[at+1:]
		}
		if target != "" {
			return target
		}
	}
	return ""
}

// inferProviderFromAncestors walks a local server's parent chain looking for a
// recognizable IDE/agent app signature, used when the AUXLY_PROVIDER env is not
// visible in the command line (macOS hides process env; Windows CommandLine
// carries args but not env).
func inferProviderFromAncestors(pid int, procs map[int]procInfo) string {
	cur := pid
	for depth := 0; depth < 8; depth++ {
		p, ok := procs[cur]
		if !ok || p.ppid == 0 || p.ppid == cur {
			break
		}
		parent, ok := procs[p.ppid]
		if !ok {
			break
		}
		lc := strings.ToLower(parent.command)
		switch {
		case strings.Contains(lc, "claude.app") || strings.Contains(lc, "claude helper"):
			return "claude"
		case strings.Contains(lc, "claude") && strings.Contains(lc, "cli"):
			return "claude-code"
		case strings.Contains(lc, "cursor"):
			return "cursor"
		case strings.Contains(lc, "antigravity"):
			return "antigravity"
		case strings.Contains(lc, "codex"):
			return "codex"
		case strings.Contains(lc, "gemini"):
			return "gemini"
		}
		cur = p.ppid
	}
	return "claude"
}
