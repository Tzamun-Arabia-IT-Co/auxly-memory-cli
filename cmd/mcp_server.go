package cmd

import (
	"github.com/Tzamun-Arabia-IT-Co/auxly-cli/mcp"
	"github.com/spf13/cobra"
)

var mcpServerCmd = &cobra.Command{
	Use:   "mcp-server",
	Short: "Start the MCP server (stdio JSON-RPC for Claude Desktop, etc.)",
	Long: `Starts an MCP (Model Context Protocol) server over stdio.
Add this to your claude_desktop_config.json to give Claude Desktop access to your memory.`,
	RunE: runMCPServer,
}

func init() {
	rootCmd.AddCommand(mcpServerCmd)
}

func runMCPServer(cmd *cobra.Command, args []string) error {
	server := mcp.NewServer(getMemoryPath())
	return server.Run()
}
