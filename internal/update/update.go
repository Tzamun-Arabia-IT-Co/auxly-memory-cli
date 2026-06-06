// Package update handles version reporting, "is a newer release available?"
// checks (cached), and in-place self-update by downloading the matching binary
// from the distribution host. Shared by the CLI (cmd) and the TUI (tui).
package update

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// versionRe matches a bare semver-ish string ("1", "1.2", "1.2.3", "v1.2.3",
// "1.2.3-rc1"). It guards against an endpoint that returns HTML/JSON/404 pages
// (e.g. a SPA serving index.html for an unknown /version path).
var versionRe = regexp.MustCompile(`^v?[0-9]+(\.[0-9]+){0,3}([-+][0-9A-Za-z.]+)?$`)

// Current is the running build version, injected at release time via
//
//	-ldflags "-X github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/update.Current=x.y.z"
//
// It defaults to the current release version for plain `go build` / source /
// `go install` builds (which carry no ldflags). Bump this together with the
// VERSION file and the git tag on every release.
var Current = "1.0.19"

const checkInterval = 24 * time.Hour

// BaseURL is the distribution host. Overridable for testing/self-hosting.
//
// M4: a poisoned or inherited AUXLY_INSTALL_BASE must not silently downgrade
// update/install traffic to http. The override is honored only when it is HTTPS,
// targets localhost (for local testing), or AUXLY_INSECURE_INSTALL=1 is set
// explicitly; otherwise we fall back to the secure default.
func BaseURL() string {
	if v := strings.TrimSpace(os.Getenv("AUXLY_INSTALL_BASE")); v != "" {
		v = strings.TrimRight(v, "/")
		if isSecureInstallBase(v) {
			return v
		}
	}
	return "https://auxly.io"
}

func isSecureInstallBase(v string) bool {
	if os.Getenv("AUXLY_INSECURE_INSTALL") == "1" {
		return true
	}
	u, err := url.Parse(v)
	if err != nil {
		return false
	}
	if strings.EqualFold(u.Scheme, "https") {
		return true
	}
	// Plain http is allowed ONLY for an exact loopback host (local dev). Compare the
	// parsed hostname exactly — a prefix check would let http://localhost.evil.tld
	// through.
	host := strings.ToLower(u.Hostname())
	return strings.EqualFold(u.Scheme, "http") && (host == "localhost" || host == "127.0.0.1" || host == "::1")
}

// Latest fetches the newest published version string from {base}/version.
// The endpoint returns a bare version (e.g. "1.2.0"); failures are returned so
// callers can stay silent rather than alarm the user.
func Latest() (string, error) {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(BaseURL() + "/version")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("version check: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return "", err
	}
	v := strings.TrimSpace(string(body))
	if !versionRe.MatchString(v) {
		// Not a version — almost certainly an HTML/404 page from a host that
		// doesn't serve /version yet. Treat as "no version published".
		return "", fmt.Errorf("version check: unexpected response %q", truncate(v, 24))
	}
	return strings.TrimPrefix(v, "v"), nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// IsNewer reports whether version `latest` is strictly greater than `current`,
// comparing dotted numeric components (1.10.0 > 1.9.0). Non-numeric or
// unparseable versions fall back to a plain inequality check.
func IsNewer(latest, current string) bool {
	la := parse(latest)
	cu := parse(current)
	if la == nil || cu == nil {
		// Unparseable input is never treated as a newer version — prevents a
		// stray/garbage string from triggering a false "update available".
		return false
	}
	for i := 0; i < len(la) && i < len(cu); i++ {
		if la[i] != cu[i] {
			return la[i] > cu[i]
		}
	}
	return len(la) > len(cu)
}

func parse(v string) []int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		// Tolerate suffixes like "1.2.0-rc1" by cutting at the first non-digit.
		end := 0
		for end < len(p) && p[end] >= '0' && p[end] <= '9' {
			end++
		}
		if end == 0 {
			return nil
		}
		n, err := strconv.Atoi(p[:end])
		if err != nil {
			return nil
		}
		out = append(out, n)
	}
	return out
}

// Available returns the latest version and whether it is newer than the running
// build. It serves a cached result when fresh (within checkInterval) and
// otherwise performs one short synchronous fetch, refreshing the cache. On any
// network failure it degrades silently to (current, false).
func Available() (string, bool) {
	latest, ok := readCache()
	if !ok {
		fetched, err := Latest()
		if err == nil && fetched != "" {
			writeCache(fetched)
			latest = fetched
		}
	}
	if latest == "" {
		return "", false
	}
	if IsNewer(latest, Current) {
		return latest, true
	}
	return "", false
}

// Cached reports the cached latest version and whether it is newer than the running
// build, performing NO network call — safe for hot paths like the statusline that must
// never block or hit the network. It intentionally ignores cache freshness: a slightly
// stale "update available" hint is acceptable and self-heals the next time a networked
// path (CLI startup / TUI) refreshes the cache via Available(). Returns ("", false) when
// there is no cache or no newer version.
func Cached() (latest string, newer bool) {
	latest, _ = readCache()
	if latest == "" || !IsNewer(latest, Current) {
		return "", false
	}
	return latest, true
}

type cacheFile struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest"`
}

func cachePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".auxly", ".update-check.json"), nil
}

// readCache returns the cached latest version and whether it is still fresh.
func readCache() (string, bool) {
	p, err := cachePath()
	if err != nil {
		return "", false
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	var c cacheFile
	if json.Unmarshal(data, &c) != nil {
		return "", false
	}
	return c.Latest, time.Since(c.CheckedAt) < checkInterval
}

func writeCache(latest string) {
	p, err := cachePath()
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	data, err := json.Marshal(cacheFile{CheckedAt: time.Now(), Latest: latest})
	if err != nil {
		return
	}
	_ = os.WriteFile(p, data, 0o644)
}

// SelfUpdate downloads the matching binary from the distribution host and
// atomically replaces the running executable. Returns the resolved path. It
// prints nothing, so it is safe to call from the TUI.
func SelfUpdate() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("could not locate the running binary: %w", err)
	}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}

	url := fmt.Sprintf("%s/dl/auxly-%s-%s", BaseURL(), runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		url += ".exe"
	}

	resp, err := http.Get(url)
	if err != nil {
		return "", fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download failed: HTTP %d for %s", resp.StatusCode, url)
	}

	tmp, err := os.CreateTemp(filepath.Dir(exe), ".auxly-update-*")
	if err != nil {
		return "", fmt.Errorf("could not create temp file next to %s (permissions?): %w", exe, err)
	}
	tmpPath := tmp.Name()
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("failed to write update: %w", err)
	}
	tmp.Close()
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		os.Remove(tmpPath)
		return "", err
	}
	if err := os.Rename(tmpPath, exe); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("could not replace %s (try re-running with sudo): %w", exe, err)
	}
	_ = exec.Command("xattr", "-c", exe).Run() // best-effort: clear macOS quarantine
	return exe, nil
}
