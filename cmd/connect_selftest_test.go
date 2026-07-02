package cmd

import (
	"testing"
	"time"
)

func TestClassifySelftestFailure(t *testing.T) {
	tests := []struct {
		name     string
		exitCode int
		isExit   bool
		sawJSON  bool
		phase    string
		want     string
	}{
		{name: "ssh transport no json", exitCode: 255, isExit: true, sawJSON: false, phase: "handshake", want: "ssh"},
		{name: "missing host binary no json", exitCode: 127, isExit: true, sawJSON: false, phase: "handshake", want: "hostbin"},
		{name: "ran but no handshake", exitCode: 1, isExit: true, sawJSON: true, phase: "handshake", want: "server"},
		{name: "read failure after handshake", exitCode: 1, isExit: true, sawJSON: true, phase: "read", want: "read"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifySelftestFailure(tt.exitCode, tt.isExit, tt.sawJSON, tt.phase)
			if got != tt.want {
				t.Fatalf("classifySelftestFailure() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatSelftestResult(t *testing.T) {
	tests := []struct {
		name    string
		files   int
		elapsed time.Duration
		want    string
	}{
		{name: "fast", files: 3, elapsed: 4999 * time.Millisecond, want: "OK (3 files)"},
		{name: "slow boundary", files: 4, elapsed: 5 * time.Second, want: "SLOW (4 files, took 5.0s)"},
		{name: "slow tenth", files: 5, elapsed: 5500 * time.Millisecond, want: "SLOW (5 files, took 5.5s)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatSelftestResult(tt.files, tt.elapsed)
			if got != tt.want {
				t.Fatalf("formatSelftestResult() = %q, want %q", got, tt.want)
			}
		})
	}
}
