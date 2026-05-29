package cmd

import (
	"fmt"
	"strings"

	"github.com/Tzamun-Arabia-IT-Co/auxly-cli/assets"
)

func printBanner() {
	lines := strings.Split(strings.TrimRight(assets.LogoANS, "\n"), "\n")
	labels := []string{
		"\033[1;38;5;038mAuxly CLI\033[0m v1.0.0",
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
