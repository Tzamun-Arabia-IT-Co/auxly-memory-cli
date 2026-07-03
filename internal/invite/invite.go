package invite

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const tokenPrefix = "auxly1-"

var tokenEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// Token is the copy-pasteable invite material shared with a joining machine.
type Token struct {
	Host        string    `json:"host"`
	Port        int       `json:"port"`
	Fingerprint string    `json:"fingerprint"`
	Secret      string    `json:"secret"`
	Expires     time.Time `json:"expires"`
}

// Mint creates a new invite token with random secret material and a deadline.
func Mint(host string, port int, fingerprint string, ttl time.Duration) (Token, error) {
	// WHY: an empty fingerprint pins to nothing, so Consume silently accepts
	// any joiner with no signal that pinning was skipped. The SSH wiring
	// must always supply a real fingerprint — an explicit opt-out isn't a
	// v1 feature — so fail closed here instead of minting an invite nobody
	// can tell is unpinned.
	if fingerprint == "" {
		return Token{}, errors.New("mint invite: fingerprint is required")
	}

	var secret [20]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return Token{}, fmt.Errorf("mint invite secret: %w", err)
	}

	return Token{
		Host:        host,
		Port:        port,
		Fingerprint: fingerprint,
		Secret:      tokenEncoding.EncodeToString(secret[:]),
		Expires:     time.Now().Add(ttl),
	}, nil
}

// Encode returns a single opaque invite string.
func (t Token) Encode() string {
	raw, err := json.Marshal(t)
	if err != nil {
		return tokenPrefix
	}

	// WHY: WhatsApp/Slack mangle colons+slashes; one [a-z2-7-] run survives chat apps.
	return tokenPrefix + tokenEncoding.EncodeToString(raw)
}

// Decode parses an opaque invite string.
func Decode(s string) (Token, error) {
	// WHY: mobile keyboards autocapitalize the first letter ("Auxly1-...");
	// the token alphabet is lowercase-only, so folding to lowercase loses
	// no information and accepts what users actually paste.
	s = strings.ToLower(strings.TrimSpace(s))
	if !strings.HasPrefix(s, tokenPrefix) {
		return Token{}, errors.New("unknown invite format")
	}

	body := strings.TrimPrefix(s, tokenPrefix)
	if body == "" {
		return Token{}, errors.New("decode invite: empty payload")
	}
	for _, r := range body {
		if (r < 'a' || r > 'z') && (r < '2' || r > '7') {
			return Token{}, fmt.Errorf("decode invite: invalid token character %q", r)
		}
	}

	decoded, err := tokenEncoding.DecodeString(body)
	if err != nil {
		return Token{}, fmt.Errorf("decode invite payload: %w", err)
	}

	var token Token
	if err := json.Unmarshal(decoded, &token); err != nil {
		return Token{}, fmt.Errorf("decode invite json: %w", err)
	}

	return token, nil
}

// ID returns the host-side lookup key for this token.
func (t Token) ID() string {
	return idForSecret(t.Secret)
}

func idForSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	// WHY: invites.json leak must not allow joining.
	return fmt.Sprintf("%x", sum[:])[:12]
}
