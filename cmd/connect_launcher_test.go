package cmd

import "testing"

// TestShouldRetryLauncher is the guard for connect-mcp resilience: retry ONLY on
// an ssh transport failure (exit 255 — remote mcp-server never ran) while
// attempts remain. Never on a non-255 exit (remote ran; stdin consumed; retry
// would corrupt the stateful MCP stream), never on a non-exit error, and never
// on the last attempt.
func TestShouldRetryLauncher(t *testing.T) {
	cases := []struct {
		name        string
		exitCode    int
		isExit      bool
		attempt     int
		maxAttempts int
		want        bool
	}{
		{"transport failure, attempts left", 255, true, 1, 3, true},
		{"transport failure, mid attempts", 255, true, 2, 3, true},
		{"transport failure, last attempt", 255, true, 3, 3, false},
		{"remote ran then failed (exit 1)", 1, true, 1, 3, false},
		{"remote clean-ish non-255 (exit 2)", 2, true, 1, 3, false},
		{"not a process exit (ssh failed to start)", 0, false, 1, 3, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldRetryLauncher(tc.exitCode, tc.isExit, tc.attempt, tc.maxAttempts)
			if got != tc.want {
				t.Errorf("shouldRetryLauncher(%d, %v, %d, %d) = %v, want %v",
					tc.exitCode, tc.isExit, tc.attempt, tc.maxAttempts, got, tc.want)
			}
		})
	}
}

func TestIsHostBinNotFound(t *testing.T) {
	cases := []struct {
		name     string
		exitCode int
		isExit   bool
		want     bool
	}{
		{"posix command not found (127)", 127, true, true},
		{"windows cmd command not found (9009)", 9009, true, true},
		{"windows powershell / general missing exit (1)", 1, true, true},
		{"ssh transport failure (255)", 255, true, false},
		{"clean exit (0)", 0, true, false},
		{"not an exit error", 0, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isHostBinNotFound(tc.exitCode, tc.isExit)
			if got != tc.want {
				t.Errorf("isHostBinNotFound(%d, %v) = %v, want %v", tc.exitCode, tc.isExit, got, tc.want)
			}
		})
	}
}

func TestIsReparableHostBin(t *testing.T) {
	cases := []struct {
		bin  string
		want bool
	}{
		{"/usr/local/bin/auxly", true},
		{"$HOME/.bun/bin/auxly", true},
		{`C:\Users\User\AppData\Local\Programs\auxly\auxly.exe`, true},
		{`%LOCALAPPDATA%\Programs\auxly\auxly.exe`, true},
		{"$env:LOCALAPPDATA\\Programs\\auxly\\auxly.exe", true},
		{"auxly", false},
		{"auxly.exe", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.bin, func(t *testing.T) {
			got := isReparableHostBin(tc.bin)
			if got != tc.want {
				t.Errorf("isReparableHostBin(%q) = %v, want %v", tc.bin, got, tc.want)
			}
		})
	}
}

func TestHostBinCandidates(t *testing.T) {
	p := remoteProfile{Name: "winbox"}
	cands := hostBinCandidates(p)
	foundWinPath := false
	for _, c := range cands {
		if c == `%LOCALAPPDATA%\Programs\auxly\auxly.exe` || c == "auxly.exe" {
			foundWinPath = true
			break
		}
	}
	if !foundWinPath {
		t.Fatalf("hostBinCandidates() = %v, expected to include Windows install path candidates", cands)
	}

	// Profile with configured HostBin
	p2 := remoteProfile{Name: "custom", HostBin: `C:\Custom\auxly.exe`}
	cands2 := hostBinCandidates(p2)
	if len(cands2) == 0 || cands2[0] != `C:\Custom\auxly.exe` {
		t.Fatalf("hostBinCandidates(p2)[0] = %q, want custom HostBin", cands2[0])
	}
}

