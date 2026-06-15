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

		// 2. Build the Go binary from the module root (the dir holding go.mod).
		// The main package used to live in an `auxly-cli/` subdir; it's now at the
		// repo root, so locate go.mod across the likely layouts instead of blindly
		// appending `auxly-cli` (which failed with "chdir …/auxly-cli: no such
		// file or directory" on the current single-module layout).
		buildDir := wd
		for _, cand := range []string{wd, filepath.Join(wd, "auxly-cli"), filepath.Join(wd, "..")} {
			if _, statErr := os.Stat(filepath.Join(cand, "go.mod")); statErr == nil {
				buildDir = cand
				break
			}
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

		binName := "auxly"
		if runtime.GOOS == "windows" {
			binName += ".exe" // the running target is auxly.exe; keep build/read/install names consistent
		}
		buildCmd := exec.Command("go", "build", "-ldflags", "-s -w", "-o", binName, ".")
		buildCmd.Dir = buildDir
		buildCmd.Stdout = os.Stdout
		buildCmd.Stderr = os.Stderr
		if err := buildCmd.Run(); err != nil {
			return fmt.Errorf("failed to compile auxly binary: %w", err)
		}
		fmt.Print("✅ Compilation completed successfully!\r\n\r\n")

		// 3. Install over the CURRENTLY-RUNNING binary so the update actually
		// takes effect on PATH. Hardcoding ~/.local/bin/auxly meant a dev box whose
		// `auxly` resolves elsewhere (e.g. ~/.bun/bin/auxly) rebuilt fine but kept
		// running the stale binary. Fall back to ~/.local/bin/auxly only when the
		// running path can't be resolved.
		home, _ := os.UserHomeDir()
		targetBin := filepath.Join(home, ".local", "bin", "auxly")
		if exe, exeErr := os.Executable(); exeErr == nil && exe != "" {
			if real, linkErr := filepath.EvalSymlinks(exe); linkErr == nil && real != "" {
				targetBin = real
			} else {
				targetBin = exe
			}
		}
		sourceBin := filepath.Join(buildDir, binName)

		fmt.Printf("🚚 Installing fresh binary globally to: %s...\r\n", targetBin)

		// Read the freshly built binary first, then install it.
		data, err := os.ReadFile(sourceBin)
		if err != nil {
			return fmt.Errorf("failed to read built binary: %w", err)
		}
		if runtime.GOOS == "windows" {
			// A running .exe is locked: you cannot delete/overwrite it, but you CAN
			// rename the live image aside, then write the new one into the freed name.
			_ = os.Remove(targetBin + ".old") // clear any prior swap leftover
			if rerr := os.Rename(targetBin, targetBin+".old"); rerr != nil {
				return fmt.Errorf("failed to move running binary aside: %w", rerr)
			}
			if werr := os.WriteFile(targetBin, data, 0755); werr != nil {
				_ = os.Rename(targetBin+".old", targetBin) // best-effort rollback
				return fmt.Errorf("failed to write binary to target bin path: %w", werr)
			}
			_ = os.Remove(targetBin + ".old") // best-effort; locked .old clears on next run
		} else {
			_ = os.Remove(targetBin) // break Unix locks/symlinks
			if werr := os.WriteFile(targetBin, data, 0755); werr != nil {
				return fmt.Errorf("failed to write binary to target bin path: %w", werr)
			}
		}

		// Clear quarantine markers
		xattrCmd := exec.Command("xattr", "-c", targetBin)
		_ = xattrCmd.Run()

		// On Apple Silicon a copied/rewritten Go binary carries an invalid ad-hoc
		// signature and the kernel SIGKILLs it on launch ("zsh: killed"). Re-sign so
		// the freshly-installed dev binary actually runs. Release binaries are signed
		// by CI — this only matters for the local dev rebuild.
		if runtime.GOOS == "darwin" {
			_ = exec.Command("codesign", "--force", "--sign", "-", targetBin).Run()
		}

		fmt.Print("🎉 " + bold + green + "SUCCESS! Auxly has been rebuilt and updated successfully!" + reset + "\r\n")
		fmt.Printf("   ↳ Global path: %s\r\n", targetBin)
		fmt.Printf("   ↳ Active version: dev-latest\r\n\r\n")

	} else {
		// Production/Release mode — download the latest binary from the
		// distribution host and atomically replace this executable.
		fmt.Print("🌐 " + bold + cyan + "Release Mode" + reset + "\r\n")

		// A package-manager-vendored binary must update through its manager: a
		// self-replace would be clobbered on the next `npm`/`pip` update (and on
		// Windows the npm-vendored copy can be locked while running). Redirect to
		// the right command instead of attempting — and failing — a self-replace.
		if method := update.InstallMethod(); method != "" {
			fmt.Printf("📦 Auxly was installed via %s — update it through %s:\r\n\r\n", method, method)
			fmt.Printf("   %s%s%s\r\n\r\n", bold, update.ManagedUpdateHint(method), reset)
			return nil
		}

		fmt.Printf("👉 Fetching the latest auxly for %s/%s...\r\n\r\n", runtime.GOOS, runtime.GOARCH)

		path, err := update.SelfUpdate()
		if err != nil {
			fmt.Printf("✗ Update failed: %v\r\n", err)
			fmt.Printf("   You can also run: %s\r\n\r\n", update.InstallerCommand())
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
