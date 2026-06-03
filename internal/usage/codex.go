package usage

import (
	"context"
	"encoding/json"
	"net/http"
)

// codexFetcher reads Codex (ChatGPT) usage. Codex exposes a primary (session)
// and secondary (weekly) rate-limit window, mirroring Claude's shape.
type codexFetcher struct{}

func (codexFetcher) provider() string { return "codex" }

func (codexFetcher) fetch(ctx context.Context) Report {
	r := Report{Provider: "codex", Source: "chatgpt.com"}

	token, accountID := codexAuth()
	if token == "" || accountID == "" {
		r.Err = "no token — sign in to Codex"
		return r
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://chatgpt.com/backend-api/wham/usage", nil)
	if err != nil {
		r.Err = "request build failed"
		return r
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("ChatGPT-Account-Id", accountID)

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
		r.Err = "token expired — re-auth in Codex"
		return r
	}
	if resp.StatusCode != http.StatusOK {
		r.Err = httpStatusReason(resp.StatusCode)
		return r
	}

	var body struct {
		RateLimit struct {
			PrimaryWindow struct {
				UsedPercent float64         `json:"used_percent"`
				ResetAt     json.RawMessage `json:"reset_at"`
			} `json:"primary_window"`
			SecondaryWindow struct {
				UsedPercent float64         `json:"used_percent"`
				ResetAt     json.RawMessage `json:"reset_at"`
			} `json:"secondary_window"`
		} `json:"rate_limit"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		r.Err = "unexpected response"
		return r
	}

	session := Window{Label: "Session", Pct: body.RateLimit.PrimaryWindow.UsedPercent, IsLimit: true}
	if t, ok := parseResetTime(body.RateLimit.PrimaryWindow.ResetAt); ok {
		session.ResetAt, session.HasReset = t, true
	}
	week := Window{Label: "Week", Pct: body.RateLimit.SecondaryWindow.UsedPercent, IsLimit: true}
	if t, ok := parseResetTime(body.RateLimit.SecondaryWindow.ResetAt); ok {
		week.ResetAt, week.HasReset = t, true
	}
	r.Windows = []Window{session, week}
	r.Account, r.Plan, r.Org = codexIdentity()
	return r
}
