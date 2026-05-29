package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"

	"github.com/spf13/cobra"
)

// ─────────────────────────────────────────────────────────────────
//  LaunchAgent PLIST template
// ─────────────────────────────────────────────────────────────────

const launchAgentPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.tzamun.auxly.server</string>

    <key>ProgramArguments</key>
    <array>
        <string>{{.BinaryPath}}</string>
        <string>server</string>
        <string>--port</string>
        <string>{{.Port}}</string>
    </array>

    <!-- Restart automatically if it crashes -->
    <key>KeepAlive</key>
    <true/>

    <!-- Launch at login (not only on-demand) -->
    <key>RunAtLoad</key>
    <true/>

    <!-- Stdout / Stderr logs -->
    <key>StandardOutPath</key>
    <string>{{.LogDir}}/auxly-server.log</string>
    <key>StandardErrorPath</key>
    <string>{{.LogDir}}/auxly-server-error.log</string>

    <!-- Throttle restart to 10-second intervals if it crashes repeatedly -->
    <key>ThrottleInterval</key>
    <integer>10</integer>
</dict>
</plist>
`

// ─────────────────────────────────────────────────────────────────
//  Install subcommand
// ─────────────────────────────────────────────────────────────────

var installCmd = &cobra.Command{
	Use:   "install",
	Short: "Install auxly server as a macOS LaunchAgent (auto-start on login)",
	Long: `Writes a launchd PLIST to ~/Library/LaunchAgents/com.tzamun.auxly.server.plist
and immediately loads the service, so the TCP memory gateway starts automatically
every time you log in — no manual 'auxly server' required.`,
	RunE: runInstall,
}

var uninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Unload and remove the auxly server LaunchAgent",
	RunE:  runUninstall,
}

var installPort int

func init() {
	installCmd.Flags().IntVarP(&installPort, "port", "p", 7357, "TCP port the daemon will listen on")
	serverCmd.AddCommand(installCmd)
	serverCmd.AddCommand(uninstallCmd)
}

// ─────────────────────────────────────────────────────────────────
//  Helpers
// ─────────────────────────────────────────────────────────────────

func launchAgentPlistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", "com.tzamun.auxly.server.plist"), nil
}

func auxlyLogDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, "Library", "Logs", "auxly")
	return dir, os.MkdirAll(dir, 0755)
}

// selfBinaryPath returns the absolute path of the currently running auxly binary.
func selfBinaryPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	// Follow symlinks to reach the real binary path
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return exe, nil // fall back to unresolved
	}
	return resolved, nil
}

// ─────────────────────────────────────────────────────────────────
//  Install
// ─────────────────────────────────────────────────────────────────

func runInstall(cmd *cobra.Command, args []string) error {
	plistPath, err := launchAgentPlistPath()
	if err != nil {
		return fmt.Errorf("cannot resolve LaunchAgent path: %w", err)
	}

	logDir, err := auxlyLogDir()
	if err != nil {
		return fmt.Errorf("cannot create log directory: %w", err)
	}

	binaryPath, err := selfBinaryPath()
	if err != nil {
		return fmt.Errorf("cannot determine binary path: %w", err)
	}

	// Render the PLIST template
	tmpl, err := template.New("plist").Parse(launchAgentPlist)
	if err != nil {
		return fmt.Errorf("template parse error: %w", err)
	}

	data := struct {
		BinaryPath string
		Port       string
		LogDir     string
	}{
		BinaryPath: binaryPath,
		Port:       strconv.Itoa(installPort),
		LogDir:     logDir,
	}

	f, err := os.Create(plistPath)
	if err != nil {
		return fmt.Errorf("cannot write PLIST to %s: %w", plistPath, err)
	}
	defer f.Close()

	if err := tmpl.Execute(f, data); err != nil {
		return fmt.Errorf("template render error: %w", err)
	}
	f.Close()

	// Load the service immediately with launchctl
	fmt.Printf("📝 PLIST written to: %s\n", plistPath)
	fmt.Println("⚙️  Loading service with launchctl...")

	out, err := exec.Command("launchctl", "load", "-w", plistPath).CombinedOutput()
	if err != nil {
		fmt.Printf("⚠️  launchctl load warning: %s\n", strings.TrimSpace(string(out)))
		fmt.Println("   You can load it manually: launchctl load -w", plistPath)
	} else {
		fmt.Println("✅ Service loaded and will auto-start on every login!")
	}

	fmt.Println()
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Printf("  Binary:  %s\n", binaryPath)
	fmt.Printf("  Port:    %d\n", installPort)
	fmt.Printf("  Logs:    %s/auxly-server.log\n", logDir)
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println()
	fmt.Println("💡 Session token: cat ~/.auxly/.session_token")
	fmt.Println("💡 Tunnel command: ssh -R 7357:127.0.0.1:7357 user@remote")
	fmt.Println("💡 To uninstall: auxly server uninstall")

	return nil
}

// ─────────────────────────────────────────────────────────────────
//  Uninstall
// ─────────────────────────────────────────────────────────────────

func runUninstall(cmd *cobra.Command, args []string) error {
	plistPath, err := launchAgentPlistPath()
	if err != nil {
		return fmt.Errorf("cannot resolve LaunchAgent path: %w", err)
	}

	if _, err := os.Stat(plistPath); os.IsNotExist(err) {
		fmt.Println("ℹ️  No LaunchAgent found — nothing to uninstall.")
		return nil
	}

	fmt.Println("⚙️  Unloading service...")
	out, err := exec.Command("launchctl", "unload", "-w", plistPath).CombinedOutput()
	if err != nil {
		fmt.Printf("⚠️  launchctl unload warning: %s\n", strings.TrimSpace(string(out)))
	}

	if err := os.Remove(plistPath); err != nil {
		return fmt.Errorf("failed to remove PLIST: %w", err)
	}

	fmt.Printf("✅ LaunchAgent removed: %s\n", plistPath)
	fmt.Println("   The auxly memory daemon will no longer start automatically on login.")
	return nil
}
