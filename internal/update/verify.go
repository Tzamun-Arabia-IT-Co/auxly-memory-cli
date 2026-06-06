package update

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	minisign "github.com/jedisct1/go-minisign"
)

// AuxlyPublicKey is the pinned minisign public key used to verify release
// signatures (H2/H3). It is the public half of the keypair whose private key
// lives only in CI (GH secret MINISIGN_KEY). NOT a secret — safe to embed.
const AuxlyPublicKey = "RWQfIGHWpXR4MtPvcbWwN1J7mx9FGsCaHMmdIpGMZAKDvmILC2Of5Q/K"

// verifyManifestSignature returns nil only when `manifest` carries a valid
// minisign signature `sig` from the pinned public key.
func verifyManifestSignature(manifest, sig []byte) error {
	return verifyWithPubKey(AuxlyPublicKey, manifest, sig)
}

// verifyWithPubKey verifies a minisign signature against an explicit public key.
// Split out from verifyManifestSignature so tests can inject a throwaway key.
func verifyWithPubKey(pubKeyStr string, manifest, sig []byte) error {
	pk, err := minisign.NewPublicKey(pubKeyStr)
	if err != nil {
		return fmt.Errorf("invalid pinned public key: %w", err)
	}
	s, err := minisign.DecodeSignature(string(sig))
	if err != nil {
		return fmt.Errorf("malformed signature: %w", err)
	}
	ok, err := pk.Verify(manifest, s)
	if err != nil {
		return fmt.Errorf("signature verification error: %w", err)
	}
	if !ok {
		return fmt.Errorf("signature does not match the pinned public key")
	}
	return nil
}

// manifestHasHash reports whether the (already signature-verified) checksum
// manifest lists the given hex sha256. goreleaser checksums.txt lines are
// "<sha256>  <filename>".
func manifestHasHash(manifest []byte, sumHex string) bool {
	want := strings.ToLower(strings.TrimSpace(sumHex))
	for _, line := range strings.Split(string(manifest), "\n") {
		if fields := strings.Fields(line); len(fields) >= 1 && strings.ToLower(fields[0]) == want {
			return true
		}
	}
	return false
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// looksLikeChecksumManifest reports whether b has at least one line shaped like a
// goreleaser checksum entry ("<64-hex-sha256>  <filename>"). Used to distinguish a
// real manifest from an HTML/SPA page a CDN may serve with HTTP 200 for a missing
// asset.
func looksLikeChecksumManifest(b []byte) bool {
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && len(f[0]) == 64 && isHex(f[0]) {
			return true
		}
	}
	return false
}

// looksLikeMinisig reports whether b begins like a minisign signature file.
func looksLikeMinisig(b []byte) bool {
	return strings.HasPrefix(strings.TrimSpace(string(b)), "untrusted comment:")
}

func isHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return len(s) > 0
}

// fetchIfPresent GETs url, returning (body, true, nil) on 200, (nil, false, nil)
// on 404 (so the caller can apply the staged fallback), or an error otherwise.
func fetchIfPresent(url string) ([]byte, bool, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20)) // manifests are tiny
	if err != nil {
		return nil, false, err
	}
	return body, true, nil
}

// verifyDownloadedBinary checks `bin` against a minisign-signed checksum manifest
// published next to the release.
//
// STAGED ROLLOUT: releases published before signing existed have no manifest /
// signature at /dl. In that case we proceed UNVERIFIED (preserving the current
// install behavior) so the existing install base is never broken — unless
// AUXLY_REQUIRE_SIGNATURE=1 is set, which makes a missing signature fatal. Once a
// signed manifest is present, verification is REQUIRED and any mismatch aborts.
func verifyDownloadedBinary(bin []byte) error {
	version, _ := Latest()
	if version == "" {
		// Can't determine the version → can't locate the versioned manifest.
		if os.Getenv("AUXLY_REQUIRE_SIGNATURE") == "1" {
			return fmt.Errorf("cannot determine release version to verify signature")
		}
		return nil
	}

	base := BaseURL()
	manifestURL := fmt.Sprintf("%s/dl/auxly-%s-checksums.txt", base, version)
	sigURL := manifestURL + ".minisig"

	manifest, mok, merr := fetchIfPresent(manifestURL)
	if merr != nil {
		return fmt.Errorf("fetch checksum manifest: %w", merr)
	}
	sig, sok, serr := fetchIfPresent(sigURL)
	if serr != nil {
		return fmt.Errorf("fetch signature: %w", serr)
	}

	// A server that lacks the manifest may answer 200 with an SPA/HTML page rather
	// than a real 404 (auxly.io's /dl did exactly this before its rule was fixed).
	// Treat a 200 whose body is NOT a checksum manifest / minisig as "absent" so the
	// staged fallback applies instead of fail-closing a legitimate install on junk.
	if mok && !looksLikeChecksumManifest(manifest) {
		mok = false
	}
	if sok && !looksLikeMinisig(sig) {
		sok = false
	}

	if !mok || !sok {
		if os.Getenv("AUXLY_REQUIRE_SIGNATURE") == "1" {
			return fmt.Errorf("release signature required but not published (manifest=%t signature=%t)", mok, sok)
		}
		return nil // staged: pre-signing release (or a non-manifest 200 from the CDN)
	}

	if err := verifyManifestSignature(manifest, sig); err != nil {
		return err
	}
	if !manifestHasHash(manifest, sha256Hex(bin)) {
		return fmt.Errorf("downloaded binary does not match the signed checksum manifest")
	}
	return nil
}
