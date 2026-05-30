package tui

import (
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
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
// fall back to parent-process inference because macOS hides their environment.
func gatherSessions() []agentSession {
	out, err := exec.Command("ps", "-axww", "-o", "pid=,ppid=,command=").Output()
	if err != nil {
		return nil
	}

	procs := make(map[int]procInfo)
	var servers []int

	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		ppid, _ := strconv.Atoi(fields[1])
		command := strings.TrimSpace(line[strings.Index(line, fields[2]):])
		procs[pid] = procInfo{ppid: ppid, command: command}

		// A server session is an `auxly ... mcp-server` invocation where the
		// binary itself (argv[0]) is auxly — this excludes the macOS Gatekeeper
		// "disclaimer" wrapper that re-launches it (a duplicate), the node-based
		// legacy server, and our own shell/ps scan lines.
		if strings.Contains(command, "mcp-server") && isAuxlyBinary(fields[2]) {
			servers = append(servers, pid)
		}
	}

	clients := readClients() // name -> target lookup for real box IPs

	var sessions []agentSession
	for _, pid := range servers {
		sessions = append(sessions, parseSession(pid, procs[pid].command, procs, clients))
	}
	return sessions
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
// recognizable IDE/agent app signature, since macOS hides the AUXLY_PROVIDER env.
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
