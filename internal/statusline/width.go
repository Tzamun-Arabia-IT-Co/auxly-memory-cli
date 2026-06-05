package statusline

import (
	"os"
	"strconv"
	"strings"
)

// termWidth returns the terminal's column count, or 0 when it can't be determined (in
// which case the renderer leaves every line unconstrained — the pre-responsive behavior).
// It runs on every statusline refresh, so it must be cheap and never block: a $COLUMNS
// lookup plus a single non-blocking ioctl on the controlling terminal.
func termWidth() int {
	if c := strings.TrimSpace(os.Getenv("COLUMNS")); c != "" {
		if n, err := strconv.Atoi(c); err == nil && n > 0 {
			return n
		}
	}
	return ttyWidth()
}
