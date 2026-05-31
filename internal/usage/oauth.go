package usage

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Antigravity does not write a readable Google credential of its own, so to show
// its usage Auxly performs its own one-time OAuth consent (a loopback + PKCE
// flow — the same the reference Stream Deck plugin uses) and stores the result
// under ~/.auxly/. The token is only ever used to call Google's own Code Assist
// endpoint for this user's account.

const (
	antigravityClientID     = "REDACTED_ANTIGRAVITY_CLIENT_ID"
	antigravityClientSecret = "REDACTED_ANTIGRAVITY_CLIENT_SECRET"
)

var antigravityScopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
}

// antigravityTokenPath is where the consent flow persists its token (mode 0600).
func antigravityTokenPath() string {
	return filepath.Join(homeDir(), ".auxly", "antigravity_oauth.json")
}

// antigravityCreds loads the token written by AntigravityLogin.
func antigravityCreds() (googleCreds, bool) {
	var c googleCreds
	if readJSONFile(antigravityTokenPath(), &c) == nil && c.RefreshToken != "" {
		return c, true
	}
	return googleCreds{}, false
}

// AntigravityLogin runs the interactive consent flow: it spins up a loopback
// listener, opens the browser to Google's consent screen, captures the returned
// code, exchanges it (with the PKCE verifier) for tokens, and saves them. It
// blocks until consent completes or ctx is cancelled. Returns the signed-in
// email on success.
func AntigravityLogin(ctx context.Context) (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("start loopback listener: %w", err)
	}
	defer ln.Close()
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", ln.Addr().(*net.TCPAddr).Port)

	verifier := randString(64)
	challenge := pkceChallenge(verifier)
	state := randString(24)

	authURL := "https://accounts.google.com/o/oauth2/v2/auth?" + url.Values{
		"client_id":             {antigravityClientID},
		"redirect_uri":          {redirectURI},
		"response_type":         {"code"},
		"scope":                 {strings.Join(antigravityScopes, " ")},
		"access_type":           {"offline"},
		"prompt":                {"consent"},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}.Encode()

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		q := req.URL.Query()
		if q.Get("state") != state {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			errCh <- fmt.Errorf("oauth state mismatch")
			return
		}
		if e := q.Get("error"); e != "" {
			fmt.Fprintf(w, "Authorization failed: %s. You can close this tab.", e)
			errCh <- fmt.Errorf("consent denied: %s", e)
			return
		}
		fmt.Fprint(w, "Antigravity connected to Auxly. You can close this tab and return to the terminal.")
		codeCh <- q.Get("code")
	})}
	go srv.Serve(ln) //nolint:errcheck // shutdown handled below
	defer srv.Close()

	if err := openBrowser(authURL); err != nil {
		fmt.Printf("Open this URL to authorize Antigravity:\n\n%s\n\n", authURL)
	}

	var code string
	select {
	case code = <-codeCh:
	case err := <-errCh:
		return "", err
	case <-ctx.Done():
		return "", ctx.Err()
	}

	creds, email, err := exchangeAntigravityCode(ctx, code, verifier, redirectURI)
	if err != nil {
		return "", err
	}
	creds.Email = email // persist for the Usage view's account line
	if err := saveAntigravityCreds(creds); err != nil {
		return "", err
	}
	return email, nil
}

func exchangeAntigravityCode(ctx context.Context, code, verifier, redirectURI string) (googleCreds, string, error) {
	form := url.Values{
		"client_id":     {antigravityClientID},
		"client_secret": {antigravityClientSecret},
		"code":          {code},
		"code_verifier": {verifier},
		"grant_type":    {"authorization_code"},
		"redirect_uri":  {redirectURI},
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://oauth2.googleapis.com/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpClient.Do(req)
	if err != nil {
		return googleCreds{}, "", fmt.Errorf("token exchange: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return googleCreds{}, "", fmt.Errorf("token exchange failed (%d)", resp.StatusCode)
	}
	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		TokenType    string `json:"token_type"`
		Scope        string `json:"scope"`
		IDToken      string `json:"id_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return googleCreds{}, "", fmt.Errorf("decode token response: %w", err)
	}
	if tok.RefreshToken == "" {
		return googleCreds{}, "", fmt.Errorf("no refresh token returned (try revoking prior access and retry)")
	}
	return googleCreds{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		ExpiryDate:   time.Now().UnixMilli() + tok.ExpiresIn*1000,
		TokenType:    tok.TokenType,
		Scope:        tok.Scope,
	}, emailFromIDToken(tok.IDToken), nil
}

func saveAntigravityCreds(c googleCreds) error {
	dir := filepath.Dir(antigravityTokenPath())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(antigravityTokenPath(), b, 0o600)
}

// emailFromIDToken pulls the email claim from an unverified id_token (display
// only; never used for auth decisions).
func emailFromIDToken(idToken string) string {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Email string `json:"email"`
	}
	_ = json.Unmarshal(payload, &claims)
	return claims.Email
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func randString(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)[:n]
}

func openBrowser(target string) error {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd, args = "open", []string{target}
	case "windows":
		cmd, args = "rundll32", []string{"url.dll,FileProtocolHandler", target}
	default:
		cmd, args = "xdg-open", []string{target}
	}
	return exec.Command(cmd, args...).Start()
}
