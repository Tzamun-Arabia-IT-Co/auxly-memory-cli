package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// sshKeyscanTimeout bounds ssh-keyscan so a firewalled/unreachable host fails
// fast instead of hanging the invite/join flow.
const sshKeyscanTimeout = 8 * time.Second

// preferredHostKeyTypes is the pin/connect key-type preference: ed25519
// first (modern sshd default), rsa as a fallback for older servers. Only ONE
// type is requested per ssh-keyscan attempt — the same type is then forced
// via HostKeyAlgorithms when connecting (see pinnedSSHArgs), so a MITM can
// never dodge a pin by offering a different, unverified key type at connect
// time (key-type coherence).
var preferredHostKeyTypes = []string{"ed25519", "rsa"}

// hostKeyPin is one captured host key: its SSH algorithm name
// ("ssh-ed25519" / "ssh-rsa"), base64 key material (for a known_hosts line),
// and SHA256 fingerprint (for the invite's pin comparison).
type hostKeyPin struct {
	Algo        string
	KeyB64      string
	Fingerprint string
}

// hostKeyFingerprint returns the SHA256 fingerprint (ssh-keygen -lf format,
// e.g. "SHA256:abc...") of host:port's SSH host key, via ssh-keyscan.
// ssh-keyscan needs no credential — it just reads the identity offered during
// the SSH handshake — so this is exactly what `auxly host invite` computes for
// its OWN sshd (scanning "localhost") and what `auxly join` independently
// re-derives for the same host:port before trusting it. Neither side has to
// transmit anything but the resulting fingerprint text.
func hostKeyFingerprint(host string, port int) (string, error) {
	pin, err := scanHostKeyPin(host, port)
	if err != nil {
		return "", err
	}
	return pin.Fingerprint, nil
}

// scanHostKeyPin tries each of preferredHostKeyTypes in turn, restricting
// ssh-keyscan to exactly ONE type per attempt, and returns the pin for the
// first type that answers with a parseable key.
func scanHostKeyPin(host string, port int) (hostKeyPin, error) {
	var lastErr error
	for _, kt := range preferredHostKeyTypes {
		out, err := runKeyscan(host, port, kt)
		if err != nil {
			lastErr = err
			continue
		}
		pin, perr := parseKeyscanPin(out, kt)
		if perr != nil {
			lastErr = perr
			continue
		}
		return pin, nil
	}
	return hostKeyPin{}, fmt.Errorf("ssh-keyscan %s: no usable ed25519/rsa host key: %w", host, lastErr)
}

// runKeyscan runs ssh-keyscan restricted to ONE key type and returns its raw
// output, bounded by sshKeyscanTimeout.
func runKeyscan(host string, port int, keytype string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), sshKeyscanTimeout)
	defer cancel()

	args := []string{"-T", "5", "-t", keytype}
	if port != 0 && port != defaultSSHPort {
		args = append(args, "-p", strconv.Itoa(port))
	}
	args = append(args, host)

	out, err := exec.CommandContext(ctx, "ssh-keyscan", args...).Output()
	if err != nil {
		return "", fmt.Errorf("ssh-keyscan %s: %w", host, err)
	}
	return string(out), nil
}

// parseKeyscanPin parses ssh-keyscan output — lines shaped "<host> <keytype>
// <base64key>", plus "# ..." comment lines — restricted to keytype (e.g.
// requesting "ed25519" only matches "ssh-ed25519" lines). Lines of any OTHER
// key type are ignored even if present, so a scan requested as one type can
// never be satisfied by a different type slipping through — the key-type
// coherence CRITICAL 1's pin depends on. Pure (no subprocess), so unit-
// testable without a live ssh-keyscan.
func parseKeyscanPin(output, keytype string) (hostKeyPin, error) {
	wantAlgo := "ssh-" + keytype
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 || !strings.EqualFold(fields[1], wantAlgo) {
			continue
		}
		pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.Join(fields[1:], " ")))
		if err != nil {
			continue // not a key line we recognize — skip, don't fail the whole scan
		}
		return hostKeyPin{Algo: fields[1], KeyB64: fields[2], Fingerprint: ssh.FingerprintSHA256(pub)}, nil
	}
	return hostKeyPin{}, fmt.Errorf("no usable %s host key in ssh-keyscan output", wantAlgo)
}

// pinnedKnownHostsFile captures host:port's CURRENT SSH host key (ed25519
// preferred, rsa fallback — see scanHostKeyPin, one type only), verifies it
// matches wantFingerprint (the invite's pin — re-checked here, immediately
// before the caller connects, shrinking the TOCTOU window between
// joinPreflight's check and the secret-carrying exec to the length of one
// more ssh-keyscan), and writes it to a fresh 0600 temp known_hosts file in
// OpenSSH's own format. Returns the file's path, the key's algorithm name
// (for HostKeyAlgorithms), and a cleanup func the caller must run once done.
func pinnedKnownHostsFile(host string, port int, wantFingerprint string) (path, algo string, cleanup func(), err error) {
	noop := func() {}

	pin, err := scanHostKeyPin(host, port)
	if err != nil {
		return "", "", noop, err
	}
	if pin.Fingerprint != wantFingerprint {
		return "", "", noop, fmt.Errorf("host key changed since preflight (got %s, want %s) — refusing to connect: this could be a MITM", pin.Fingerprint, wantFingerprint)
	}

	f, err := os.CreateTemp("", "auxly-join-known-hosts-*")
	if err != nil {
		return "", "", noop, fmt.Errorf("create pinned known_hosts: %w", err)
	}
	path = f.Name()
	cleanup = func() { os.Remove(path) }

	if cerr := f.Chmod(0600); cerr != nil {
		f.Close()
		cleanup()
		return "", "", noop, fmt.Errorf("chmod pinned known_hosts: %w", cerr)
	}
	if _, werr := f.WriteString(knownHostsLine(host, port, pin.Algo, pin.KeyB64) + "\n"); werr != nil {
		f.Close()
		cleanup()
		return "", "", noop, fmt.Errorf("write pinned known_hosts: %w", werr)
	}
	if cerr := f.Close(); cerr != nil {
		cleanup()
		return "", "", noop, fmt.Errorf("close pinned known_hosts: %w", cerr)
	}
	return path, pin.Algo, cleanup, nil
}

// knownHostsLine renders one OpenSSH known_hosts entry for host:port:
// "[host]:port algo key" for a non-default port (OpenSSH's own bracketed
// form, so its known_hosts matcher recognizes it), bare "host algo key" for
// the default port.
func knownHostsLine(host string, port int, algo, keyB64 string) string {
	target := host
	if port != 0 && port != defaultSSHPort {
		target = fmt.Sprintf("[%s]:%d", host, port)
	}
	return target + " " + algo + " " + keyB64
}

// pinnedSSHArgs returns the -o overrides that pin an SSH connection to a
// specific host key: StrictHostKeyChecking=yes against ONLY the temp
// known_hosts file just written (no accept-new, no fallback to the user's
// real known_hosts), restricted to the exact key algorithm that was pinned
// so the client can't be talked into verifying one key type and connecting
// with another.
func pinnedSSHArgs(knownHostsPath, algo string) []string {
	return []string{
		"-o", "UserKnownHostsFile=" + knownHostsPath,
		"-o", "StrictHostKeyChecking=yes",
		"-o", "HostKeyAlgorithms=" + algo,
	}
}
