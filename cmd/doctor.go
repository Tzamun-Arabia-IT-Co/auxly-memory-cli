package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/detect"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/pending"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/trust"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/update"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/tui"
	"github.com/spf13/cobra"
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "One-screen health check of your Auxly install",
	// Diagnostic: always exits 0 — problems are named in the output, and a
	// broken setup shouldn't ALSO fail the very command that explains it.
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Print(doctorReport(getMemoryPath()))
		return nil
	},
}

func init() {
	rootCmd.AddCommand(doctorCmd)
}

// doctorReport builds the full health report for the given memory root. Pure
// function of on-disk state — testable without a cobra invocation.
func doctorReport(memPath string) string {
	var b strings.Builder
	line := func(mark, text, hint string) {
		b.WriteString("   " + mark + " " + text + "\n")
		if hint != "" {
			b.WriteString("       ↳ " + hint + "\n")
		}
	}

	b.WriteString("🩺 Auxly doctor\n")

	// 1. Initialization
	initialized := tui.IsInitialized(memPath)
	if initialized {
		line("✓", "memory initialized", "")
	} else {
		line("✗", "memory NOT initialized", "run `auxly init` — creates the vault and wires your agents")
	}

	// 2. Vault
	store := memory.NewStore(memPath)
	files, err := store.List()
	if err != nil || len(files) == 0 {
		line("✗", fmt.Sprintf("vault empty or unreadable at %s", memPath), "run `auxly init` (or check --path)")
	} else {
		var total int64
		var newest time.Time
		for _, f := range files {
			total += f.Size
			if f.ModTime.After(newest) {
				newest = f.ModTime
			}
		}
		line("✓", fmt.Sprintf("vault: %s — %d files, %.1f KB, last write %s",
			memPath, len(files), float64(total)/1024, newest.Format("2006-01-02 15:04")), "")

		// 3. Pending approvals
		if pendings, perr := pending.NewManager(memPath).List(); perr != nil {
			line("✗", fmt.Sprintf("pending queue unreadable: %v", perr), "check permissions on "+filepath.Join(memPath, ".pending"))
		} else if n := len(pendings); n > 0 {
			line("⚠", fmt.Sprintf("%d pending approval(s) waiting", n), "review with `auxly approve <name>` / dashboard tab 4")
		} else {
			line("✓", "no pending approvals", "")
		}

		// 4. Semantic index freshness. The DB runs in WAL mode, so recent writes
		// live in the -wal sidecar until a checkpoint — freshness must consider
		// the newest of db/-wal/-shm, not just the main file.
		idxBase := filepath.Join(memPath, ".index", "embeddings.db")
		var idxNewest time.Time
		idxExists := false
		for _, suffix := range []string{"", "-wal", "-shm"} {
			if st, ierr := os.Stat(idxBase + suffix); ierr == nil {
				idxExists = true
				if st.ModTime().After(idxNewest) {
					idxNewest = st.ModTime()
				}
			}
		}
		switch {
		case !idxExists:
			line("⚠", "semantic index not built — recall falls back to substring search", "run any recall once (agents build it), or `auxly index rebuild`")
		case newest.After(idxNewest):
			line("⚠", "semantic index stale (vault changed since last index)", "next recall refreshes it automatically")
		default:
			line("✓", "semantic index fresh", "")
		}
	}

	// 5. Agents + MCP wiring
	home, _ := os.UserHomeDir()
	agents := detect.InstalledAgents()
	if len(agents) == 0 {
		line("⚠", "no AI agents detected", "install Claude Code / Cursor / Codex / Gemini CLI, then `auxly setup`")
	} else {
		wired := mcpWiredProviders(home)
		for _, a := range agents {
			switch {
			// Shell-only first: no MCP config of its own — a sibling agent sharing
			// the provider must never make it read as "wired".
			case a.Connection == "Shell":
				line("✓", fmt.Sprintf("%s — shell integration (no MCP config)", a.Name), "")
			case a.Provider == "perplexity":
				line("⚠", fmt.Sprintf("%s — manual wiring (Connectors UI)", a.Name), "`auxly setup` prints the exact connector command")
			case providerWired(wired, a.Provider):
				line("✓", fmt.Sprintf("%s — MCP wired", a.Name), "")
			default:
				line("✗", fmt.Sprintf("%s — NOT wired to Auxly", a.Name), "run `auxly setup`")
			}
		}
	}

	// 6. Trust levels
	if cfg, terr := trust.Load(memPath); terr != nil {
		line("✗", fmt.Sprintf("trust config unreadable: %v", terr), "check "+filepath.Join(memPath, "trust.yaml"))
	} else if len(cfg.Providers) == 0 {
		line("✓", fmt.Sprintf("trust: default %q for all agents", cfg.Default), "")
	} else {
		parts := make([]string, 0, len(cfg.Providers))
		for p, pc := range cfg.Providers {
			parts = append(parts, p+"="+pc.TrustLevel)
		}
		sort.Strings(parts) // map order is random — keep the report deterministic
		line("✓", fmt.Sprintf("trust: default %q · %s", cfg.Default, strings.Join(parts, " · ")), "")
	}

	// 7. Version. Cached() is network-free and returns ("", false) when no
	// check has ever run — that's "unknown", not "up to date".
	latest, newer := update.Cached()
	switch {
	case newer:
		line("⚠", fmt.Sprintf("auxly %s — %s is available", update.Current, latest), "run `auxly update`")
	case latest == "":
		line("⚠", "auxly "+update.Current+" — update status unknown (no check has run yet)", "run `auxly update` to check")
	default:
		line("✓", "auxly "+update.Current+" — up to date", "")
	}

	return b.String()
}
