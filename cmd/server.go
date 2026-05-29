package cmd

import (
	"bufio"
	"compress/gzip"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/Tzamun-Arabia-IT-Co/auxly-cli/mcp"
	"github.com/Tzamun-Arabia-IT-Co/auxly-cli/tui"
	"github.com/spf13/cobra"
)

var (
	serverPort     int
	staticToken    string
	serverCompress bool
	activeConns    int
	connMutex      sync.Mutex
	relayerCmd     *exec.Cmd
	ngrokCmd       *exec.Cmd
)

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Start the Auxly local-first memory TCP daemon gateway",
	Long:  `Starts a background localhost-only TCP daemon on 127.0.0.1:7357 that remote bridges connect to over secure SSH tunnels.`,
	RunE:  runServer,
}

func init() {
	serverCmd.Flags().IntVarP(&serverPort, "port", "p", 7357, "TCP port to listen on")
	serverCmd.Flags().StringVarP(&staticToken, "token", "t", "", "Override session token with a static value")
	serverCmd.Flags().BoolVarP(&serverCompress, "compress", "z", false, "Enable gzip compression for high-latency SSH connections")
	rootCmd.AddCommand(serverCmd)
}

type handshakeRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  struct {
		Token string `json:"token"`
	} `json:"params"`
}

func generateSessionToken() (string, error) {
	if staticToken != "" {
		return staticToken, nil
	}
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func writePIDFile() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	pidPath := filepath.Join(home, ".auxly", "daemon.pid")
	_ = os.MkdirAll(filepath.Dir(pidPath), 0755)
	
	pidStr := strconv.Itoa(os.Getpid())
	return os.WriteFile(pidPath, []byte(pidStr), 0644)
}

func removePIDFile() {
	home, err := os.UserHomeDir()
	if err == nil {
		pidPath := filepath.Join(home, ".auxly", "daemon.pid")
		_ = os.Remove(pidPath)
	}
}

func updateConnsFile(count int) {
	home, err := os.UserHomeDir()
	if err == nil {
		connsPath := filepath.Join(home, ".auxly", "daemon.conns")
		_ = os.MkdirAll(filepath.Dir(connsPath), 0755)
		_ = os.WriteFile(connsPath, []byte(strconv.Itoa(count)), 0644)
	}
}

func removeConnsFile() {
	home, err := os.UserHomeDir()
	if err == nil {
		connsPath := filepath.Join(home, ".auxly", "daemon.conns")
		_ = os.Remove(connsPath)
	}
}

func writeTokenFile(token string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	tokenPath := filepath.Join(home, ".auxly", ".session_token")
	_ = os.MkdirAll(filepath.Dir(tokenPath), 0755)
	return os.WriteFile(tokenPath, []byte(token), 0600)
}

func removeTokenFile() {
	home, err := os.UserHomeDir()
	if err == nil {
		tokenPath := filepath.Join(home, ".auxly", ".session_token")
		_ = os.Remove(tokenPath)
	}
}

func writePortFile(port int) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	portPath := filepath.Join(home, ".auxly", "daemon.port")
	_ = os.MkdirAll(filepath.Dir(portPath), 0755)
	return os.WriteFile(portPath, []byte(strconv.Itoa(port)), 0644)
}

func removePortFile() {
	home, err := os.UserHomeDir()
	if err == nil {
		portPath := filepath.Join(home, ".auxly", "daemon.port")
		_ = os.Remove(portPath)
	}
}

func runServer(cmd *cobra.Command, args []string) error {
	if !serverCompress {
		serverCompress = tui.ReadCompressFromConfig()
	}
	memPath := getMemoryPath()
	token, err := generateSessionToken()
	if err != nil {
		return fmt.Errorf("failed to generate session token: %w", err)
	}

	if err := writeTokenFile(token); err != nil {
		return fmt.Errorf("failed to save session token: %w", err)
	}
	defer removeTokenFile()

	if err := writePIDFile(); err != nil {
		return fmt.Errorf("failed to save daemon PID: %w", err)
	}
	defer removePIDFile()

	updateConnsFile(0)
	defer removeConnsFile()

	// Dynamic port scanning: scan serverPort and 9 ports above it
	var listener net.Listener
	actualPort := serverPort
	for p := serverPort; p < serverPort+10; p++ {
		addr := fmt.Sprintf("127.0.0.1:%d", p)
		listener, err = net.Listen("tcp", addr)
		if err == nil {
			actualPort = p
			break
		}
	}
	if err != nil {
		return fmt.Errorf("failed to listen on %s (and tried next 9 ports): %w", fmt.Sprintf("127.0.0.1:%d", serverPort), err)
	}
	defer listener.Close()

	if err := writePortFile(actualPort); err != nil {
		return fmt.Errorf("failed to save daemon port: %w", err)
	}
	defer removePortFile()

	addr := fmt.Sprintf("127.0.0.1:%d", actualPort)
	compressLabel := "off"
	if serverCompress {
		compressLabel = "on (gzip)"
	}

	fmt.Println("📡 Auxly-Memory CLI Daemon")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Printf("📂 Memory Root:     %s\n", memPath)
	fmt.Printf("🌐 Socket Interface: %s (localhost-only)\n", addr)
	fmt.Printf("🔑 Session Token:    %s\n", token)
	fmt.Printf("🗜️  Compression:     %s\n", compressLabel)
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Printf("👉 Tunnel command:  ssh -R %d:127.0.0.1:%d user@remote\n", actualPort, actualPort)
	if serverCompress {
		fmt.Println("💡 Bridge must also use --compress / AUXLY_COMPRESS=1")
	}
	fmt.Println("\nServer running... Press Ctrl+C to stop.")

	// Set up signal handler for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\nStopping server gateway daemon gracefully...")
		if relayerCmd != nil && relayerCmd.Process != nil {
			_ = relayerCmd.Process.Kill()
		}
		if ngrokCmd != nil && ngrokCmd.Process != nil {
			_ = ngrokCmd.Process.Kill()
		}
		listener.Close()
		removePIDFile()
		removeTokenFile()
		removePortFile()
		os.Exit(0)
	}()

	// Background relayer and tunnels are disabled as remote/web clients are out of scope.

	mcpServer := mcp.NewServer(memPath)

	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-sigChan:
				return nil
			default:
				fmt.Fprintf(os.Stderr, "Accept connection error: %v\n", err)
				continue
			}
		}

		go handleClient(conn, token, mcpServer, serverCompress)
	}
}

func handleClient(conn net.Conn, token string, mcpServer *mcp.Server, compress bool) {
	defer conn.Close()

	// 1. Perform Security Handshake (always uncompressed — plain text)
	reader := bufio.NewReader(conn)
	lineBytes, err := reader.ReadBytes('\n')
	if err != nil {
		return
	}

	line := strings.TrimSpace(string(lineBytes))
	var handshake handshakeRequest
	if err := json.Unmarshal([]byte(line), &handshake); err != nil {
		sendHandshakeError(conn, -32600, "Invalid handshake payload format")
		return
	}

	if handshake.Method != "handshake" || handshake.Params.Token != token {
		sendHandshakeError(conn, -32001, "Unauthorized session token")
		return
	}

	// Reply authorized (uncompressed — bridge reads this before enabling compression)
	sendHandshakeSuccess(conn, handshake.ID)

	// 2. Track active connections
	connMutex.Lock()
	activeConns++
	updateConnsFile(activeConns)
	connMutex.Unlock()

	defer func() {
		connMutex.Lock()
		activeConns--
		updateConnsFile(activeConns)
		connMutex.Unlock()
	}()

	// 3. Optionally wrap streams with gzip compression
	var rpcIn io.Reader = conn
	var rpcOut io.Writer = conn

	if compress {
		// Wrap the already-buffered reader remainder back into a gzip reader.
		// The bridge will switch to gzip after receiving the handshake reply.
		gzReader, err := gzip.NewReader(conn)
		if err != nil {
			fmt.Fprintf(os.Stderr, "gzip reader init error: %v\n", err)
			return
		}
		defer gzReader.Close()

		gzWriter := gzip.NewWriter(conn)
		defer gzWriter.Close()

		rpcIn = gzReader
		rpcOut = gzWriter
	}

	// 4. Bind (possibly compressed) streams to MCP server
	_ = mcpServer.RunStream(rpcIn, rpcOut)
}

func sendHandshakeError(w io.Writer, code int, msg string) {
	resp := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      nil,
		"error": map[string]interface{}{
			"code":    code,
			"message": msg,
		},
	}
	data, _ := json.Marshal(resp)
	fmt.Fprintf(w, "%s\n", data)
}

func sendHandshakeSuccess(w io.Writer, id int) {
	resp := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  "authorized",
	}
	data, _ := json.Marshal(resp)
	fmt.Fprintf(w, "%s\n", data)
}



func findExecutable(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	home, _ := os.UserHomeDir()
	checkPaths := []string{
		"/opt/homebrew/bin/" + name,
		"/usr/local/bin/" + name,
		"/usr/bin/" + name,
		"/bin/" + name,
		filepath.Join(home, ".local/bin/", name),
	}
	for _, p := range checkPaths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}
