// Package clipboard copies text to the system clipboard via a platform tool.
// It lives under internal/ (rather than cmd/) so both the CLI (auto-copy
// after `host invite`) and the TUI ([y] on a held invite token) share one
// implementation instead of each shelling their own.
package clipboard

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// ErrNoTool is returned when no supported clipboard command is found on
// PATH for the current platform.
var ErrNoTool = errors.New("no clipboard tool found on PATH")

// argvFor picks the first available clipboard command from a
// platform-ordered candidate list. Pure — goos and the "is this on PATH"
// probe are both passed in — so tool selection is unit-testable without
// depending on what's actually installed on the machine running the tests.
func argvFor(goos string, available func(name string) bool) []string {
	var candidates [][]string
	switch goos {
	case "darwin":
		candidates = [][]string{{"pbcopy"}}
	case "windows":
		candidates = [][]string{{"clip.exe"}}
	case "linux":
		candidates = [][]string{
			{"wl-copy"},
			{"xclip", "-selection", "clipboard"},
			{"xsel", "--clipboard", "--input"},
		}
	}
	for _, argv := range candidates {
		if available(argv[0]) {
			return argv
		}
	}
	return nil
}

// Copy sends text to the system clipboard by piping it via STDIN — never
// argv — to the first available platform tool, so a secret (e.g. an invite
// token) never shows up in `ps` output or shell history.
func Copy(text string) error {
	argv := argvFor(runtime.GOOS, func(name string) bool {
		_, err := exec.LookPath(name)
		return err == nil
	})
	if argv == nil {
		return ErrNoTool
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = strings.NewReader(text)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w: %s", argv[0], err, bytes.TrimSpace(stderr.Bytes()))
	}
	return nil
}
