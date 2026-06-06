package update

import "testing"

func TestBaseURL_RejectsInsecureHTTPOverride(t *testing.T) {
	t.Setenv("AUXLY_INSTALL_BASE", "http://evil.example.com")
	if got := BaseURL(); got != "https://auxly.io" {
		t.Fatalf("insecure http override must fall back to the secure default, got %q", got)
	}
}

func TestBaseURL_HonorsHTTPSOverride(t *testing.T) {
	t.Setenv("AUXLY_INSTALL_BASE", "https://mirror.example.com/")
	if got := BaseURL(); got != "https://mirror.example.com" {
		t.Fatalf("https override should be honored (trailing slash trimmed), got %q", got)
	}
}

func TestBaseURL_AllowsLocalhostHTTPForDev(t *testing.T) {
	t.Setenv("AUXLY_INSTALL_BASE", "http://localhost:8080")
	if got := BaseURL(); got != "http://localhost:8080" {
		t.Fatalf("localhost http should be allowed for local dev, got %q", got)
	}
}

func TestBaseURL_RejectsLocalhostLookalikeHost(t *testing.T) {
	// http://localhost.evil.example must NOT pass as "localhost" (prefix bypass).
	for _, evil := range []string{"http://localhost.evil.example", "http://127.0.0.1.evil.example/dl"} {
		t.Setenv("AUXLY_INSTALL_BASE", evil)
		if got := BaseURL(); got != "https://auxly.io" {
			t.Fatalf("lookalike host %q must be rejected, got %q", evil, got)
		}
	}
}

func TestBaseURL_InsecureOptInAllowsHTTP(t *testing.T) {
	t.Setenv("AUXLY_INSTALL_BASE", "http://192.168.1.50:9000")
	t.Setenv("AUXLY_INSECURE_INSTALL", "1")
	if got := BaseURL(); got != "http://192.168.1.50:9000" {
		t.Fatalf("explicit insecure opt-in should honor http override, got %q", got)
	}
}
