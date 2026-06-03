package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/detect"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/profiler"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/templates"
	"github.com/spf13/cobra"
)

var populateCmd = &cobra.Command{
	Use:   "populate",
	Short: "Auto-detect system profile and populate memory files",
	Long:  `Runs system profiler to auto-detect identity, tools, infrastructure, agents, and projects. Writes the results to memory files.`,
	RunE:  runPopulate,
}

func init() {
	rootCmd.AddCommand(populateCmd)
}

func runPopulate(cmd *cobra.Command, args []string) error {
	memPath := getMemoryPath()

	if err := os.MkdirAll(memPath, 0755); err != nil {
		return err
	}
	os.MkdirAll(filepath.Join(memPath, ".pending"), 0755)

	// Auto-detect
	prof := profiler.Detect()
	prof.Agents = detect.InstalledAgents()

	// Write auto-detected files
	autoFiles := []struct {
		name    string
		content string
	}{
		{"identity.md", prof.RenderIdentityMD()},
		{"preferences.md", prof.RenderPreferencesMD()},
		{"infra.md", prof.RenderInfraMD()},
		{"agents.md", prof.RenderAgentsMD()},
		{"products.md", prof.RenderProductsMD()},
	}

	for _, af := range autoFiles {
		destPath := filepath.Join(memPath, af.name)
		if err := os.WriteFile(destPath, []byte(af.content), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "  ✗ %s: %v\n", af.name, err)
			continue
		}
		fmt.Printf("  ✓ %s (%d bytes)\n", af.name, len(af.content))
	}

	// Copy templates that don't exist yet
	entries, err := templates.FS.ReadDir(".")
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() || entry.Name() == "embed.go" {
				continue
			}
			destPath := filepath.Join(memPath, entry.Name())
			if _, err := os.Stat(destPath); err == nil {
				continue
			}
			data, err := templates.FS.ReadFile(entry.Name())
			if err != nil {
				continue
			}
			os.WriteFile(destPath, data, 0644)
			fmt.Printf("  ✓ %s (template)\n", entry.Name())
		}
	}

	// Audit log
	auditPath := filepath.Join(memPath, ".audit.log")
	if _, err := os.Stat(auditPath); os.IsNotExist(err) {
		os.WriteFile(auditPath, []byte{}, 0644)
	}

	// Marker
	markerPath := filepath.Join(memPath, ".initialized")
	os.WriteFile(markerPath, []byte("populated"), 0644)

	fmt.Printf("\n✅ Memory populated at %s\n", memPath)
	return nil
}
