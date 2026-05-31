package usage

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

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
	type creds struct {
		ClaudeAiOauth struct {
			AccessToken string `json:"accessToken"`
		} `json:"claudeAiOauth"`
	}

	if runtime.GOOS == "darwin" {
		out, err := exec.Command("security", "find-generic-password",
			"-s", "Claude Code-credentials", "-w").Output()
		if err == nil {
			var c creds
			if json.Unmarshal([]byte(strings.TrimSpace(string(out))), &c) == nil &&
				c.ClaudeAiOauth.AccessToken != "" {
				return c.ClaudeAiOauth.AccessToken
			}
		}
	}

	var c creds
	if readJSONFile(filepath.Join(homeDir(), ".claude", ".credentials.json"), &c) == nil {
		return c.ClaudeAiOauth.AccessToken
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

// googleCreds is the standard google-auth credential shape written by the
// Gemini CLI to ~/.gemini/oauth_creds.json. The access token is short-lived;
// when expired we exchange refresh_token for a fresh one.
type googleCreds struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiryDate   int64  `json:"expiry_date"` // Unix ms
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
}

// geminiCreds loads the Gemini CLI credential file.
func geminiCreds() (googleCreds, bool) {
	var c googleCreds
	if readJSONFile(filepath.Join(homeDir(), ".gemini", "oauth_creds.json"), &c) == nil && c.RefreshToken != "" {
		return c, true
	}
	return googleCreds{}, false
}
