package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/memory"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/update"
	"github.com/spf13/cobra"
)

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

		path, err := update.SelfUpdate()
		if err != nil {
			fmt.Printf("✗ Update failed: %v\r\n", err)
			fmt.Printf("   You can also run: curl -sSL %s/cli | sh\r\n\r\n", update.BaseURL())
			return err
		}
		fmt.Print("🎉 " + bold + green + "Updated to the latest release!" + reset + "\r\n")
		fmt.Printf("   ↳ Path: %s\r\n", path)
		if out, verr := exec.Command(path, "--version").CombinedOutput(); verr == nil {
			fmt.Printf("   ↳ %s\r\n", strings.TrimSpace(firstLine(string(out))))
		}
		fmt.Print("\r\n")
	}

	// Back-fill any new default memory files this release introduced (e.g.
	// personal.md) for existing users — idempotent, never overwrites.
	if created, _ := memory.SeedDefaultFiles(getMemoryPath()); len(created) > 0 {
		fmt.Printf("📂 Added new memory files: %s\r\n\r\n", strings.Join(created, ", "))
	}

	return nil
}
