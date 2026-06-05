package cmd

import (
	"strings"
	"testing"
)

// TestPsEncode validates UTF-16LE base64 encoding against known-good PowerShell
// vectors produced by:
//
//	[Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes("..."))
//
// These expected values are authoritative. If this test fails, psEncode is broken —
// do NOT change the expected values.
func TestPsEncode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "whoami",
			input: "whoami",
			want:  "dwBoAG8AYQBtAGkA",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "single letter A",
			input: "A",
			want:  "QQA=",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Arrange — input is tc.input, want is tc.want

			// Act
			got := psEncode(tc.input)

			// Assert
			if got != tc.want {
				t.Errorf("CRITICAL: psEncode(%q)\n  got  %q\n  want %q\n  (expected value is authoritative PowerShell output — fix psEncode, not this test)", tc.input, got, tc.want)
			}
		})
	}
}

// TestClassifyOS verifies that declared OS strings are mapped to the correct
// remoteOS family.
func TestClassifyOS(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
		want  remoteOS
	}{
		// Windows variants
		{name: "windows lowercase", input: "windows", want: osWindows},
		{name: "Windows title case", input: "Windows", want: osWindows},
		{name: "WIN uppercase", input: "WIN", want: osWindows},
		{name: "win64", input: "win64", want: osWindows},

		// Unix variants
		{name: "linux", input: "linux", want: osUnix},
		{name: "Linux title case", input: "Linux", want: osUnix},
		{name: "darwin", input: "darwin", want: osUnix},
		{name: "macos", input: "macos", want: osUnix},
		{name: "freebsd", input: "freebsd", want: osUnix},
		{name: "unix", input: "unix", want: osUnix},

		// Unknown
		{name: "empty string", input: "", want: osUnknown},
		{name: "freenas", input: "freenas", want: osUnknown},
		{name: "plan9", input: "plan9", want: osUnknown},
		{name: "banana", input: "banana", want: osUnknown},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Arrange — input is tc.input

			// Act
			got := classifyOS(tc.input)

			// Assert
			if got != tc.want {
				t.Errorf("classifyOS(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

// TestPsQuote verifies single-quoting and internal single-quote escaping for
// PowerShell string literals.
func TestPsQuote(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
		want  string
	}{
		{name: "simple word", input: "x", want: "'x'"},
		{name: "embedded single quote", input: "a'b", want: "'a''b'"},
		{name: "empty string", input: "", want: "''"},
		{name: "sentence with apostrophe", input: "it's a test", want: "'it''s a test'"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Arrange — input is tc.input

			// Act
			got := psQuote(tc.input)

			// Assert
			if got != tc.want {
				t.Errorf("psQuote(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestLooksLikeUnixUname verifies detection of valid uname output vs Windows
// version strings and empty input.
func TestLooksLikeUnixUname(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
		want  bool
	}{
		{name: "Linux x86_64", input: "Linux x86_64", want: true},
		{name: "Darwin arm64", input: "Darwin arm64", want: true},
		{name: "FreeBSD amd64", input: "FreeBSD amd64", want: true},
		{name: "Windows version string", input: "Microsoft Windows [Version 10.0.26100]", want: false},
		{name: "empty string", input: "", want: false},
		{name: "bare Windows word", input: "Windows", want: false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Arrange — input is tc.input

			// Act
			got := looksLikeUnixUname(tc.input)

			// Assert
			if got != tc.want {
				t.Errorf("looksLikeUnixUname(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

// TestWinInstallCmd verifies that the generated PowerShell one-liner contains
// the required TLS hardening, the source URL, and the iex invocation.
func TestWinInstallCmd(t *testing.T) {
	t.Parallel()

	// Arrange
	url := "https://auxly.io/cli.ps1"

	// Act
	got := winInstallCmd(url)

	// Assert
	checks := []struct {
		desc    string
		needle  string
		present bool
	}{
		{"TLS 1.2 hardening", "Tls12", true},
		{"irm with URL", "irm " + url, true},
		{"iex invocation", "iex", true},
	}

	for _, c := range checks {
		t.Run(c.desc, func(t *testing.T) {
			if strings.Contains(got, c.needle) != c.present {
				if c.present {
					t.Errorf("winInstallCmd(%q) missing %q\n  got: %q", url, c.needle, got)
				} else {
					t.Errorf("winInstallCmd(%q) unexpectedly contains %q\n  got: %q", url, c.needle, got)
				}
			}
		})
	}
}
