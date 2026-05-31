package cmd

import (
	"fmt"
	"strings"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/assets"
	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/update"
)

func printBanner() {
	lines := strings.Split(strings.TrimRight(assets.LogoANS, "\n"), "\n")
	labels := []string{
		fmt.Sprintf("\033[1;38;5;038mAuxly CLI\033[0m v%s", update.Current),
		"\033[38;5;240mUnified Memory for AI Agents\033[0m",
	}
	titleLine := 3

	for i, line := range lines {
		fmt.Print(line)
		if i >= titleLine && i-titleLine < len(labels) {
			fmt.Print("   " + labels[i-titleLine])
		}
		fmt.Println()
	}
	fmt.Println("\033[0m")
}
