package cmd

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// installBaseURL is the distribution host serving per-OS/arch binaries at
// /dl/auxly-<os>-<arch>[.exe]. Overridable for testing/self-hosting.
func installBaseURL() string {
	if v := strings.TrimSpace(os.Getenv("AUXLY_INSTALL_BASE")); v != "" {
		return v
	}
	return "https://auxly.io"
}

// selfUpdateFromRelease downloads the matching binary from the distribution host
// and atomically replaces the running executable. Returns the resolved path.
func selfUpdateFromRelease() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("could not locate the running binary: %w", err)
	}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}

	url := fmt.Sprintf("%s/dl/auxly-%s-%s", installBaseURL(), runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		url += ".exe"
	}
	fmt.Printf("📥 Downloading %s\r\n", url)

	resp, err := http.Get(url)
	if err != nil {
		return "", fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download failed: HTTP %d for %s", resp.StatusCode, url)
	}

	// Write to a temp file in the SAME directory, then atomically rename over the
	// target (works even while the current process is running).
	tmp, err := os.CreateTemp(filepath.Dir(exe), ".auxly-update-*")
	if err != nil {
		return "", fmt.Errorf("could not create temp file next to %s (permissions?): %w", exe, err)
	}
	tmpPath := tmp.Name()
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("failed to write update: %w", err)
	}
	tmp.Close()
	if err := os.Chmod(tmpPath, 0755); err != nil {
		os.Remove(tmpPath)
		return "", err
	}
	if err := os.Rename(tmpPath, exe); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("could not replace %s (try re-running with sudo): %w", exe, err)
	}
	// Best-effort: clear quarantine on macOS.
	_ = exec.Command("xattr", "-c", exe).Run()
	return exe, nil
}

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Check for updates and automatically rebuild/install the latest package",
	RunE:  runUpdate,
}

func init() {
	rootCmd.AddCommand(updateCmd)
}

func runUpdate(cmd *cobra.Command, args []string) error {
	cyan := "\033[38;5;38m"
	purple := "\033[38;5;134m"
	green := "\033[38;5;34m"
	bold := "\033[1m"
	dim := "\033[38;5;240m"
	reset := "\033[0m"

	fmt.Print("\r\n" + bold + purple + "🔄 Auxly Automatic Update System" + reset + "\r\n")
	fmt.Print(dim + "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━" + reset + "\r\n")
	fmt.Print("🔍 Checking for local repository or remote updates...\r\n\r\n")

	// Check if we are inside a Git repository (Dev Mode)
	isGit := false
	wd, err := os.Getwd()
	if err == nil {
		// Check for .git directory in current or parent
		if _, err := os.Stat(filepath.Join(wd, ".git")); err == nil {
			isGit = true
		} else if _, err := os.Stat(filepath.Join(wd, "..", ".git")); err == nil {
			isGit = true
		}
	}

	if isGit {
		fmt.Print("📦 " + bold + cyan + "Git repository detected (Dev Mode)!" + reset + "\r\n")
		fmt.Print("👉 Pulling latest source changes from remote git repository...\r\n")

		// 1. Run git pull
		pullCmd := exec.Command("git", "pull")
		pullCmd.Stdout = os.Stdout
		pullCmd.Stderr = os.Stderr
		if err := pullCmd.Run(); err != nil {
			fmt.Printf("⚠️  [Git Pull Warning] %v (continuing with local build)\r\n", err)
		} else {
			fmt.Print("✅ Git pull completed successfully!\r\n")
		}
		fmt.Print("\r\n")

		// 2. Run go build inside auxly-cli
		buildDir := wd
		if filepath.Base(wd) != "auxly-cli" {
			buildDir = filepath.Join(wd, "auxly-cli")
		}
		fmt.Print("⚙️  " + bold + "Compiling & rebuilding Go binary..." + reset + "\r\n")

		// Shimmer progress emulation
		for i := 0; i <= 10; i++ {
			time.Sleep(100 * time.Millisecond)
			pct := i * 10
			bar := strings.Repeat("=", i) + strings.Repeat("-", 10-i)
			fmt.Printf("\r   [%s] %d%% Building...", bar, pct)
		}
		fmt.Print("\r\n")

		buildCmd := exec.Command("go", "build", "-ldflags", "-s -w", "-o", "auxly", ".")
		buildCmd.Dir = buildDir
		buildCmd.Stdout = os.Stdout
		buildCmd.Stderr = os.Stderr
		if err := buildCmd.Run(); err != nil {
			return fmt.Errorf("failed to compile auxly binary: %w", err)
		}
		fmt.Print("✅ Compilation completed successfully!\r\n\r\n")

		// 3. Install globally to ~/.local/bin/auxly
		home, _ := os.UserHomeDir()
		targetBin := filepath.Join(home, ".local", "bin", "auxly")
		sourceBin := filepath.Join(buildDir, "auxly")

		fmt.Printf("🚚 Installing fresh binary globally to: %s...\r\n", targetBin)

		// Remove existing to break locks
		_ = os.Remove(targetBin)

		// Copy binary
		data, err := os.ReadFile(sourceBin)
		if err != nil {
			return fmt.Errorf("failed to read built binary: %w", err)
		}
		err = os.WriteFile(targetBin, data, 0755)
		if err != nil {
			return fmt.Errorf("failed to write binary to target bin path: %w", err)
		}

		// Clear quarantine markers
		xattrCmd := exec.Command("xattr", "-c", targetBin)
		_ = xattrCmd.Run()

		fmt.Print("🎉 " + bold + green + "SUCCESS! Auxly has been rebuilt and updated successfully!" + reset + "\r\n")
		fmt.Printf("   ↳ Global path: %s\r\n", targetBin)
		fmt.Printf("   ↳ Active version: dev-latest\r\n\r\n")

	} else {
		// Production/Release mode — download the latest binary from the
		// distribution host and atomically replace this executable.
		fmt.Print("🌐 " + bold + cyan + "Release Mode" + reset + "\r\n")
		fmt.Printf("👉 Fetching the latest auxly for %s/%s...\r\n\r\n", runtime.GOOS, runtime.GOARCH)

		path, err := selfUpdateFromRelease()
		if err != nil {
			fmt.Printf("✗ Update failed: %v\r\n", err)
			fmt.Printf("   You can also run: curl -sSL %s/cli | sh\r\n\r\n", installBaseURL())
			return err
		}
		fmt.Print("🎉 " + bold + green + "Updated to the latest release!" + reset + "\r\n")
		fmt.Printf("   ↳ Path: %s\r\n", path)
		if out, verr := exec.Command(path, "--version").CombinedOutput(); verr == nil {
			fmt.Printf("   ↳ %s\r\n", strings.TrimSpace(firstLine(string(out))))
		}
		fmt.Print("\r\n")
	}

	return nil
}
