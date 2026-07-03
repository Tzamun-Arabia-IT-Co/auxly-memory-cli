package clipboard

import "testing"

// TestArgvFor exercises tool selection in isolation from what's actually
// installed on the test machine (real availability comes from exec.LookPath
// in Copy; here it's a stubbed set).
func TestArgvFor(t *testing.T) {
	has := func(names ...string) func(string) bool {
		set := map[string]bool{}
		for _, n := range names {
			set[n] = true
		}
		return func(n string) bool { return set[n] }
	}
	argvEq := func(got, want []string) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}

	tests := []struct {
		name string
		goos string
		have func(string) bool
		want []string
	}{
		{"darwin uses pbcopy", "darwin", has("pbcopy"), []string{"pbcopy"}},
		{"darwin with nothing on PATH", "darwin", has(), nil},
		{"windows uses clip.exe", "windows", has("clip.exe"), []string{"clip.exe"}},
		{"linux prefers wl-copy over xclip/xsel", "linux", has("wl-copy", "xclip", "xsel"), []string{"wl-copy"}},
		{"linux falls back to xclip when wl-copy missing", "linux", has("xclip", "xsel"), []string{"xclip", "-selection", "clipboard"}},
		{"linux falls back to xsel when only it is present", "linux", has("xsel"), []string{"xsel", "--clipboard", "--input"}},
		{"linux with nothing on PATH", "linux", has(), nil},
		{"unrecognized goos", "plan9", has("pbcopy"), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := argvFor(tt.goos, tt.have)
			if !argvEq(got, tt.want) {
				t.Errorf("argvFor(%q) = %v, want %v", tt.goos, got, tt.want)
			}
		})
	}
}
