package cmd

import (
	"strings"
	"testing"
)

// testEd25519KeyLine and testEd25519Fingerprint are a real, fixed keypair
// (generated once with `ssh-keygen -t ed25519`) used to pin the parser
// against ssh-keygen's own fingerprint output, not a hand-computed value.
//
// testRSAKeyLine / testRSAFingerprint are a second, real, fixed keypair
// (`ssh-keygen -t rsa`) of a DIFFERENT type, used to prove parseKeyscanPin
// only ever matches the requested type out of mixed-type output.
const (
	testEd25519KeyLine     = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHLnMEzSu21IdYcoBAF9FSGIlo2kBueKFn7+pQfBfIb3"
	testEd25519Fingerprint = "SHA256:ZRl2JIZ3ESsh7b+YmDsiBwi+wIMpBKQ+qauno1/vYvE"

	testRSAKeyLine     = "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQC4F0RuaHlNSoo/4HyzvlEIW2zwMWlwxXzxohEMBXHnMai//j4CPD42CyDtY5YEWjSfTXIOGMqOaU8i7/S3ayenH/ey/58qAOVncRsboQTLSyTO1kBT6ikFfdMLtvrORsj/5BHzI91RNeXvaOgxSvBsr9vfofx3MFiU4HpFUBBfZwvdTpF+4+iKnMstM8vWxTnqidoSSdaDTbf+oEJBDPLTYniT91GmyNEXJdN1yvptdam322jtEbumYkk1JLSFC5nsOYUTrtygeaZFhNG2x1SIg+HhTQMncDSLEQEvO5EdRGthlqs9rDoyajIaf6yFahsuniyFk9/b35POCrp/YV6v"
	testRSAFingerprint = "SHA256:mmm3UvhqOqsC5Jl4rwQXxPrssbsCC1SyROqT8rYL8zY"
)

func TestParseKeyscanPin(t *testing.T) {
	t.Run("parses a real ssh-keyscan ed25519 line", func(t *testing.T) {
		out := "example.test " + testEd25519KeyLine + "\n"
		pin, err := parseKeyscanPin(out, "ed25519")
		if err != nil {
			t.Fatalf("parseKeyscanPin() error = %v", err)
		}
		if pin.Fingerprint != testEd25519Fingerprint {
			t.Fatalf("fingerprint = %q, want %q", pin.Fingerprint, testEd25519Fingerprint)
		}
		if pin.Algo != "ssh-ed25519" {
			t.Fatalf("algo = %q, want ssh-ed25519", pin.Algo)
		}
	})

	t.Run("skips ssh-keyscan's leading comment line", func(t *testing.T) {
		out := "# example.test:22 SSH-2.0-OpenSSH_9.9\nexample.test " + testEd25519KeyLine + "\n"
		pin, err := parseKeyscanPin(out, "ed25519")
		if err != nil {
			t.Fatalf("parseKeyscanPin() error = %v", err)
		}
		if pin.Fingerprint != testEd25519Fingerprint {
			t.Fatalf("fingerprint = %q, want %q", pin.Fingerprint, testEd25519Fingerprint)
		}
	})

	t.Run("empty output", func(t *testing.T) {
		if _, err := parseKeyscanPin("", "ed25519"); err == nil {
			t.Fatal("want error for empty output, got nil")
		}
	})

	t.Run("only comments/unparseable lines", func(t *testing.T) {
		out := "# example.test:22 SSH-2.0-OpenSSH_9.9\ngarbage line with no key\n"
		if _, err := parseKeyscanPin(out, "ed25519"); err == nil {
			t.Fatal("want error when no line parses as a key, got nil")
		}
	})

	t.Run("multi-keytype output: picks only the requested type", func(t *testing.T) {
		out := "example.test " + testEd25519KeyLine + "\n" + "example.test " + testRSAKeyLine + "\n"

		edPin, err := parseKeyscanPin(out, "ed25519")
		if err != nil {
			t.Fatalf("parseKeyscanPin(ed25519) error = %v", err)
		}
		if edPin.Fingerprint != testEd25519Fingerprint {
			t.Fatalf("ed25519 fingerprint = %q, want %q", edPin.Fingerprint, testEd25519Fingerprint)
		}

		rsaPin, err := parseKeyscanPin(out, "rsa")
		if err != nil {
			t.Fatalf("parseKeyscanPin(rsa) error = %v", err)
		}
		if rsaPin.Fingerprint != testRSAFingerprint {
			t.Fatalf("rsa fingerprint = %q, want %q", rsaPin.Fingerprint, testRSAFingerprint)
		}

		if edPin.Fingerprint == rsaPin.Fingerprint {
			t.Fatal("requesting two different key types out of the same output must not yield the same pin")
		}
	})
}

func TestKnownHostsLine(t *testing.T) {
	t.Run("default port renders a bare host", func(t *testing.T) {
		line := knownHostsLine("example.com", 22, "ssh-ed25519", "AAAAKEY")
		want := "example.com ssh-ed25519 AAAAKEY"
		if line != want {
			t.Fatalf("knownHostsLine() = %q, want %q", line, want)
		}
	})

	t.Run("nonstandard port renders [host]:port", func(t *testing.T) {
		line := knownHostsLine("example.com", 2222, "ssh-ed25519", "AAAAKEY")
		want := "[example.com]:2222 ssh-ed25519 AAAAKEY"
		if line != want {
			t.Fatalf("knownHostsLine() = %q, want %q", line, want)
		}
	})
}

func TestPinnedSSHArgs(t *testing.T) {
	args := pinnedSSHArgs("/tmp/auxly-join-known-hosts-xyz", "ssh-ed25519")
	joined := strings.Join(args, " ")

	if !strings.Contains(joined, "UserKnownHostsFile=/tmp/auxly-join-known-hosts-xyz") {
		t.Fatalf("pinnedSSHArgs() = %v, want it to pin UserKnownHostsFile to the temp file", args)
	}
	if !strings.Contains(joined, "StrictHostKeyChecking=yes") {
		t.Fatalf("pinnedSSHArgs() = %v, want StrictHostKeyChecking=yes", args)
	}
	if strings.Contains(joined, "accept-new") {
		t.Fatalf("pinnedSSHArgs() = %v, must never fall back to accept-new", args)
	}
	if !strings.Contains(joined, "HostKeyAlgorithms=ssh-ed25519") {
		t.Fatalf("pinnedSSHArgs() = %v, want HostKeyAlgorithms restricted to the pinned type", args)
	}
}
