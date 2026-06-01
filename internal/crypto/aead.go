// Package crypto provides OmniRepo's single AES-GCM-256 envelope-encryption
// helper. It is used for project-scoped upstream_creds secrets and reuses
// the same master key + helper for S3 secret encryption.
//
// The master key is 32 raw bytes (AES-256). Nonce is 12 bytes from
// crypto/rand, prepended to the GCM output. The whole (nonce || ciphertext ||
// tag) is base64-encoded (std alphabet, padded) for storage as text.
//
// This helper never logs or returns the key material. Callers log only
// key length — see internal/app/app.go bootEnsureAEADKey.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
)

// KeySize is the required master-key length in bytes (AES-256).
const KeySize = 32

// ErrKeySize is returned by New when the supplied key is not KeySize bytes.
var ErrKeySize = errors.New("aead: key must be 32 bytes")

// ErrCiphertextTooShort is returned by Decrypt when the decoded envelope is
// smaller than the nonce size (i.e. malformed / truncated).
var ErrCiphertextTooShort = errors.New("aead: ciphertext too short")

// AEAD is the stateless AES-GCM-256 envelope. The zero value is not usable;
// use New.
type AEAD struct {
	key []byte // 32 bytes
}

// New constructs an AEAD from key. Returns ErrKeySize if len(key) != KeySize.
// The key bytes are retained by reference; callers should not mutate them.
func New(key []byte) (*AEAD, error) {
	if len(key) != KeySize {
		return nil, ErrKeySize
	}
	return &AEAD{key: key}, nil
}

// Encrypt seals plaintext with a fresh random 12-byte nonce and returns
// base64(nonce || ciphertext || tag). Two calls with the same plaintext
// return different envelopes (random nonce).
func (a *AEAD) Encrypt(plaintext []byte) (string, error) {
	block, err := aes.NewCipher(a.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize()) // 12 bytes
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ct := gcm.Seal(nil, nonce, plaintext, nil)
	env := make([]byte, 0, len(nonce)+len(ct))
	env = append(env, nonce...)
	env = append(env, ct...)
	return base64.StdEncoding.EncodeToString(env), nil
}

// Decrypt reverses Encrypt. Any tampering (or wrong key) produces a non-nil
// error from gcm.Open (tag mismatch).
func (a *AEAD) Decrypt(encoded string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(a.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(raw) < gcm.NonceSize() {
		return nil, ErrCiphertextTooShort
	}
	nonce, ct := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	return gcm.Open(nil, nonce, ct, nil)
}

// GenerateKey returns KeySize bytes from crypto/rand, suitable for passing
// to New. Errors propagate the rand.Reader failure (essentially never on
// a healthy system).
func GenerateKey() ([]byte, error) {
	k := make([]byte, KeySize)
	if _, err := io.ReadFull(rand.Reader, k); err != nil {
		return nil, err
	}
	return k, nil
}
