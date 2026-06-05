//go:build windows

package statusline

// ttyWidth has no cheap /dev/tty equivalent on Windows; $COLUMNS (checked in termWidth)
// is the fallback, and 0 here leaves lines unconstrained.
func ttyWidth() int { return 0 }
