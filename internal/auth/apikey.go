package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"regexp"
)

// APIKeyKind distinguishes user-owned (u) from project-owned (p) keys. The
// single-letter form lives inside the plaintext token between the omr_ prefix
// and the 28-char suffix.
type APIKeyKind string

const (
	APIKeyKindUser    APIKeyKind = "u"
	APIKeyKindProject APIKeyKind = "p"
)

// APIKeyRegex matches OmniRepo API-key plaintext tokens (D-17):
//
//	omr_<u|p>_<28 base62>
//
// The 28-char suffix is drawn uniformly from [0-9A-Za-z] giving ~167 bits of
// entropy (≫ 128).
var APIKeyRegex = regexp.MustCompile(`^omr_([up])_([0-9A-Za-z]{28})$`)

// apiKeyAlphabet is the 62-char base62 alphabet used for the suffix body.
const apiKeyAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// apiKeyPrefixLen is the number of leading suffix characters stored in the
// api_keys.token_prefix column for index lookups.
const apiKeyPrefixLen = 8

// apiKeySuffixLen is the base62 suffix length.
const apiKeySuffixLen = 28

// APIKey is the return type from GenerateAPIKey. The plaintext is revealed
// to the caller exactly once; only (Prefix, SHA256, Kind) persist to
// api_keys.
type APIKey struct {
	Plaintext string     // omr_<u|p>_<28 base62> — shown to user at creation time only
	Prefix    string     // first 8 chars of the 28-char suffix — indexed for lookup
	SHA256    string     // hex sha256(Plaintext) — stored, constant-time compared on verify
	Kind      APIKeyKind // "u" or "p"
}

// GenerateAPIKey builds a fresh API key of the given kind. The suffix is 28
// characters drawn uniformly from base62; the returned APIKey contains the
// plaintext (to show to the user) alongside the prefix/sha256 pair that the
// server persists.
func GenerateAPIKey(kind APIKeyKind) (APIKey, error) {
	switch kind {
	case APIKeyKindUser, APIKeyKindProject:
	default:
		return APIKey{}, fmt.Errorf("auth: generate api key: invalid kind %q", kind)
	}
	max := big.NewInt(int64(len(apiKeyAlphabet)))
	suf := make([]byte, apiKeySuffixLen)
	for i := range suf {
		idx, err := rand.Int(rand.Reader, max)
		if err != nil {
			return APIKey{}, fmt.Errorf("auth: generate api key: %w", err)
		}
		suf[i] = apiKeyAlphabet[idx.Int64()]
	}
	plaintext := "omr_" + string(kind) + "_" + string(suf)
	sum := sha256.Sum256([]byte(plaintext))
	return APIKey{
		Plaintext: plaintext,
		Prefix:    string(suf[:apiKeyPrefixLen]),
		SHA256:    hex.EncodeToString(sum[:]),
		Kind:      kind,
	}, nil
}

// ParseAPIKey validates token against APIKeyRegex and returns (kind, prefix,
// sha256hex, nil) on success. Malformed input returns a non-nil error with
// zero-valued strings.
//
// The sha256 hex computed here is the canonical value middlewares compare
// against api_keys.token_sha256 using subtle.ConstantTimeCompare — see
// EqualSHA256.
func ParseAPIKey(token string) (APIKeyKind, string, string, error) {
	m := APIKeyRegex.FindStringSubmatch(token)
	if m == nil {
		return "", "", "", errors.New("auth: parse api key: invalid format")
	}
	kind := APIKeyKind(m[1])
	suffix := m[2]
	sum := sha256.Sum256([]byte(token))
	return kind, suffix[:apiKeyPrefixLen], hex.EncodeToString(sum[:]), nil
}

// EqualSHA256 constant-time compares two hex-encoded sha256 strings. Returns
// false if either is not the canonical 64 hex characters.
func EqualSHA256(a, b string) bool {
	if len(a) != 64 || len(b) != 64 {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
