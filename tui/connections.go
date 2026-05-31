package tui

import (
	"sort"
	"strings"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/session"
)

// agentSession is a live MCP connection to this memory host, read from the
// session registry (~/.auxly/sessions) that each running `auxly mcp-server`
// maintains. Each live record is one connected agent session.
type agentSession struct {
	Provider     string // claude, claude-code, cursor, ... (authoritative)
	Remote       bool   // true when served to an SSH-remote client
	PID          int    // OS process id (the live server) — shown for local sessions
	Host         string // remote client hostname (remote only)
	IP           string // remote client IP — real box IP from clients.yaml (remote only)
	OS           string // remote client OS (remote only)
	Unregistered bool   // live server that never wrote a session record (older
	// build / pre-registry version skew); provider is inferred from ancestry
}

// gatherSessions returns every live MCP session. It reconciles two sources of
// truth so the dashboard is correct regardless of what any individual server
// wrote:
//
//  1. The session registry — records each server self-writes (authoritative
//     provider + source). Records whose process has died are pruned here.
//  2. The live process list — every running `auxly mcp-server`. A server that
//     is alive but has no registry record (an older build that predates the
//     registry, or version skew mid auto-update) is still surfaced, with its
//     provider inferred from process ancestry and flagged Unregistered.
//
// Without (2), a connected agent running a stale binary shows as idle on the
// dashboard even while its writes land in the activity log — the exact
// confusion this reconciliation removes.
func gatherSessions() []agentSession {
	records := session.List()
	clients := readClients() // name -> target lookup for real box IPs

	registeredPIDs := make([]int, 0, len(records))
	for _, r := range records {
		registeredPIDs = append(registeredPIDs, r.PID)
	}
	alive := session.PidsAlive(registeredPIDs)

	registered := make(map[int]bool, len(records))
	var out []agentSession
	for _, r := range records {
		if !alive[r.PID] {
			_ = session.Remove(r.PID) // prune stale crash leftovers
			continue
		}
		registered[r.PID] = true
		out = append(out, sessionFromRecord(r, clients))
	}

	// Surface live servers that never registered (version skew / stale binary).
	for _, pid := range session.LiveServerPIDs() {
		if registered[pid] {
			continue
		}
		provider := session.InferProvider(session.AncestorCommands(pid))
		if provider == "" {
			provider = "claude" // matches the server's own resolveProvider default
		}
		out = append(out, agentSession{Provider: provider, PID: pid, Unregistered: true})
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

// boxIsLive reports whether a configured client box currently holds a live
// SSH-remote session, matching the session's RemoteHost against the box name or
// the host part of its target. Best-effort: an unmatched box reads as idle
// rather than falsely "connected".
func boxIsLive(live map[string]bool, c clientRow) bool {
	if c.Name != "" && live[strings.ToLower(c.Name)] {
		return true
	}
	if c.Hostname != "" && live[strings.ToLower(c.Hostname)] {
		return true
	}
	t := targetHost(c.Target)
	return t != "" && live[strings.ToLower(t)]
}

// targetHost extracts the bare host from a "[user@]host[:port]" target.
func targetHost(target string) string {
	t := target
	if at := strings.LastIndex(t, "@"); at >= 0 {
		t = t[at+1:]
	}
	if i := strings.IndexByte(t, ':'); i >= 0 {
		t = t[:i]
	}
	return t
}

// remoteIPForHost resolves a remote client's real box IP from clients.yaml,
// matching the session hostname against the box name or its captured hostname.
func remoteIPForHost(host string, clients []clientRow) string {
	for _, c := range clients {
		if !strings.EqualFold(c.Name, host) && !strings.EqualFold(c.Hostname, host) {
			continue
		}
		if h := targetHost(c.Target); h != "" {
			return h
		}
	}
	return ""
}
