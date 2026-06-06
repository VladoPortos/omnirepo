package crypto_test

import (
	"testing"

	"github.com/vladoportos/omnirepo/internal/crypto"
)

func TestSHA256Hex(t *testing.T) {
	// Known vector: sha256("abc").
	const want = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got := crypto.SHA256Hex([]byte("abc")); got != want {
		t.Fatalf("SHA256Hex(abc) = %s, want %s", got, want)
	}
	// Empty input is the empty-string digest, not a panic.
	const wantEmpty = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if got := crypto.SHA256Hex(nil); got != wantEmpty {
		t.Fatalf("SHA256Hex(nil) = %s, want %s", got, wantEmpty)
	}
}
