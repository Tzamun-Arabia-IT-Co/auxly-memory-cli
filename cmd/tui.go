package cmd

import (
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/tui"
	"github.com/spf13/cobra"
)

var tuiCmd = &cobra.Command{
	Use:   "ui",
	Short: "Launch the interactive TUI dashboard",
	Run: func(cmd *cobra.Command, args []string) {
		tui.Run(getMemoryPath())
	},
}

func init() {
	rootCmd.AddCommand(tuiCmd)
}
