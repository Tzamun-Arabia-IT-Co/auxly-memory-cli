package usage

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// FormatReset renders the time until a reset as a compact human string,
// matching the reference plugin: "now", "2d 5h", "2d", "3h 45m", "3h", "45m".
func FormatReset(resetAt time.Time, now time.Time) string {
	if resetAt.IsZero() {
		return ""
	}
	d := resetAt.Sub(now)
	if d <= 0 {
		return "now"
	}
	mins := int(d / time.Minute)
	hours := mins / 60
	days := hours / 24
	switch {
	case days > 0:
		if h := hours % 24; h > 0 {
			return fmt.Sprintf("%dd %dh", days, h)
		}
		return fmt.Sprintf("%dd", days)
	case hours > 0:
		if mm := mins % 60; mm > 0 {
			return fmt.Sprintf("%dh %dm", hours, mm)
		}
		return fmt.Sprintf("%dh", hours)
	default:
		return fmt.Sprintf("%dm", mins)
	}
}

// parseResetTime accepts the two shapes providers use for a reset value: an
// ISO-8601 / RFC3339 string (Claude, Codex, Google) or a numeric Unix value
// (seconds). It returns the parsed time and whether parsing succeeded.
func parseResetTime(raw json.RawMessage) (time.Time, bool) {
	if len(raw) == 0 {
		return time.Time{}, false
	}
	// Try string first.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil && s != "" {
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05Z07:00"} {
			if t, err := time.Parse(layout, s); err == nil {
				return t, true
			}
		}
		// Numeric-as-string (rare).
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return unixGuess(n), true
		}
		return time.Time{}, false
	}
	// Numeric: could be seconds or milliseconds.
	var n float64
	if err := json.Unmarshal(raw, &n); err == nil && n > 0 {
		return unixGuess(int64(n)), true
	}
	return time.Time{}, false
}

// unixGuess interprets a Unix timestamp that may be in seconds or milliseconds.
// Anything past ~year 2286 in seconds is assumed to actually be milliseconds.
func unixGuess(n int64) time.Time {
	if n > 1e12 {
		return time.UnixMilli(n)
	}
	return time.Unix(n, 0)
}
