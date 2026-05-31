package usage

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// titleWord upper-cases the first rune of a lowercase plan/tier word
// ("plus" -> "Plus", "max" -> "Max") without pulling in golang.org/x/text.
func titleWord(s string) string {
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// decodeJWTClaims decodes (without verifying) a JWT's payload into a claims map.
// Used only to read display fields (email, plan) from id_tokens already on disk.
func decodeJWTClaims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var claims map[string]any
	if json.Unmarshal(payload, &claims) != nil {
		return nil
	}
	return claims
}

// Token values loaded here are held only in memory for the duration of a single
// fetch and sent only to their own provider's endpoint. They are never logged
// or persisted by Auxly.

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return ""
}

// readJSONFile loads and unmarshals a JSON credential file into v.
func readJSONFile(path string, v any) error {
	b, err := os.ReadFile(path) //nolint:gosec // path is a fixed well-known cred location
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// claudeToken returns the Claude OAuth access token, trying the macOS keychain
// service "Claude Code-credentials" first (where recent Claude Code stores it),
// then the ~/.claude/.credentials.json fallback. Returns "" if neither yields a
// token (the report then renders "—  sign in to Claude Code").
func claudeToken() string {
	var c struct {
		ClaudeAiOauth struct {
			AccessToken string `json:"accessToken"`
		} `json:"claudeAiOauth"`
	}
	raw := claudeCredentialJSON()
	if raw == "" || json.Unmarshal([]byte(raw), &c) != nil {
		return ""
	}
	return c.ClaudeAiOauth.AccessToken
}

// claudeTier returns a human subscription label for the signed-in Claude account
// from the local credential (e.g. "Max (20x)", "Pro"). Empty if not present.
func claudeTier() string {
	type creds struct {
		ClaudeAiOauth struct {
			SubscriptionType string `json:"subscriptionType"` // "max", "pro", "free"
			RateLimitTier    string `json:"rateLimitTier"`    // "default_claude_max_20x"
		} `json:"claudeAiOauth"`
	}
	var c creds
	raw := claudeCredentialJSON()
	if raw == "" || json.Unmarshal([]byte(raw), &c) != nil {
		return ""
	}
	sub := c.ClaudeAiOauth.SubscriptionType
	if sub == "" {
		return ""
	}
	label := titleWord(sub)
	// Pull a "20x"-style multiplier out of e.g. "default_claude_max_20x".
	if parts := strings.Split(c.ClaudeAiOauth.RateLimitTier, "_"); len(parts) > 0 {
		last := parts[len(parts)-1]
		if strings.HasSuffix(last, "x") && last != label {
			label += " (" + last + ")"
		}
	}
	return label
}

// claudeCredentialJSON returns the raw Claude credential JSON (keychain or file),
// or "" if unavailable. Shared by claudeToken and claudeTier.
func claudeCredentialJSON() string {
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("security", "find-generic-password",
			"-s", "Claude Code-credentials", "-w").Output(); err == nil {
			if s := strings.TrimSpace(string(out)); s != "" {
				return s
			}
		}
	}
	if b, err := os.ReadFile(filepath.Join(homeDir(), ".claude", ".credentials.json")); err == nil {
		return string(b)
	}
	return ""
}

// codexAuth returns the Codex access token and ChatGPT account id from
// ~/.codex/auth.json. Both are required for the usage call.
func codexAuth() (accessToken, accountID string) {
	var c struct {
		Tokens struct {
			AccessToken string `json:"access_token"`
			AccountID   string `json:"account_id"`
		} `json:"tokens"`
	}
	if readJSONFile(filepath.Join(homeDir(), ".codex", "auth.json"), &c) == nil {
		return c.Tokens.AccessToken, c.Tokens.AccountID
	}
	return "", ""
}

// codexIdentity returns the signed-in email, plan label ("Plus"), and default
// organization name from the Codex id_token. All empty if unavailable.
func codexIdentity() (email, plan, org string) {
	var c struct {
		Tokens struct {
			IDToken string `json:"id_token"`
		} `json:"tokens"`
	}
	if readJSONFile(filepath.Join(homeDir(), ".codex", "auth.json"), &c) != nil {
		return "", "", ""
	}
	claims := decodeJWTClaims(c.Tokens.IDToken)
	if claims == nil {
		return "", "", ""
	}
	email, _ = claims["email"].(string)
	if auth, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
		if p, _ := auth["chatgpt_plan_type"].(string); p != "" {
			plan = titleWord(p)
		}
		if orgs, ok := auth["organizations"].([]any); ok {
			for _, o := range orgs {
				if om, ok := o.(map[string]any); ok {
					if def, _ := om["is_default"].(bool); def {
						org, _ = om["title"].(string)
						break
					}
				}
			}
		}
	}
	return email, plan, org
}

// googleCreds is the standard google-auth credential shape written by the
// Gemini CLI to ~/.gemini/oauth_creds.json. The access token is short-lived;
// when expired we exchange refresh_token for a fresh one.
type googleCreds struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiryDate   int64  `json:"expiry_date"` // Unix ms
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	IDToken      string `json:"id_token"`
	Email        string `json:"email"` // populated by our own Antigravity OAuth flow
}

// googleEmail returns the signed-in email for a Google credential: the stored
// Email field (Antigravity, written at auth) or the id_token's email claim
// (Gemini CLI). Empty if neither is present.
func googleEmail(c googleCreds) string {
	if c.Email != "" {
		return c.Email
	}
	if claims := decodeJWTClaims(c.IDToken); claims != nil {
		if e, _ := claims["email"].(string); e != "" {
			return e
		}
	}
	return ""
}

// geminiCreds loads the Gemini CLI credential file.
func geminiCreds() (googleCreds, bool) {
	var c googleCreds
	if readJSONFile(filepath.Join(homeDir(), ".gemini", "oauth_creds.json"), &c) == nil && c.RefreshToken != "" {
		return c, true
	}
	return googleCreds{}, false
}
