package cmd

import (
	"fmt"
	"strings"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/update"
	"github.com/spf13/cobra"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version number of auxly-cli",
	Run: func(cmd *cobra.Command, args []string) {
		purple := "\033[38;5;134m"
		bold := "\033[1m"
		reset := "\033[0m"

		// update.Current is injected at build time via -ldflags (goreleaser /
		// Makefile), so the printed version always matches the actual release.
		ver := update.Current

		var sb strings.Builder
		sb.WriteString("\r\n")
		sb.WriteString(fmt.Sprintf("%s🧠 Auxly-Memory CLI Version:%s %s%s%s\r\n", bold+purple, reset, bold, ver, reset))
		sb.WriteString("   ↳ Platform: macOS/Linux/Windows stdio-native\r\n")
		sb.WriteString(fmt.Sprintf("   ↳ Revision: release-v%s\r\n\r\n", ver))
		fmt.Print(sb.String())
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
