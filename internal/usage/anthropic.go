package usage

import (
	"context"
	"encoding/json"
	"net/http"
)

// anthropicFetcher reads Claude usage from the OAuth usage endpoint. The same
// Anthropic account backs both Claude Code and Claude Desktop, so the "claude"
// and "claude-code" cards share this fetcher and show identical numbers.
type anthropicFetcher struct{ id string }

func (f anthropicFetcher) provider() string { return f.id }

func (f anthropicFetcher) fetch(ctx context.Context) Report {
	r := Report{Provider: f.id, Source: "api.anthropic.com"}

	token := claudeToken()
	if token == "" {
		r.Err = "no token — sign in to Claude Code"
		return r
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.anthropic.com/api/oauth/usage", nil)
	if err != nil {
		r.Err = "request build failed"
		return r
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")

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
		r.Err = "token expired — re-auth in Claude Code"
		return r
	}
	if resp.StatusCode != http.StatusOK {
		r.Err = httpStatusReason(resp.StatusCode)
		return r
	}

	var body struct {
		FiveHour struct {
			Utilization float64         `json:"utilization"`
			ResetsAt    json.RawMessage `json:"resets_at"`
		} `json:"five_hour"`
		SevenDay struct {
			Utilization float64         `json:"utilization"`
			ResetsAt    json.RawMessage `json:"resets_at"`
		} `json:"seven_day"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		r.Err = "unexpected response"
		return r
	}

	session := Window{Label: "Session", Pct: body.FiveHour.Utilization, IsLimit: true}
	if t, ok := parseResetTime(body.FiveHour.ResetsAt); ok {
		session.ResetAt, session.HasReset = t, true
	}
	week := Window{Label: "Week", Pct: body.SevenDay.Utilization, IsLimit: true}
	if t, ok := parseResetTime(body.SevenDay.ResetsAt); ok {
		week.ResetAt, week.HasReset = t, true
	}
	r.Windows = []Window{session, week}
	return r
}
