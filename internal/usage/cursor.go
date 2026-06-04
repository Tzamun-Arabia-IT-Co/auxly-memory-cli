package usage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver for reading Cursor's state.vscdb
)

// cursorFetcher reads Cursor plan quota via the dashboard Connect RPC endpoint
// (GetCurrentPeriodUsage), using the JWT Cursor already stores in
// ~/Library/Application Support/Cursor/User/globalStorage/state.vscdb.
// Unofficial but stable enough for local usage widgets; same approach as
// Cursor Usage Status / openusage.
type cursorFetcher struct{}

func (cursorFetcher) provider() string { return "cursor" }

func (cursorFetcher) fetch(ctx context.Context) Report {
	r := Report{Provider: "cursor", Source: "api2.cursor.sh"}
	if !cursorInstalled() {
		r.Err = "Cursor not detected"
		return r
	}
	token := cursorToken()
	if token == "" {
		r.Err = "no token — sign in to Cursor"
		return r
	}
	r.Account = cursorAccountEmail()
	r.Plan = cursorPlanLabel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api2.cursor.sh/aiserver.v1.DashboardService/GetCurrentPeriodUsage",
		bytes.NewReader([]byte("{}")))
	if err != nil {
		r.Err = "request build failed"
		return r
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")

	resp, err := httpClient.Do(req)
	if err != nil {
		r.Err = "offline or unreachable"
		return r
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		r.Err = httpStatusReason(resp.StatusCode)
		r.RateLimited = true
		return r
	}
	if resp.StatusCode == http.StatusUnauthorized {
		r.Err = "token expired — re-auth in Cursor"
		return r
	}
	if resp.StatusCode != http.StatusOK {
		r.Err = httpStatusReason(resp.StatusCode)
		return r
	}

	var body struct {
		BillingCycleEnd string `json:"billingCycleEnd"`
		PlanUsage       struct {
			TotalPercentUsed float64 `json:"totalPercentUsed"`
			AutoPercentUsed  float64 `json:"autoPercentUsed"`
			APIPercentUsed   float64 `json:"apiPercentUsed"`
			Remaining        float64 `json:"remaining"` // cents, when present
			Limit            float64 `json:"limit"`     // cents
		} `json:"planUsage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		r.Err = "unexpected response"
		return r
	}

	resetAt, hasReset := parseCursorCycleEnd(body.BillingCycleEnd)

	// A 200 OK from GetCurrentPeriodUsage means the plan quota IS exposed, so emit the
	// plan (Total) and Auto bars unconditionally — even at 0%. A brand-new or just-
	// reset plan legitimately reads 0% and must render as full meters, NOT as a "no
	// quota data" error (which made an idle plan look broken). Errors are reserved for
	// genuine transport/auth/non-200 failures, all handled above.
	addWindow := func(label string, pct float64) {
		w := Window{Label: label, Pct: pct, IsLimit: true}
		if hasReset {
			w.ResetAt, w.HasReset = resetAt, true
		}
		r.Windows = append(r.Windows, w)
	}
	addWindow("Total", body.PlanUsage.TotalPercentUsed)
	addWindow("Auto", body.PlanUsage.AutoPercentUsed)
	// NOTE: Cursor's API (pay-as-you-go) bucket is intentionally NOT shown. Cursor's
	// usage endpoint reports a non-zero apiPercentUsed even for plan-only users who
	// never touch an API key, so it's misleading noise on the statusline. The plan
	// (Total) and Auto bars are the meaningful plan quotas.
	return r
}

func parseCursorCycleEnd(ms string) (time.Time, bool) {
	ms = strings.TrimSpace(ms)
	if ms == "" {
		return time.Time{}, false
	}
	n, err := strconv.ParseInt(ms, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.UnixMilli(n), true
}

func cursorInstalled() bool {
	home := homeDir()
	for _, p := range []string{
		filepath.Join(home, ".cursor"),
		cursorGlobalStateDB(),
	} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

func cursorGlobalStateDB() string {
	home := homeDir()
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(os.Getenv("APPDATA"), "Cursor", "User", "globalStorage", "state.vscdb")
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Cursor", "User", "globalStorage", "state.vscdb")
	default:
		return filepath.Join(home, ".config", "Cursor", "User", "globalStorage", "state.vscdb")
	}
}

func cursorStateValue(key string) string {
	dbPath := cursorGlobalStateDB()
	if dbPath == "" {
		return ""
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return ""
	}
	defer db.Close()
	var v string
	err = db.QueryRow(`SELECT value FROM ItemTable WHERE key = ?`, key).Scan(&v)
	if err != nil {
		return ""
	}
	return v
}

func cursorToken() string {
	return cursorStateValue("cursorAuth/accessToken")
}

func cursorAccountEmail() string {
	return cursorStateValue("cursorAuth/cachedEmail")
}

func cursorPlanLabel() string {
	tier := cursorStateValue("cursorAuth/stripeMembershipType")
	if tier == "" {
		return ""
	}
	return titleWord(tier)
}
