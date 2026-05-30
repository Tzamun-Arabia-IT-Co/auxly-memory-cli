package tui

import (
	"sort"
	"strings"

	"github.com/Tzamun-Arabia-IT-Co/auxly-cli/internal/session"
)

// agentSession is a live MCP connection to this memory host, read from the
// session registry (~/.auxly/sessions) that each running `auxly mcp-server`
// maintains. Each live record is one connected agent session.
type agentSession struct {
	Provider string // claude, claude-code, cursor, ... (authoritative)
	Remote   bool   // true when served to an SSH-remote client
	PID      int    // OS process id (the live server) — shown for local sessions
	Host     string // remote client hostname (remote only)
	IP       string // remote client IP — real box IP from clients.yaml (remote only)
	OS       string // remote client OS (remote only)
}

// gatherSessions returns every live MCP session, pruning records whose process
// has died (a crashed server that never cleaned up its file). Attribution is
// exact: each record was written by the server itself, which knows its own
// provider and source.
func gatherSessions() []agentSession {
	records := session.List()
	if len(records) == 0 {
		return nil
	}

	pids := make([]int, 0, len(records))
	for _, r := range records {
		pids = append(pids, r.PID)
	}
	alive := session.PidsAlive(pids)
	clients := readClients() // name -> target lookup for real box IPs

	var out []agentSession
	for _, r := range records {
		if !alive[r.PID] {
			_ = session.Remove(r.PID) // prune stale crash leftovers
			continue
		}
		out = append(out, sessionFromRecord(r, clients))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out
}

// sessionFromRecord maps a stored session record onto a display session,
// resolving a remote client's real box IP from clients.yaml (the SSH_CONNECTION
// IP is the tunnel's localhost, which is meaningless).
func sessionFromRecord(r session.Record, clients []clientRow) agentSession {
	s := agentSession{
		Provider: r.Provider,
		PID:      r.PID,
		Remote:   r.Source == "ssh-remote",
		Host:     r.RemoteHost,
		OS:       r.RemoteOS,
	}
	if s.Remote {
		if ip := remoteIPForHost(r.RemoteHost, clients); ip != "" {
			s.IP = ip
		} else {
			s.IP = r.RemoteIP
		}
	}
	if s.Provider == "" {
		s.Provider = "claude"
	}
	return s
}

// remoteIPForHost resolves a remote client's real box IP from clients.yaml.
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
