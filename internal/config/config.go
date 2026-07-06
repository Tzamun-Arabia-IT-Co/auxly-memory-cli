package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const (
	EnvMemoryPath = "AUXLY_MEMORY_PATH"
	DefaultDir    = ".auxly"
	MemoryDir     = "memory"
	SettingsFile  = "settings.json"
)

// ResolveMemoryPath returns the effective memory folder path.
// Priority: AUXLY_MEMORY_PATH env > ~/.auxly/memory/
func ResolveMemoryPath() string {
	if envPath := os.Getenv(EnvMemoryPath); envPath != "" {
		return envPath
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, DefaultDir, MemoryDir)
}

// Settings holds persisted user preferences not derivable from the environment.
// It lives at ~/.auxly/settings.json. Fields default to their zero value so a
// missing file behaves like all-defaults.
type Settings struct {
	// LiveUsage opts into the dashboard usage panel, which calls each agent's
	// provider with its stored login token. Off by default to preserve Auxly's
	// local-first, zero-network behavior.
	LiveUsage bool `json:"liveUsage"`

	// AutoUpdate opts into self-updating to the latest published release. When on,
	// `auxly` applies an available update IN PLACE after an interactive command
	// finishes (never mid-session, never on the hot statusline path), so the next
	// launch runs the new binary. Off by default — updates otherwise only notify and
	// wait for a manual `auxly update`. Enable it on machines (e.g. remotes) you want
	// to keep current automatically.
	AutoUpdate bool `json:"autoUpdate,omitempty"`

	// UpdateRemotesOnConnect opts into keeping the REMOTE current as part of the
	// connect flow: when this machine connects to a host and finds an older auxly
	// there, it bumps it in place over the same SSH (and ensures the remote's
	// statusline), skipping a host that's serving a live session. Off by default —
	// connect otherwise only verifies auxly is present, never mutating the remote
	// binary. The `--update-remote` flag overrides this per invocation.
	UpdateRemotesOnConnect bool `json:"updateRemotesOnConnect,omitempty"`

	// DefaultRemoteWrite flips the per-remote sharing default from read-only to
	// read+write for KNOWN clients (those listed in clients.yaml) that have no
	// explicit per-file write grant. Off by default — the sharing model stays
	// fail-closed/read-only for everyone else, and an UNMATCHED/unknown remote is
	// never granted write by this flag. An explicit per-file WriteFiles grant always
	// takes precedence. Enable it when you trust every box you connect (e.g. your
	// own fleet) and want write access without per-box setup.
	DefaultRemoteWrite bool `json:"defaultRemoteWrite,omitempty"`

	// SyncStatuslineToRemotes is the master switch for carrying THIS machine's
	// statusline preference to connected boxes: when on, applying a statusline change
	// in Settings → Customizations also pushes it to the selected boxes
	// (StatuslineSyncBoxes) over SSH. Off by default. A manual "sync now" works
	// regardless of this flag.
	SyncStatuslineToRemotes bool `json:"syncStatuslineToRemotes,omitempty"`

	// StatuslineSyncBoxes is the set of connected-box names selected for statusline
	// sync (by name, matching clients.yaml). Empty means no box is selected. Used by
	// both the auto-sync (above) and the "sync selected" action.
	StatuslineSyncBoxes []string `json:"statuslineSyncBoxes,omitempty"`

	// HiddenAgents lists canonical provider/brand ids the user has chosen to hide
	// from the dashboard grid (Settings → Agents). Empty (the default) shows every
	// detected or active agent. Hiding only affects the dashboard display — it
	// never stops an agent from connecting or writing.
	HiddenAgents []string `json:"hiddenAgents,omitempty"`

	// EmbedEndpoint persists the embeddings API URL (OpenAI-compatible
	// /v1/embeddings) so semantic recall reaches a remote embedder even when
	// the auxly MCP server — spawned by an agent — has none of the user's
	// shell env. Env AUXLY_EMBED_ENDPOINT still takes precedence.
	EmbedEndpoint string `json:"embedEndpoint,omitempty"`

	// EmbedModel persists the embedding model name. Env AUXLY_EMBED_MODEL wins.
	EmbedModel string `json:"embedModel,omitempty"`

	// EmbedAllowCloud persists the opt-in to send vault text to a non-local
	// embeddings host (a public IP/hostname). Env AUXLY_EMBED_ALLOW_CLOUD=1
	// also enables it. Off by default — local/LAN endpoints never need it.
	EmbedAllowCloud bool `json:"embedAllowCloud,omitempty"`
}

// settingsPath returns ~/.auxly/settings.json.
func settingsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, DefaultDir, SettingsFile)
}

// LoadSettings reads persisted settings, returning defaults if the file is
// missing or unreadable (never errors — settings are best-effort).
func LoadSettings() Settings {
	var s Settings
	b, err := os.ReadFile(settingsPath())
	if err != nil {
		return s
	}
	_ = json.Unmarshal(b, &s)
	return s
}

// SaveSettings persists settings to ~/.auxly/settings.json (mode 0600).
func SaveSettings(s Settings) error {
	if err := os.MkdirAll(filepath.Dir(settingsPath()), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(settingsPath(), b, 0o600)
}
