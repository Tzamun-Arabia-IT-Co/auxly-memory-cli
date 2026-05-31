package usage

import (
	"fmt"
	"net/http"
	"time"
)

// httpClient is shared across providers. Per-request timeouts come from the
// context (fetchTimeout); this cap is a backstop for connection setup.
var httpClient = &http.Client{Timeout: fetchTimeout + 2*time.Second}

// httpStatusReason turns a non-OK status into a short, user-facing reason for
// the popup detail line.
func httpStatusReason(code int) string {
	switch {
	case code == http.StatusTooManyRequests:
		return "rate limited — try later"
	case code == http.StatusForbidden:
		return "access denied"
	case code >= 500:
		return fmt.Sprintf("provider error (%d)", code)
	default:
		return fmt.Sprintf("unavailable (%d)", code)
	}
}
