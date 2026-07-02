package cmd

import (
	"fmt"
	"sort"
	"strings"
)

// hostTopologyWarnings analyzes the host's relay + client config for the
// duplicate/orphan classes from the July 2026 incident review: two client rows
// that are really the same machine, and relays no client references. Pure
// config analysis — never touches the network, never auto-removes anything
// (wiring self-heals; topology decisions stay with the human).
func hostTopologyWarnings(relays []hostConfig) []string {
	var warnings []string
	clients, _ := loadClients()

	// (f) duplicate machines: two entries sharing a self-reported hostname.
	byHostname := map[string][]string{}
	for _, c := range clients {
		if h := strings.ToLower(strings.TrimSpace(c.Hostname)); h != "" {
			byHostname[h] = append(byHostname[h], c.Name)
		}
	}
	var dupKeys []string
	for h, names := range byHostname {
		if len(names) > 1 {
			dupKeys = append(dupKeys, h)
		}
	}
	sort.Strings(dupKeys)
	for _, h := range dupKeys {
		names := byHostname[h]
		sort.Strings(names)
		warnings = append(warnings, fmt.Sprintf("connections %s are the same machine (hostname %q) — likely a duplicate route", strings.Join(names, " + "), h))
	}

	// (g) orphan relays: a relay whose rendezvous host no client targets.
	for _, r := range relays {
		_, rhost, _, err := parseHostSpec(r.Rendezvous)
		if err != nil || rhost == "" {
			continue
		}
		referenced := false
		for _, c := range clients {
			if strings.Contains(strings.ToLower(c.Target), strings.ToLower(rhost)) {
				referenced = true
				break
			}
		}
		if !referenced {
			warnings = append(warnings, fmt.Sprintf("relay %s has no connected box referencing it — orphan tunnel serving nobody", r.Rendezvous))
		}
	}
	return warnings
}

// warnIfKnownMachine flags a provision target that is already connected under
// another name (same self-reported hostname, different route/IP) — the user
// may be adding a duplicate rather than a new box. Warning only; a second
// route is sometimes intentional.
func warnIfKnownMachine(boxHostname, newName string) {
	if strings.TrimSpace(boxHostname) == "" {
		return
	}
	clients, _ := loadClients()
	for _, c := range clients {
		if strings.EqualFold(c.Hostname, boxHostname) && !strings.EqualFold(c.Name, newName) {
			fmt.Printf("   ⚠ this machine is already connected as %q (hostname %s) — adding a second route to the same box\n", c.Name, boxHostname)
			return
		}
	}
}
