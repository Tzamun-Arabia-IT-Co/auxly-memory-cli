package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/audit"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/git"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/pending"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/trust"
	"github.com/spf13/cobra"
)

var (
	writeAgent    string
	writeProvider string
	writeFile     string
	writeDiff     string
	writeReason   string
)

var writeCmd = &cobra.Command{
	Use:   "write",
	Short: "Write a change to memory (respects trust levels)",
	Long: `Write a change to memory. Based on the provider's trust level:
  - auto: writes directly to memory/
  - require_approval: writes to memory/.pending/
  - read_only: rejected`,
	RunE: runWrite,
}

func init() {
	writeCmd.Flags().StringVar(&writeAgent, "agent", "", "Agent ID (required)")
	writeCmd.Flags().StringVar(&writeProvider, "provider", "", "Provider name (required)")
	writeCmd.Flags().StringVar(&writeFile, "file", "", "Target file path relative to memory/ (required)")
	writeCmd.Flags().StringVar(&writeDiff, "diff", "", "Unified diff or content snippet (required)")
	writeCmd.Flags().StringVar(&writeReason, "reason", "", "Reason for the change (required)")
	writeCmd.MarkFlagRequired("agent")
	writeCmd.MarkFlagRequired("provider")
	writeCmd.MarkFlagRequired("file")
	writeCmd.MarkFlagRequired("diff")
	writeCmd.MarkFlagRequired("reason")
	rootCmd.AddCommand(writeCmd)
}

func runWrite(cmd *cobra.Command, args []string) error {
	memPath := getMemoryPath()

	// Load trust config
	trustCfg, err := trust.Load(memPath)
	if err != nil {
		return fmt.Errorf("failed to load trust config: %w", err)
	}

	level := trustCfg.GetTrustLevel(writeProvider)

	// Check if read-only
	if level == trust.LevelReadOnly {
		fmt.Printf("❌ Provider '%s' is read_only. Write rejected.\n", writeProvider)
		return nil
	}

	// Initialize audit logger
	logger, err := audit.NewLogger(memPath)
	if err != nil {
		return fmt.Errorf("failed to init audit: %w", err)
	}
	defer logger.Close()

	if level == trust.LevelRequireApproval {
		// Write to .pending/
		mgr := pending.NewManager(memPath)
		pendingName, err := mgr.Write(writeFile, writeDiff)
		if err != nil {
			return fmt.Errorf("failed to write pending: %w", err)
		}

		// Log audit entry
		logger.Log(writeAgent, writeProvider, "write", writeFile, writeDiff, writeReason, level)

		fmt.Printf("⏳ Change queued for approval: .pending/%s\n", pendingName)
		fmt.Println("   Run 'auxly approve' to accept or 'auxly reject' to discard.")
		return nil
	}

	// Auto trust: write directly
	cleanedRel := filepath.Clean(writeFile)
	if filepath.IsAbs(cleanedRel) || strings.HasPrefix(cleanedRel, "..") {
		return fmt.Errorf("access denied: path traversal attempt for '%s'", writeFile)
	}
	targetPath := filepath.Join(memPath, cleanedRel)

	// Double check boundaries
	relBoundary, err := filepath.Rel(memPath, targetPath)
	if err != nil || strings.HasPrefix(relBoundary, "..") {
		return fmt.Errorf("access denied: path escapes memory boundary")
	}

	dir := filepath.Dir(targetPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Apply diff using the centralized ApplyDiff helper
	var existing string
	if data, err := os.ReadFile(targetPath); err == nil {
		existing = string(data)
	}
	content := pending.ApplyDiff(existing, writeDiff)

	// Update "Last Updated" field
	content = updateLastUpdated(content)

	if err := os.WriteFile(targetPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	// Log audit entry
	logger.Log(writeAgent, writeProvider, "write", writeFile, writeDiff, writeReason, level)

	// Auto-commit if configured
	gitCfg, _ := git.LoadConfig(memPath)
	if gitCfg != nil && gitCfg.AutoCommit {
		git.AutoCommit(memPath, writeFile, writeReason)
	}

	fmt.Printf("✅ Written to %s\n", writeFile)
	return nil
}

func updateLastUpdated(content string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "(Autofilled by auxly-cli)") || strings.HasPrefix(line, "## Last Updated") {
			if strings.HasPrefix(line, "## Last Updated") && i+1 < len(lines) {
				lines[i+1] = time.Now().UTC().Format(time.RFC3339)
			}
		}
	}
	return strings.Join(lines, "\n")
}
