package cmd

import (
	"encoding/base64"
	"fmt"
	"strings"
	"sync"
	"unicode/utf16"
)

var (
	osDetectMu    sync.Mutex
	osDetectCache = map[string]remoteOS{}
)

type remoteOS int

const (
	osUnknown remoteOS = iota
	osUnix
	osWindows
)

// classifyOS maps a declared profile OS string to the command family used over SSH.
func classifyOS(declared string) remoteOS {
	switch strings.ToLower(strings.TrimSpace(declared)) {
	case "":
		return osUnknown
	case "windows", "win", "win32", "win64":
		return osWindows
	case "linux", "darwin", "macos", "freebsd", "openbsd", "netbsd", "dragonfly", "sunos", "solaris", "aix", "unix", "posix":
		return osUnix
	default:
		return osUnknown
	}
}

// detectRemoteOS determines which remote shell family should be used.
//
// If the profile declares an OS, that declaration is trusted and no live probe is
// performed. Otherwise this probes for Unix first with `uname -sm`, then Windows
// with `cmd /c ver`.
//
// Reference translations for callers:
//
//	// Reachback probe:
//	posix := "nc -z -w3 " + shellQuote(ip) + " 22 || timeout 3 bash -c " + shellQuote("echo > /dev/tcp/"+ip+"/22")
//	powershell := "if((Test-NetConnection -ComputerName " + psQuote(ip) + " -Port 22 -WarningAction SilentlyContinue).TcpTestSucceeded){exit 0}else{exit 1}"
//	_, err := runRemoteScript(p, fam, posix, powershell)
//
//	// Auxly liveness:
//	posix := "pgrep -f " + shellQuote("auxly mcp-server")
//	powershell := "if(Get-Process auxly -ErrorAction SilentlyContinue){'LIVE'}"
//	out, err := runRemoteScript(p, fam, posix, powershell)
func detectRemoteOS(p remoteProfile) (remoteOS, string, error) {
	// Declared OS: validate and return without probing or caching (already free).
	if declared := strings.TrimSpace(p.OS); declared != "" {
		fam := classifyOS(declared)
		if fam == osUnknown {
			return osUnknown, "", fmt.Errorf("unrecognized declared OS %q in profile", declared)
		}
		return fam, fmt.Sprintf("profile OS declared as %q", declared), nil
	}

	// Check memo cache (only when the profile has a name to key on).
	if p.Name != "" {
		osDetectMu.Lock()
		cached, ok := osDetectCache[p.Name]
		osDetectMu.Unlock()
		if ok {
			return cached, "cached", nil
		}
	}

	// Live probe: Unix first.
	uname, err := runSSH(p, "uname", "-sm")
	if err == nil {
		uname = strings.TrimSpace(uname)
		if looksLikeUnixUname(uname) {
			if p.Name != "" {
				osDetectMu.Lock()
				osDetectCache[p.Name] = osUnix
				osDetectMu.Unlock()
			}
			return osUnix, uname, nil
		}

		err = fmt.Errorf("unix probe returned unrecognized output: %q", uname)
	} else {
		err = fmt.Errorf("unix probe failed: %w", err)
	}

	// Live probe: Windows fallback.
	ver, winErr := runSSH(p, "cmd", "/c", "ver")
	if winErr == nil {
		ver = strings.TrimSpace(ver)
		if strings.Contains(strings.ToLower(ver), "windows") {
			if p.Name != "" {
				osDetectMu.Lock()
				osDetectCache[p.Name] = osWindows
				osDetectMu.Unlock()
			}
			return osWindows, ver, nil
		}

		return osUnknown, ver, fmt.Errorf("windows probe returned unrecognized output: %q", ver)
	}

	return osUnknown, "", fmt.Errorf("windows probe failed after %v: %w", err, winErr)
}

// runRemoteScript runs the same logical script on a Unix or Windows SSH target.
//
// For Unix targets, the POSIX script is passed as one already-quoted argv element
// to `sh -c`. This is safer than building "'"+script+"'" by hand because
// shellQuote is the single escaping boundary and correctly handles embedded
// single quotes.
//
// For Windows targets, the PowerShell script is UTF-16LE base64 encoded for
// -EncodedCommand, which avoids cmd.exe-over-ssh quoting issues.
func runRemoteScript(p remoteProfile, fam remoteOS, posix, powershell string) (string, error) {
	switch fam {
	case osWindows:
		if powershell == "" {
			return "", fmt.Errorf("no PowerShell rendering provided for windows target")
		}

		out, err := runSSH(p, "powershell", "-NoProfile", "-NonInteractive", "-EncodedCommand", psEncode(powershell))
		if err != nil {
			return "", fmt.Errorf("run windows remote script: %w", err)
		}
		return out, nil

	case osUnix, osUnknown:
		out, err := runSSH(p, "sh", "-c", shellQuote(posix))
		if err != nil {
			return "", fmt.Errorf("run unix remote script: %w", err)
		}
		return out, nil

	default:
		return "", fmt.Errorf("unsupported remote OS family: %d", fam)
	}
}

// psEncode returns the UTF-16LE base64 form required by powershell -EncodedCommand.
func psEncode(script string) string {
	words := utf16.Encode([]rune(script))
	buf := make([]byte, 0, len(words)*2)

	for _, word := range words {
		buf = append(buf, byte(word), byte(word>>8))
	}

	return base64.StdEncoding.EncodeToString(buf)
}

// psQuote returns a single-quoted PowerShell string literal.
func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// winInstallCmd returns the PowerShell one-liner that installs/updates auxly on a
// Windows host. It forces TLS 1.2 first because pre-1903 Windows / Server 2016
// default to TLS 1.0/1.1, which makes the HTTPS download fail silently.
func winInstallCmd(psURL string) string {
	return "[Net.ServicePointManager]::SecurityProtocol=[Net.SecurityProtocolType]::Tls12; irm " + psURL + " | iex"
}

func looksLikeUnixUname(out string) bool {
	if out == "" {
		return false
	}

	lower := strings.ToLower(out)
	if strings.Contains(lower, "windows") {
		return false
	}

	for _, marker := range []string{
		"linux",
		"darwin",
		"freebsd",
		"openbsd",
		"netbsd",
		"dragonfly",
		"sunos",
		"solaris",
		"aix",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}

	return false
}
