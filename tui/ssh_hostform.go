package tui

import (
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strings"
)

// privateHostTUI reports whether a [user@]host[:port] spec points at a
// private/LAN address — the wizard's "direct" method resolves to lan vs public
// with this, so the user never has to answer a networking question.
func privateHostTUI(spec string) bool {
	host := spec
	if at := strings.LastIndex(host, "@"); at >= 0 {
		host = host[at+1:]
	}
	if strings.HasPrefix(host, "[") {
		// "[v6addr]:port" — the address is exactly the bracketed span.
		if end := strings.Index(host, "]"); end > 0 {
			host = host[1:end]
		}
	} else if colon := strings.LastIndex(host, ":"); colon >= 0 && strings.Count(host, ":") == 1 {
		host = host[:colon]
	}
	if strings.HasSuffix(strings.ToLower(host), ".local") {
		return true // mDNS names are LAN by construction
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false // bare DNS name — assume public reachability rules
	}
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()
}

// sshConfigHosts returns the concrete Host aliases from ~/.ssh/config
// (patterns and negations skipped) — the wizard's Tab-completion source.
func sshConfigHosts() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(home, ".ssh", "config"))
	if err != nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.EqualFold(fields[0], "Host") {
			continue
		}
		for _, h := range fields[1:] {
			if strings.ContainsAny(h, "*?!") || seen[h] {
				continue
			}
			seen[h] = true
			out = append(out, h)
		}
	}
	return out
}

// completeHostField returns the next ~/.ssh/config alias matching the typed
// prefix (cycling past the current value), or "" when nothing matches.
func completeHostField(current string) string {
	hosts := sshConfigHosts()
	if len(hosts) == 0 {
		return ""
	}
	prefix := current
	// When the field already holds a full suggestion, cycle to the next one.
	for i, h := range hosts {
		if h == current {
			return hosts[(i+1)%len(hosts)]
		}
	}
	var matches []string
	for _, h := range hosts {
		if strings.HasPrefix(h, prefix) {
			matches = append(matches, h)
		}
	}
	if len(matches) == 0 {
		return ""
	}
	return matches[0]
}

// hostFieldHint renders the wizard's inline guidance under the host field:
// the assumed username for a bare host, and the Tab-completion affordance.
func hostFieldHint(current string) string {
	var parts []string
	if !strings.Contains(current, "@") {
		if u, err := user.Current(); err == nil && u.Username != "" {
			parts = append(parts, "user defaults to "+u.Username+"@")
		}
	}
	if hosts := sshConfigHosts(); len(hosts) > 0 {
		show := hosts
		if len(show) > 3 {
			show = show[:3]
		}
		parts = append(parts, "Tab completes: "+strings.Join(show, ", "))
	}
	return strings.Join(parts, "   ·   ")
}
