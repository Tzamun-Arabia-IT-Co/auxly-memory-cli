package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// checkAndInstallDependencies verifies that Node.js is installed and
// automatically runs 'npm install' if node_modules is missing in the workspace.
func checkAndInstallDependencies() {
	// Check if Node.js is installed
	_, err := exec.LookPath("node")
	if err != nil {
		fmt.Println("⚠️ Node.js is not found on your system. Some features like ChatGPT relayer require Node.js.")
		return
	}

	// Check if npm is installed
	_, err = exec.LookPath("npm")
	if err != nil {
		fmt.Println("⚠️ npm is not found on your system.")
		return
	}

	// Get current working directory
	cwd, err := os.Getwd()
	if err != nil {
		return
	}

	// Locate package.json in current directory or one level up
	pkgJSONPath := filepath.Join(cwd, "package.json")
	if _, err := os.Stat(pkgJSONPath); os.IsNotExist(err) {
		pkgJSONPath = filepath.Join(filepath.Dir(cwd), "package.json")
		if _, err := os.Stat(pkgJSONPath); os.IsNotExist(err) {
			return // Not inside the npm project root
		}
		cwd = filepath.Dir(cwd)
	}

	// Check if node_modules already exists
	nodeModulesPath := filepath.Join(cwd, "node_modules")
	if _, err := os.Stat(nodeModulesPath); err == nil {
		return // Dependencies already installed
	}

	fmt.Printf("📦 Missing Node.js dependencies. Running 'npm install' in %s...\n", cwd)
	runCmd := exec.Command("npm", "install")
	runCmd.Dir = cwd
	runCmd.Stdout = os.Stdout
	runCmd.Stderr = os.Stderr
	if err := runCmd.Run(); err != nil {
		fmt.Printf("❌ Failed to install Node.js dependencies: %v\n", err)
	} else {
		fmt.Println("✓ Successfully installed Node.js dependencies!")
	}
}
