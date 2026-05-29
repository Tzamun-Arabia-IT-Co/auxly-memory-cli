package cmd

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/Tzamun-Arabia-IT-Co/auxly-cli/tui"
)

var (
	bridgeAddr     string
	bridgeToken    string
	bridgeCompress bool
	setupToken     string
	setupPort      int
)

var bridgeCmd = &cobra.Command{
	Use:   "bridge",
	Short: "Remote stdio-to-TCP bridge relayer for SSH environments",
	Long:  `Acts as an MCP server next to remote IDEs, redirecting all stdio RPC requests over a reverse TCP socket back to the local Mac mini.`,
	RunE:  runBridge,
}

var bridgeSetupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Automatically configure remote IDEs/CLIs with the bridge token",
	RunE:  runBridgeSetup,
}

func init() {
	bridgeCmd.Flags().StringVarP(&bridgeAddr, "addr", "a", "127.0.0.1:7357", "TCP address of the local daemon server")
	bridgeCmd.Flags().StringVarP(&bridgeToken, "token", "t", "", "Authentication token to present to the daemon")
	bridgeCmd.Flags().BoolVarP(&bridgeCompress, "compress", "z", false, "Enable gzip compression (must match server --compress flag)")

	bridgeSetupCmd.Flags().StringVarP(&setupToken, "token", "t", "", "Authentication token to configure")
	bridgeSetupCmd.Flags().IntVarP(&setupPort, "port", "p", 7357, "Port of the local daemon server")
	bridgeCmd.AddCommand(bridgeSetupCmd)

	rootCmd.AddCommand(bridgeCmd)
}

type handshakeSuccess struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int         `json:"id"`
	Result  string      `json:"result"`
}

type handshakeErr struct {
	JSONRPC string `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Error   struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func runBridge(cmd *cobra.Command, args []string) error {
	// 1. Resolve Auth Token
	token := bridgeToken
	if token == "" {
		token = os.Getenv("AUXLY_BRIDGE_TOKEN")
	}

	// Resolve compression flag (also accept env var for IDE MCP configs)
	compress := bridgeCompress
	if os.Getenv("AUXLY_COMPRESS") == "1" {
		compress = true
	} else if !compress {
		compress = tui.ReadCompressFromConfig()
	}

	// 2. Dial local TCP socket
	dialer := net.Dialer{Timeout: 3 * time.Second}
	conn, err := dialer.Dial("tcp", bridgeAddr)
	if err != nil {
		sendBridgeErrorResponse(nil, -32000, 
			fmt.Sprintf("❌ Auxly local daemon is unreachable at %s over the SSH reverse tunnel. "+
				"Please verify that (1) 'auxly server' is running locally, and (2) your SSH reverse port-forwarding (-R 7357:localhost:7357) is active.", 
				bridgeAddr),
		)
		return nil
	}
	defer conn.Close()

	// 3. Perform Handshake silently
	handshake := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      0,
		"method":  "handshake",
		"params": map[string]interface{}{
			"token": token,
		},
	}
	handshakeBytes, _ := json.Marshal(handshake)
	fmt.Fprintf(conn, "%s\n", handshakeBytes)

	// Consume and validate reply
	reader := bufio.NewReader(conn)
	replyBytes, err := reader.ReadBytes('\n')
	if err != nil {
		sendBridgeErrorResponse(nil, -32001, "❌ Handshake failed: Connection closed prematurely by local server.")
		return nil
	}

	var success handshakeSuccess
	if err := json.Unmarshal(replyBytes, &success); err != nil || success.Result != "authorized" {
		// Handshake failed, parse error message if present
		var hErr handshakeErr
		_ = json.Unmarshal(replyBytes, &hErr)
		errMsg := "Handshake rejected: Unauthorized session token."
		if hErr.Error.Message != "" {
			errMsg = fmt.Sprintf("Handshake rejected: %s", hErr.Error.Message)
		}
		sendBridgeErrorResponse(nil, -32001, errMsg)
		return nil
	}

	// 4. Optionally upgrade connection to gzip compression
	var rpcIn io.Reader = conn
	var rpcOut io.Writer = conn

	if compress {
		// Server switches to compressed mode after sending the handshake reply.
		// Bridge must write compressed bytes from this point on.
		gzWriter := gzip.NewWriter(conn)
		defer gzWriter.Close()

		// We must flush after every JSON line — wrap with a flushing writer.
		rpcOut = &autoFlushGzipWriter{gz: gzWriter}

		gzReader, err := gzip.NewReader(conn)
		if err != nil {
			sendBridgeErrorResponse(nil, -32003, "❌ Failed to initialise gzip reader on bridge side.")
			return nil
		}
		defer gzReader.Close()
		rpcIn = gzReader
	}

	// 5. Start Bidirectional Relayer
	doneChan := make(chan struct{})

	// Pipe server responses (possibly decompressed) back to IDE stdout
	go func() {
		_, _ = io.Copy(os.Stdout, rpcIn)
		close(doneChan)
	}()

	// Pipe IDE stdin (possibly compressed) into TCP socket
	go func() {
		_, _ = io.Copy(rpcOut, os.Stdin)
		if tcpConn, ok := conn.(*net.TCPConn); ok {
			_ = tcpConn.CloseWrite() // half-close socket for writing
		}
	}()

	// Wait for the TCP-to-stdout relayer to finish
	<-doneChan
	return nil
}

// autoFlushGzipWriter wraps gzip.Writer and flushes after every Write so
// JSON-RPC lines are not buffered inside the compressor and reach the server promptly.
type autoFlushGzipWriter struct {
	gz *gzip.Writer
}

func (w *autoFlushGzipWriter) Write(p []byte) (int, error) {
	n, err := w.gz.Write(p)
	if err != nil {
		return n, err
	}
	return n, w.gz.Flush()
}

func sendBridgeErrorResponse(id interface{}, code int, message string) {
	resp := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]interface{}{
			"code":    code,
			"message": message,
		},
	}
	data, _ := json.Marshal(resp)
	fmt.Fprintf(os.Stdout, "%s\n", data)
}

func runBridgeSetup(cmd *cobra.Command, args []string) error {
	token := setupToken
	if token == "" {
		token = os.Getenv("AUXLY_BRIDGE_TOKEN")
	}
	if token == "" {
		return fmt.Errorf("please provide a token via --token / -t flag or AUXLY_BRIDGE_TOKEN env variable")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	fmt.Println("🚀 Auxly-Memory Auto-Configuration Wizard")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	// 1. Claude Desktop
	macClaude := filepath.Join(home, "Library/Application Support/Claude/claude_desktop_config.json")
	linuxClaude := filepath.Join(home, ".config/Claude/claude_desktop_config.json")
	updateMCPConfigFile(macClaude, token, setupPort, true)
	updateMCPConfigFile(linuxClaude, token, setupPort, true)

	// 2. Cursor
	macCursor := filepath.Join(home, "Library/Application Support/Cursor/User/globalStorage/co.heron.cursor/mcpServers.json")
	linuxCursor := filepath.Join(home, ".config/Cursor/User/globalStorage/co.heron.cursor/mcpServers.json")
	updateMCPConfigFile(macCursor, token, setupPort, false)
	updateMCPConfigFile(linuxCursor, token, setupPort, false)

	// 3. Trae IDE
	updateMCPConfigFile(filepath.Join(home, ".trae/mcp.json"), token, setupPort, false)

	// 3b. Kimi Code CLI / Desktop App
	updateMCPConfigFile(filepath.Join(home, ".kimi-code/mcp.json"), token, setupPort, false)
	updateMCPConfigFile(filepath.Join(home, ".kimi-code/mcp_config.json"), token, setupPort, false)
	updateMCPConfigFile(filepath.Join(home, ".kimi/mcp.json"), token, setupPort, false)
	updateMCPConfigFile(filepath.Join(home, ".kimi/mcp_config.json"), token, setupPort, false)

	// 4. Claude Code CLI
	updateMCPConfigFile(filepath.Join(home, ".claudecode/mcp.json"), token, setupPort, false)
	updateMCPConfigFile(filepath.Join(home, ".claude.json"), token, setupPort, false)

	// 5. Antigravity CLI / Gemini CLI
	updateMCPConfigFile(filepath.Join(home, ".gemini/antigravity-cli/mcp.json"), token, setupPort, false)
	updateMCPConfigFile(filepath.Join(home, ".gemini/antigravity-cli/mcp_config.json"), token, setupPort, false)
	updateMCPConfigFile(filepath.Join(home, ".gemini/mcp_config.json"), token, setupPort, false)
	updateMCPConfigFile(filepath.Join(home, ".gemini/mcp.json"), token, setupPort, false)
	updateMCPConfigFile(filepath.Join(home, ".gemini/settings.json"), token, setupPort, false)

	// 5b. Antigravity IDE / Agent
	updateMCPConfigFile(filepath.Join(home, ".gemini/antigravity/mcp_config.json"), token, setupPort, false)
	updateMCPConfigFile(filepath.Join(home, ".gemini/antigravity-ide/mcp_config.json"), token, setupPort, false)
	updateMCPConfigFile(filepath.Join(home, "Library/Application Support/Antigravity/User/settings.json"), token, setupPort, false)
	updateMCPConfigFile(filepath.Join(home, "Library/Application Support/Antigravity IDE/User/settings.json"), token, setupPort, false)

	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("🎉 Auto-configuration complete!")
	fmt.Println("Restart your IDE or CLI agent on this server to apply the changes.")
	return nil
}

func updateMCPConfigFile(path string, token string, port int, isClaudeDesktop bool) {
	// If parent dir doesn't exist, don't force create it (means the app isn't installed)
	dir := filepath.Dir(path)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return
	}

	// Read existing JSON
	var config map[string]interface{}
	data, err := os.ReadFile(path)
	if err == nil {
		json.Unmarshal(data, &config)
	}
	if config == nil {
		config = make(map[string]interface{})
	}

	// Define our server structure
	argsList := []interface{}{"bridge"}
	if port != 7357 {
		argsList = append(argsList, "--addr", fmt.Sprintf("127.0.0.1:%d", port))
	}
	serverDef := map[string]interface{}{
		"command": "auxly",
		"args":    argsList,
		"env": map[string]interface{}{
			"AUXLY_BRIDGE_TOKEN": token,
		},
	}

	if isClaudeDesktop || filepath.Base(path) == "mcp_config.json" {
		// Claude Desktop and Antigravity IDE mcp_config.json put servers inside "mcpServers" key
		servers, ok := config["mcpServers"].(map[string]interface{})
		if !ok {
			servers = make(map[string]interface{})
		}
		servers["auxly-memory"] = serverDef
		config["mcpServers"] = servers
	} else {
		// Cursor, Windsurf, Claude Code, and Antigravity CLI use direct servers list or "mcpServers"
		// Let's support both direct key or "mcpServers" depending on what already exists
		if _, ok := config["mcpServers"]; ok || filepath.Base(path) == "mcp.json" {
			servers, ok := config["mcpServers"].(map[string]interface{})
			if !ok {
				servers = make(map[string]interface{})
			}
			servers["auxly-memory"] = serverDef
			config["mcpServers"] = servers
		} else {
		}
	}

	if filepath.Base(path) == "settings.json" && strings.Contains(path, ".gemini") {
		config["model"] = map[string]string{"name": "gemini-2.5-flash"}
	}

	// Marshal and write back
	newData, err := json.MarshalIndent(config, "", "  ")
	if err == nil {
		os.WriteFile(path, newData, 0644)
		fmt.Printf("✅ Automatically configured MCP for: %s\n", filepath.Base(filepath.Dir(filepath.Dir(path))))
	}
}
