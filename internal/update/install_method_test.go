package update

import (
	"runtime"
	"testing"
)

func TestClassifyInstallPath(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		// npm: binary vendored under node_modules/auxly-cli/vendor/
		{`C:\Users\Sam\AppData\Roaming\npm\node_modules\auxly-cli\vendor\auxly.exe`, "npm"},
		{`/usr/local/lib/node_modules/auxly-cli/vendor/auxly`, "npm"},
		// pip: binary cached under <cache>/auxly/
		{`C:\Users\Sam\.cache\auxly\auxly.exe`, "pip"},
		{`/home/sam/.cache/auxly/auxly`, "pip"},
		// plain binary installs self-update normally → ""
		{`C:\Users\Sam\AppData\Local\Programs\auxly\auxly.exe`, ""},
		{`/usr/local/bin/auxly`, ""},
		{`/Users/sam/.local/bin/auxly`, ""},
	}
	for _, c := range cases {
		if got := classifyInstallPath(c.path); got != c.want {
			t.Errorf("classifyInstallPath(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

func TestManagedUpdateHint(t *testing.T) {
	if h := ManagedUpdateHint("npm"); h != "npm install -g auxly-cli@latest" {
		t.Errorf("npm hint wrong: %q", h)
	}
	if h := ManagedUpdateHint("pip"); h != "pip install -U auxly-cli" {
		t.Errorf("pip hint wrong: %q", h)
	}
	if h := ManagedUpdateHint(""); h != "" {
		t.Errorf("plain install must have no managed hint, got %q", h)
	}
}

func TestInstallerCommandPerOS(t *testing.T) {
	got := InstallerCommand()
	if runtime.GOOS == "windows" {
		if got != "irm https://auxly.io/cli.ps1 | iex" {
			t.Errorf("windows installer hint wrong: %q", got)
		}
	} else if got != "curl -sSL https://auxly.io/cli | sh" {
		t.Errorf("unix installer hint wrong: %q", got)
	}
}
