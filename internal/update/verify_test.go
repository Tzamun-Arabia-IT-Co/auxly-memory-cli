package update

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPinnedPublicKeyIsValid(t *testing.T) {
	// The compiled-in key must parse, or every verification would error.
	if err := verifyWithPubKey(AuxlyPublicKey, []byte("x"), []byte("not a sig")); err == nil {
		t.Fatal("expected a signature-decode error, got nil")
	} else if strings.Contains(err.Error(), "invalid pinned public key") {
		t.Fatalf("pinned public key failed to parse: %v", err)
	}
}

func TestManifestHasHash(t *testing.T) {
	manifest := []byte("abc123  auxly-linux-amd64\ndef456  auxly-darwin-arm64\n")
	if !manifestHasHash(manifest, "DEF456") {
		t.Error("should match case-insensitively")
	}
	if manifestHasHash(manifest, "ffffff") {
		t.Error("must not match an absent hash")
	}
}

// Real end-to-end signature check with a throwaway minisign keypair.
func TestVerifyWithPubKey_RealSignature(t *testing.T) {
	mini, err := exec.LookPath("minisign")
	if err != nil {
		t.Skip("minisign CLI not installed")
	}
	dir := t.TempDir()
	pub := filepath.Join(dir, "k.pub")
	sec := filepath.Join(dir, "k.key")
	if out, err := exec.Command(mini, "-G", "-W", "-p", pub, "-s", sec).CombinedOutput(); err != nil {
		t.Skipf("keygen failed: %v (%s)", err, out)
	}
	manifestPath := filepath.Join(dir, "checksums.txt")
	manifest := []byte("deadbeef  auxly-linux-amd64\n")
	if err := os.WriteFile(manifestPath, manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(mini, "-S", "-s", sec, "-m", manifestPath).CombinedOutput(); err != nil {
		t.Skipf("sign failed: %v (%s)", err, out)
	}
	sig, err := os.ReadFile(manifestPath + ".minisig")
	if err != nil {
		t.Fatal(err)
	}
	// The public key string is the 2nd line of the .pub file.
	pubData, _ := os.ReadFile(pub)
	lines := strings.Split(strings.TrimSpace(string(pubData)), "\n")
	pubKey := strings.TrimSpace(lines[len(lines)-1])

	// Positive: valid signature verifies.
	if err := verifyWithPubKey(pubKey, manifest, sig); err != nil {
		t.Fatalf("valid signature should verify: %v", err)
	}
	// Negative: tampered manifest fails.
	if err := verifyWithPubKey(pubKey, append(manifest, '!'), sig); err == nil {
		t.Fatal("tampered manifest must fail verification")
	}
}

// Staged rollout: a release with no published manifest/signature proceeds
// unverified by default, but fails when AUXLY_REQUIRE_SIGNATURE=1.
func TestVerifyDownloadedBinary_StagedFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/version") {
			w.Write([]byte("9.9.9"))
			return
		}
		http.NotFound(w, r) // no checksums/signature published
	}))
	defer srv.Close()
	t.Setenv("AUXLY_INSTALL_BASE", srv.URL) // http://127.0.0.1:port is allowed (dev)

	if err := verifyDownloadedBinary([]byte("anything")); err != nil {
		t.Fatalf("staged fallback should proceed when unsigned: %v", err)
	}

	t.Setenv("AUXLY_REQUIRE_SIGNATURE", "1")
	if err := verifyDownloadedBinary([]byte("anything")); err == nil {
		t.Fatal("AUXLY_REQUIRE_SIGNATURE=1 must fail when no signature is published")
	}
}

// A CDN that lacks the manifest may answer 200 with an HTML/SPA page instead of a
// real 404. That junk must be treated as "absent" → staged skip, NOT fail-closed.
func TestVerifyDownloadedBinary_Non404SpaPageIsStaged(t *testing.T) {
	const spa = "<!doctype html><html><head><title>auxly</title></head><body>app</body></html>"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/version") {
			w.Write([]byte("9.9.9"))
			return
		}
		w.WriteHeader(http.StatusOK) // 200 with a bogus body for manifest + .minisig
		w.Write([]byte(spa))
	}))
	defer srv.Close()
	t.Setenv("AUXLY_INSTALL_BASE", srv.URL)

	if err := verifyDownloadedBinary([]byte("anything")); err != nil {
		t.Fatalf("a non-manifest 200 must be treated as absent (staged skip), got: %v", err)
	}

	t.Setenv("AUXLY_REQUIRE_SIGNATURE", "1")
	if err := verifyDownloadedBinary([]byte("anything")); err == nil {
		t.Fatal("AUXLY_REQUIRE_SIGNATURE=1 must reject a bogus non-manifest 200")
	}
}

func TestLooksLikeChecksumManifest(t *testing.T) {
	good := []byte("8363672aa042f14a9463dbead5283e72ac9dcd0b31dda2a2b74a0fd2955a6664  auxly-linux-amd64\n")
	if !looksLikeChecksumManifest(good) {
		t.Error("real manifest line should be recognized")
	}
	if looksLikeChecksumManifest([]byte("<!doctype html><html></html>")) {
		t.Error("HTML must not be recognized as a manifest")
	}
	if !looksLikeMinisig([]byte("untrusted comment: signature from minisign secret key\nRUQf...")) {
		t.Error("minisig header should be recognized")
	}
	if looksLikeMinisig([]byte("<!doctype html>")) {
		t.Error("HTML must not be recognized as a minisig")
	}
}
