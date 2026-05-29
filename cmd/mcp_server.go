package cmd

import (
	"os"

	"github.com/Tzamun-Arabia-IT-Co/auxly-cli/mcp"
	"github.com/spf13/cobra"
)

var (
	mcpProvider   string
	mcpSource     string
	mcpRemoteOS   string
	mcpRemoteHost string
)

var mcpServerCmd = &cobra.Command{
	Use:   "mcp-server",
	Short: "Start the MCP server (stdio JSON-RPC for Claude Desktop, etc.)",
	Long: `Starts an MCP (Model Context Protocol) server over stdio.
Add this to your claude_desktop_config.json to give Claude Desktop access to your memory.`,
	RunE: runMCPServer,
}

func init() {
	flags := mcpServerCmd.Flags()
	flags.StringVar(&mcpProvider, "provider", "", "provider id (cursor/claude-code/gemini/…)")
	flags.StringVar(&mcpSource, "source", "local", "attribution source: \"local\" or \"ssh-remote\"")
	flags.StringVar(&mcpRemoteOS, "remote-os", "", "remote OS (set by the SSH launcher)")
	flags.StringVar(&mcpRemoteHost, "remote-host", "", "remote hostname (set by the SSH launcher)")

	rootCmd.AddCommand(mcpServerCmd)
}

func runMCPServer(cmd *cobra.Command, args []string) error {
	// Bridge attribution flags into environment variables consumed host-side
	// by mcp/server.go. SSH does not forward env vars, so the launcher passes
	// them as flags and we re-expose them here. These env var names are a hard contract.
	if mcpProvider != "" {
		os.Setenv("AUXLY_PROVIDER", mcpProvider)
	}
	os.Setenv("AUXLY_SOURCE", mcpSource)
	if mcpRemoteOS != "" {
		os.Setenv("AUXLY_REMOTE_OS", mcpRemoteOS)
	}
	if mcpRemoteHost != "" {
		os.Setenv("AUXLY_REMOTE_HOST", mcpRemoteHost)
	}

	server := mcp.NewServer(getMemoryPath())
	return server.Run()
}
