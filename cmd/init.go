package cmd

import (
	"github.com/Tzamun-Arabia-IT-Co/auxly-cli/tui"
	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize memory folder with default templates",
	Long:  `Creates the memory folder structure with all default .md templates, trust.yaml, and git.yaml.`,
	RunE:  runInit,
}

func init() {
	rootCmd.AddCommand(initCmd)
}

func runInit(cmd *cobra.Command, args []string) error {
	memPath := getMemoryPath()

	// Automatically check and install missing dependencies (Node.js)
	checkAndInstallDependencies()

	tui.RunWizard(memPath)
	return nil
}
