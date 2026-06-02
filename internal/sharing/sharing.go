// Package sharing implements the per-remote memory file-sharing ACL.
//
// A host decides which of its memory files each connecting remote (consumer) may
// READ and whether it may WRITE. The model is FAIL-CLOSED: personal-tier files
// are never served to a remote unless the host explicitly lists them, and an
// unknown/unmatched remote gets the safe default (all shared-tier files, no
// personal, read-only).
package sharing

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	"gopkg.in/yaml.v3"
)

const (
	AccessRead  = "read"
	AccessWrite = "write"
)

// unifiedDump is a generated aggregate that can embed personal facts; it is never
// served to a remote by default (only via an explicit SharedFiles grant).
const unifiedDump = "unified_memory.md"

// ClientShare is one remote's file-sharing ACL, persisted per client.
type ClientShare struct {
	// SharedFiles is an explicit allow-list of files this remote may read.
	// Empty/nil means "use the safe default" (all non-personal files).
	SharedFiles []string `yaml:"shared_files,omitempty"`
	// WriteFiles is the per-file writable subset (each must also be in the
	// readable set). When non-empty it is AUTHORITATIVE: writes are allowed only
	// to files it names, regardless of Access. Empty/nil falls back to the legacy
	// global Access flag so older configs keep working.
	WriteFiles []string `yaml:"write_files,omitempty"`
	// Access is the legacy global toggle: "read" (default) or "write". Superseded
	// by WriteFiles for new per-file grants; retained for back-compat.
	Access string `yaml:"access,omitempty"`
}

// effectiveAccess defaults to read-only.
func (c *ClientShare) effectiveAccess() string {
	if c != nil && c.Access == AccessWrite {
		return AccessWrite
	}
	return AccessRead
}

// AllowedReads returns the set of files a remote may READ, fail-closed.
//
//   - Explicit SharedFiles → exactly that set.
//   - No config → every vault file EXCEPT personal-tier files and the unified
//     aggregate dump (which can contain personal facts).
//
// allVaultFiles is the full list of files present in the vault.
func AllowedReads(share *ClientShare, allVaultFiles []string) map[string]bool {
	allowed := map[string]bool{}
	if share != nil && len(share.SharedFiles) > 0 {
		for _, f := range share.SharedFiles {
			allowed[f] = true
		}
		return allowed
	}
	for _, f := range allVaultFiles {
		if memory.IsPersonalFile(f) || f == unifiedDump {
			continue // fail-closed: never default-share personal or the aggregate
		}
		allowed[f] = true
	}
	return allowed
}

// CanRead reports whether a remote may read a specific file.
func CanRead(share *ClientShare, file string, allVaultFiles []string) bool {
	return AllowedReads(share, allVaultFiles)[file]
}

// CanWrite reports whether a remote may write a specific file. A file must first
// be in the readable set (you cannot write what you cannot see). When the per-file
// WriteFiles set is present it is authoritative — only its members are writable;
// otherwise the legacy global Access flag governs. Personal files require an
// explicit read grant to be writable at all (and the MCP layer hard-blocks them
// regardless).
func CanWrite(share *ClientShare, file string, allVaultFiles []string) bool {
	if !AllowedReads(share, allVaultFiles)[file] {
		return false
	}
	if share != nil && len(share.WriteFiles) > 0 {
		for _, f := range share.WriteFiles {
			if f == file {
				return true
			}
		}
		return false
	}
	return share.effectiveAccess() == AccessWrite
}

// --- persistence: read the host's per-client shares from clients.yaml ---

// clientsFile mirrors only the fields this package needs from ~/.auxly/clients.yaml.
// The authoritative writer lives in package cmd; we read the same shape.
type clientsFile struct {
	Clients []clientEntry `yaml:"clients"`
}

type clientEntry struct {
	Name   string `yaml:"name"`
	Target string `yaml:"target"`
	// Hostname is the box's self-reported hostname, captured at provision time. A
	// box keyed by IP in Target (e.g. root@192.168.1.24) reports a DIFFERENT string
	// as its session RemoteHost (e.g. "auxly.tzamun.dev"), because the SSH launcher
	// passes `--remote-host localHostname()`. Matching must consider this field, not
	// just Target — otherwise the ACL never loads and write grants are dropped.
	Hostname    string   `yaml:"hostname,omitempty"`
	SharedFiles []string `yaml:"shared_files,omitempty"`
	WriteFiles  []string `yaml:"write_files,omitempty"`
	Access      string   `yaml:"access,omitempty"`
}

// LoadForRemoteHost looks up the ClientShare for a connecting remote, matched by
// hostname against the host's clients.yaml Target field. Returns nil when no
// match is found — callers then apply the safe default via AllowedReads(nil, …).
func LoadForRemoteHost(memoryPath, remoteHost string) *ClientShare {
	if remoteHost == "" {
		return nil
	}
	path := clientsYamlPath(memoryPath)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var cf clientsFile
	if yaml.Unmarshal(data, &cf) != nil {
		return nil
	}
	for _, c := range cf.Clients {
		if clientMatches(c, remoteHost) {
			return &ClientShare{SharedFiles: c.SharedFiles, WriteFiles: c.WriteFiles, Access: c.Access}
		}
	}
	return nil
}

// clientMatches reports whether a clients.yaml entry refers to the connecting
// remote, matched by friendly name, the box's self-reported hostname, OR the host
// part of its target — all case-insensitively. A relay box reports its own hostname
// as RemoteHost (the SSH launcher passes `--remote-host localHostname()`), stored in
// the `hostname` field, NOT the IP-keyed target; matching target alone silently
// dropped per-file write grants. Mirrors clientIsLive in package cmd so sharing and
// live-status agree on which box a session belongs to.
func clientMatches(c clientEntry, remoteHost string) bool {
	if remoteHost == "" {
		return false
	}
	if c.Name != "" && strings.EqualFold(c.Name, remoteHost) {
		return true
	}
	if c.Hostname != "" && strings.EqualFold(c.Hostname, remoteHost) {
		return true
	}
	return hostMatches(c.Target, remoteHost)
}

// clientsYamlPath resolves ~/.auxly/clients.yaml from the memory path
// (~/.auxly/memory) by taking its parent directory.
func clientsYamlPath(memoryPath string) string {
	return filepath.Join(filepath.Dir(memoryPath), "clients.yaml")
}

// hostMatches reports whether a clients.yaml target (e.g. "root@1.2.3.4:22")
// refers to the given remote host (hostname or IP).
func hostMatches(target, remoteHost string) bool {
	if target == "" || remoteHost == "" {
		return false
	}
	host := target
	if at := lastIndexByte(host, '@'); at >= 0 {
		host = host[at+1:]
	}
	if colon := indexByte(host, ':'); colon >= 0 {
		host = host[:colon]
	}
	return strings.EqualFold(host, remoteHost)
}

func lastIndexByte(s string, b byte) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == b {
			return i
		}
	}
	return -1
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
