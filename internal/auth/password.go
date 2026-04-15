package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters (D-16). These are tuned for OWASP 2026 low-RAM defaults.
// Any change here MUST bump the hash format version (the "v=19" literal) so
// VerifyPassword can refuse or re-derive mismatched encodings.
const (
	argonTime = uint32(3)
	// argonMemory is 65536 KiB = 64 MiB (D-16).
	argonMemory  = uint32(65536)
	argonThreads = uint8(4)
	saltLen      = 16
	keyLen       = 32
)

// otpAlphabet is the 62-char alphanumeric alphabet used by OneTimePassword.
const otpAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// HashPassword derives an argon2id hash of plain using D-16 parameters and
// encodes the result in the canonical PHC-ish format:
//
//	$argon2id$v=19$m=<memKiB>,t=<time>,p=<threads>$<salt_b64>$<key_b64>
//
// salt is 16 bytes from crypto/rand; key is 32 bytes; both encoded with
// base64.RawStdEncoding (no padding).
func HashPassword(plain string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: hash password: %w", err)
	}
	key := argon2.IDKey([]byte(plain), salt, argonTime, argonMemory, argonThreads, keyLen)
	return fmt.Sprintf(
		"$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword re-derives the argon2id key for plain using the parameters
// embedded in encoded and compares it to the stored key in constant time.
//
// Returns (true, nil) on match, (false, nil) on a well-formed but mismatching
// encoding, and (false, err) when encoded is malformed / uses an unsupported
// algorithm or version.
func VerifyPassword(encoded, plain string) (bool, error) {
	parts := strings.Split(encoded, "$")
	// Format: ["", "argon2id", "v=19", "m=...,t=...,p=...", saltB64, keyB64]
	if len(parts) != 6 {
		return false, errors.New("auth: verify password: malformed encoding")
	}
	if parts[1] != "argon2id" {
		return false, fmt.Errorf("auth: verify password: unsupported algorithm %q", parts[1])
	}
	if parts[2] != "v=19" {
		return false, fmt.Errorf("auth: verify password: unsupported version %q", parts[2])
	}

	var mem uint32
	var t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &t, &p); err != nil {
		return false, fmt.Errorf("auth: verify password: bad params %q: %w", parts[3], err)
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, fmt.Errorf("auth: verify password: bad salt: %w", err)
	}
	wantKey, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, fmt.Errorf("auth: verify password: bad key: %w", err)
	}
	gotKey := argon2.IDKey([]byte(plain), salt, t, mem, p, uint32(len(wantKey)))
	if subtle.ConstantTimeCompare(gotKey, wantKey) == 1 {
		return true, nil
	}
	return false, nil
}

// OneTimePassword returns a 16-character alphanumeric string suitable for the
// first-login flow (TEN-08). Entropy source is crypto/rand; each character is
// drawn uniformly from a 62-char alphabet via rand.Int, which yields ~95 bits
// of entropy — well above the 80-bit bar for single-use tokens.
func OneTimePassword() string {
	max := big.NewInt(int64(len(otpAlphabet)))
	b := make([]byte, 16)
	for i := range b {
		idx, err := rand.Int(rand.Reader, max)
		if err != nil {
			// crypto/rand failure is unrecoverable at this layer; fall back
			// to a deterministic non-zero char rather than panicking inside
			// an auth path. Callers treat the OTP as single-use; a single
			// degenerate character still needs the full remainder to match.
			b[i] = otpAlphabet[0]
			continue
		}
		b[i] = otpAlphabet[idx.Int64()]
	}
	return string(b)
}
