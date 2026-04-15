package crypto_test

import (
	"bytes"
	cryptorand "crypto/rand"
	"encoding/base64"
	"math/rand/v2"
	"strings"
	"testing"

	omrcrypto "github.com/dxc-internal/omnirepo/internal/crypto"
)

func TestNewRejectsWrongKeyLength(t *testing.T) {
	for _, size := range []int{0, 1, 15, 16, 31, 33, 64} {
		_, err := omrcrypto.New(make([]byte, size))
		if err == nil {
			t.Errorf("New(len=%d): want error, got nil", size)
		}
	}
}

func TestNewAcceptsThirtyTwoBytes(t *testing.T) {
	k, err := omrcrypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if len(k) != omrcrypto.KeySize {
		t.Fatalf("GenerateKey len = %d, want %d", len(k), omrcrypto.KeySize)
	}
	if _, err := omrcrypto.New(k); err != nil {
		t.Fatalf("New(32): %v", err)
	}
}

func TestGenerateKeyIsRandom(t *testing.T) {
	a, err := omrcrypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	b, err := omrcrypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if bytes.Equal(a, b) {
		t.Fatalf("two GenerateKey calls returned identical bytes")
	}
}

func TestRoundTripVariableLengths(t *testing.T) {
	k, _ := omrcrypto.GenerateKey()
	a, err := omrcrypto.New(k)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rng := rand.New(rand.NewPCG(1, 2))
	for i := 0; i < 1000; i++ {
		n := rng.IntN(4097) // 0..4096
		pt := make([]byte, n)
		if _, err := cryptorand.Read(pt); err != nil {
			t.Fatalf("rand: %v", err)
		}
		env, err := a.Encrypt(pt)
		if err != nil {
			t.Fatalf("Encrypt: %v", err)
		}
		got, err := a.Decrypt(env)
		if err != nil {
			t.Fatalf("Decrypt iter %d (len=%d): %v", i, n, err)
		}
		if !bytes.Equal(got, pt) {
			t.Fatalf("round-trip mismatch iter %d (len=%d)", i, n)
		}
	}
}

func TestNonceUniqueness(t *testing.T) {
	k, _ := omrcrypto.GenerateKey()
	a, _ := omrcrypto.New(k)
	pt := []byte("same plaintext every time")
	env1, err := a.Encrypt(pt)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	env2, err := a.Encrypt(pt)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if env1 == env2 {
		t.Fatal("identical ciphertext across two Encrypts — nonce not random")
	}
}

func TestTamperingFailsDecrypt(t *testing.T) {
	k, _ := omrcrypto.GenerateKey()
	a, _ := omrcrypto.New(k)
	pt := []byte("sensitive: upstream password fragment")
	env, err := a.Encrypt(pt)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(env)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Mutate the middle byte (inside ciphertext/tag region, not the nonce prefix).
	for i := 0; i < len(raw); i++ {
		mutated := make([]byte, len(raw))
		copy(mutated, raw)
		mutated[i] ^= 0x01
		mutatedEnv := base64.StdEncoding.EncodeToString(mutated)
		if _, err := a.Decrypt(mutatedEnv); err == nil {
			t.Fatalf("Decrypt accepted tampered byte at index %d", i)
		}
	}
}

func TestWrongKeyFailsDecrypt(t *testing.T) {
	k1, _ := omrcrypto.GenerateKey()
	k2, _ := omrcrypto.GenerateKey()
	a1, _ := omrcrypto.New(k1)
	a2, _ := omrcrypto.New(k2)
	env, err := a1.Encrypt([]byte("hello"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, err := a2.Decrypt(env); err == nil {
		t.Fatal("Decrypt with wrong key succeeded")
	}
}

func TestDecryptRejectsTruncatedInput(t *testing.T) {
	k, _ := omrcrypto.GenerateKey()
	a, _ := omrcrypto.New(k)
	// 11 bytes (< 12 byte nonce size).
	short := base64.StdEncoding.EncodeToString(make([]byte, 11))
	if _, err := a.Decrypt(short); err == nil {
		t.Fatal("expected error on truncated ciphertext")
	}
}

func TestDecryptRejectsInvalidBase64(t *testing.T) {
	k, _ := omrcrypto.GenerateKey()
	a, _ := omrcrypto.New(k)
	if _, err := a.Decrypt("not valid base64!!!"); err == nil {
		t.Fatal("expected error on invalid base64")
	}
}

func TestEnvelopeLengthAtLeastNonceAndTag(t *testing.T) {
	k, _ := omrcrypto.GenerateKey()
	a, _ := omrcrypto.New(k)
	env, err := a.Encrypt(nil)
	if err != nil {
		t.Fatalf("Encrypt(nil): %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(env)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	// 12-byte nonce + 16-byte GCM tag = 28 bytes minimum.
	if len(raw) < 28 {
		t.Fatalf("envelope len = %d, want >= 28", len(raw))
	}
	// The encoded form should also be plain base64 (no separator).
	if strings.ContainsAny(env, "\n\r") {
		t.Fatalf("encoded envelope contains line breaks: %q", env)
	}
}
