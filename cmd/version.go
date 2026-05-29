package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version number of auxly-cli",
	Run: func(cmd *cobra.Command, args []string) {
		purple := "\033[38;5;134m"
		bold := "\033[1m"
		reset := "\033[0m"

		var sb strings.Builder
		sb.WriteString("\r\n")
		sb.WriteString(fmt.Sprintf("%s🧠 Auxly-Memory CLI Version:%s %s1.0.0%s\r\n", bold+purple, reset, bold, reset))
		sb.WriteString("   ↳ Platform: macOS/Linux/Windows stdio-native\r\n")
		sb.WriteString("   ↳ Revision: release-v1.0.0\r\n\r\n")
		fmt.Print(sb.String())
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
