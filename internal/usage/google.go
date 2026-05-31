package usage

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Google Code Assist backs both Gemini CLI and Antigravity. It exposes per-model
// quota buckets (a single rolling allowance), NOT Claude-style 5h/7d windows, so
// these providers surface one "Overall" usage figure plus the soonest reset.
//
// Gemini CLI stores a standard google-auth credential at ~/.gemini/oauth_creds.json
// that we can refresh and use directly. Antigravity does NOT drop a readable
// token of its own (the reference plugin runs its own OAuth for it), so for the
// "antigravity" card we make a best-effort attempt with the Gemini credential
// against Antigravity's daily- host; if Google rejects it, the card shows "—".

// geminiOAuthClient is the public installed-app client the Gemini CLI ships, used
// only to refresh the user's own stored token — the same exchange the CLI does.
const (
	geminiClientID     = "REDACTED_GEMINI_CLIENT_ID"
	geminiClientSecret = "REDACTED_GEMINI_CLIENT_SECRET"
)

type googleFetcher struct{ id string }

func (f googleFetcher) provider() string { return f.id }

func (f googleFetcher) fetch(ctx context.Context) Report {
	r := Report{Provider: f.id}

	var creds googleCreds
	var ok bool
	clientID, clientSecret := geminiClientID, geminiClientSecret
	if f.id == "antigravity" {
		creds, ok = antigravityCreds()
		clientID, clientSecret = antigravityClientID, antigravityClientSecret
		if !ok {
			r.Err = "not authorized — run: auxly usage auth antigravity"
			return r
		}
	} else {
		creds, ok = geminiCreds()
		if !ok {
			r.Err = "no token — sign in to Gemini CLI"
			return r
		}
	}

	r.Account = googleEmail(creds)

	token, err := googleAccessToken(ctx, creds, clientID, clientSecret)
	if err != "" {
		r.Err = err
		return r
	}

	host := "https://cloudcode-pa.googleapis.com/v1internal"
	userAgent := ""
	body := map[string]any{}
	if f.id == "antigravity" {
		host = "https://daily-cloudcode-pa.googleapis.com/v1internal"
		userAgent = "antigravity"
		r.Source = "daily-cloudcode-pa.googleapis.com"
	} else {
		r.Source = "cloudcode-pa.googleapis.com"
		project, tier := loadCodeAssistInfo(ctx, host, token)
		if project != "" {
			body["project"] = project
		}
		if tier != "" {
			r.Plan = tier
		}
	}

	var quota struct {
		Buckets []struct {
			ModelID           string          `json:"modelId"`
			RemainingFraction *float64        `json:"remainingFraction"`
			ResetTime         json.RawMessage `json:"resetTime"`
		} `json:"buckets"`
	}
	if reason, limited := googlePost(ctx, host+":retrieveUserQuota", token, userAgent, body, &quota); reason != "" {
		r.Err = reason
		r.RateLimited = limited
		return r
	}
	if len(quota.Buckets) == 0 {
		r.Err = "no quota data"
		return r
	}

	// Overall usage = the most-depleted bucket (smallest remainingFraction);
	// its reset time is the soonest the user regains headroom.
	minRemain := 1.0
	var resetRaw json.RawMessage
	for _, b := range quota.Buckets {
		if b.RemainingFraction == nil {
			continue
		}
		if *b.RemainingFraction < minRemain {
			minRemain = *b.RemainingFraction
			resetRaw = b.ResetTime
		}
	}
	// When every bucket is full (0% used) none is "most depleted", so fall back
	// to the first bucket's reset so the window still shows when it rolls over.
	if len(resetRaw) == 0 {
		for _, b := range quota.Buckets {
			if len(b.ResetTime) > 0 {
				resetRaw = b.ResetTime
				break
			}
		}
	}
	used := clampPct(100 * (1 - minRemain))
	overall := Window{Label: "Overall", Pct: used, IsLimit: true}
	if t, ok := parseResetTime(resetRaw); ok {
		overall.ResetAt, overall.HasReset = t, true
	}
	r.Windows = []Window{overall}
	return r
}

// googleAccessToken returns a usable bearer token, refreshing the stored one via
// the refresh_token grant when it has expired.
func googleAccessToken(ctx context.Context, c googleCreds, clientID, clientSecret string) (token, errReason string) {
	if c.AccessToken != "" && c.ExpiryDate > time.Now().UnixMilli()+30_000 {
		return c.AccessToken, ""
	}
	form := url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"refresh_token": {c.RefreshToken},
		"grant_type":    {"refresh_token"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://oauth2.googleapis.com/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", "request build failed"
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", "offline or unreachable"
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "token refresh failed"
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if json.NewDecoder(resp.Body).Decode(&tok) != nil || tok.AccessToken == "" {
		return "", "token refresh failed"
	}
	return tok.AccessToken, ""
}

// loadCodeAssistInfo returns the Cloud AI Companion project id and the current
// subscription tier name from the Code Assist loadCodeAssist call. The project
// falls back to env overrides; the tier (e.g. "Standard", "Free") comes from
// currentTier and drives the Usage view's plan label.
func loadCodeAssistInfo(ctx context.Context, host, token string) (project, tier string) {
	var lca struct {
		CloudaicompanionProject string `json:"cloudaicompanionProject"`
		CurrentTier             struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"currentTier"`
	}
	body := map[string]any{"metadata": map[string]any{
		"ideType":    "IDE_UNSPECIFIED",
		"platform":   "PLATFORM_UNSPECIFIED",
		"pluginType": "GEMINI",
	}}
	if reason, _ := googlePost(ctx, host+":loadCodeAssist", token, "", body, &lca); reason == "" {
		project = lca.CloudaicompanionProject
		tier = lca.CurrentTier.Name
		if tier == "" && lca.CurrentTier.ID != "" {
			tier = prettyTierID(lca.CurrentTier.ID)
		}
	}
	// Env overrides take precedence for the project id.
	if p := os.Getenv("GOOGLE_CLOUD_PROJECT"); p != "" {
		project = p
	} else if p := os.Getenv("GOOGLE_CLOUD_PROJECT_ID"); p != "" {
		project = p
	}
	return project, tier
}

// prettyTierID turns an id like "free-tier" or "standard-tier" into "Free" /
// "Standard" for display.
func prettyTierID(id string) string {
	id = strings.TrimSuffix(id, "-tier")
	id = strings.ReplaceAll(id, "-", " ")
	if id == "" {
		return ""
	}
	return strings.ToUpper(id[:1]) + id[1:]
}

func googlePost(ctx context.Context, urlStr, token, userAgent string, body any, out any) (errReason string, rateLimited bool) {
	payload, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, strings.NewReader(string(payload)))
	if err != nil {
		return "request build failed", false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "offline or unreachable", false
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return httpStatusReason(resp.StatusCode), true
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "not authorized for this account", false
	}
	if resp.StatusCode != http.StatusOK {
		return httpStatusReason(resp.StatusCode), false
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return "unexpected response", false
	}
	return "", false
}

func clampPct(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}
