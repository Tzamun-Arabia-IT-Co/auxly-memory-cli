package session

import "testing"

func TestInferProvider(t *testing.T) {
	tests := []struct {
		name      string
		ancestors []string
		want      string
	}{
		{
			name:      "antigravity ide via app bundle path",
			ancestors: []string{"/Applications/Antigravity.app/Contents/Resources/bin/language_server", "/Applications/Antigravity.app/Contents/MacOS/Antigravity"},
			want:      "antigravity-agent",
		},
		{
			name:      "antigravity ide explicit ide bundle",
			ancestors: []string{"/Applications/Antigravity IDE.app/Contents/MacOS/stub"},
			want:      "antigravity-ide",
		},
		{
			name:      "gemini cli binary",
			ancestors: []string{"/opt/homebrew/bin/gemini"},
			want:      "gemini",
		},
		{
			name:      "claude desktop app bundle",
			ancestors: []string{"/Applications/Claude.app/Contents/MacOS/Claude"},
			want:      "claude",
		},
		{
			name:      "claude code cli",
			ancestors: []string{"/Users/x/.nvm/versions/node/v22/bin/claude"},
			want:      "claude-code",
		},
		{
			name:      "cursor app",
			ancestors: []string{"/Applications/Cursor.app/Contents/MacOS/Cursor"},
			want:      "cursor",
		},
		{
			name:      "codex app",
			ancestors: []string{"/Applications/Codex.app/Contents/MacOS/Codex"},
			want:      "codex",
		},
		{
			// Windsurf launching a generic helper is NOT attributable to a
			// brand by ancestry alone — it must come from AUXLY_PROVIDER. We
			// must return "" rather than guess.
			name:      "unknown host returns empty",
			ancestors: []string{"/Applications/Windsurf.app/Contents/Frameworks/Windsurf Helper (Plugin).app/Contents/MacOS/Windsurf Helper (Plugin)"},
			want:      "",
		},
		{
			name:      "empty chain returns empty",
			ancestors: nil,
			want:      "",
		},
		{
			// Nearest matching ancestor wins; the launcher shell is skipped.
			name:      "nearest brand ancestor wins over deeper login shell",
			ancestors: []string{"/bin/zsh", "/Applications/Cursor.app/Contents/MacOS/Cursor"},
			want:      "cursor",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := InferProvider(tt.ancestors); got != tt.want {
				t.Errorf("InferProvider(%v) = %q, want %q", tt.ancestors, got, tt.want)
			}
		})
	}
}
