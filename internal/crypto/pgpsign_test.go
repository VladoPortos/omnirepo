package crypto_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/clearsign"

	omrcrypto "github.com/vladoportos/omnirepo/internal/crypto"
)

// Use 2048-bit keys in unit tests so the -race suite completes in a
// reasonable time. Production default is 4096 per config.Signing.
const testBits = 2048

func TestGenerateRepoKeyShape(t *testing.T) {
	t.Parallel()
	priv, pub, fp, err := omrcrypto.GenerateRepoKey("omnirepo-test", testBits)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !strings.Contains(priv, "BEGIN PGP PRIVATE KEY BLOCK") {
		t.Fatalf("priv missing marker: %q", priv[:60])
	}
	if !strings.Contains(pub, "BEGIN PGP PUBLIC KEY BLOCK") {
		t.Fatalf("pub missing marker: %q", pub[:60])
	}
	if len(fp) != 40 {
		t.Fatalf("fingerprint len=%d want 40", len(fp))
	}
	if fp != strings.ToUpper(fp) {
		t.Fatalf("fingerprint not uppercase: %q", fp)
	}
}

func TestClearSignRoundTrip(t *testing.T) {
	t.Parallel()
	priv, pub, _, err := omrcrypto.GenerateRepoKey("omnirepo-test", testBits)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	payload := []byte("Origin: OmniRepo\nLabel: test\nSuite: stable\n")
	signed, err := omrcrypto.ClearSign(priv, payload)
	if err != nil {
		t.Fatalf("clearsign: %v", err)
	}
	if !bytes.Contains(signed, []byte("BEGIN PGP SIGNED MESSAGE")) {
		t.Fatalf("clearsign output missing marker")
	}
	// Decode the clearsigned message and verify against the public key.
	block, _ := clearsign.Decode(signed)
	if block == nil {
		t.Fatal("clearsign.Decode returned nil")
	}
	// clearsign canonicalizes line endings to CRLF per RFC4880; compare
	// after stripping CR so the equality focuses on byte-content fidelity.
	wantNoCR := bytes.ReplaceAll(payload, []byte("\r"), nil)
	gotNoCR := bytes.ReplaceAll(block.Bytes, []byte("\r"), nil)
	if !bytes.Equal(bytes.TrimRight(gotNoCR, "\n"), bytes.TrimRight(wantNoCR, "\n")) {
		t.Fatalf("plaintext mismatch: want %q got %q", payload, block.Bytes)
	}
	keyring, err := openpgp.ReadArmoredKeyRing(strings.NewReader(pub))
	if err != nil {
		t.Fatalf("read pub: %v", err)
	}
	if _, err := openpgp.CheckDetachedSignature(keyring, bytes.NewReader(block.Bytes), block.ArmoredSignature.Body, nil); err != nil {
		t.Fatalf("verify clearsign: %v", err)
	}
}

func TestDetachSignRoundTrip(t *testing.T) {
	t.Parallel()
	priv, pub, _, err := omrcrypto.GenerateRepoKey("omnirepo-test", testBits)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	body := []byte("<repomd><revision>1</revision></repomd>")
	sig, err := omrcrypto.DetachSign(priv, body)
	if err != nil {
		t.Fatalf("detach: %v", err)
	}
	if !bytes.Contains(sig, []byte("BEGIN PGP SIGNATURE")) {
		t.Fatalf("detach output missing marker")
	}
	keyring, err := openpgp.ReadArmoredKeyRing(strings.NewReader(pub))
	if err != nil {
		t.Fatalf("read pub: %v", err)
	}
	if _, err := openpgp.CheckArmoredDetachedSignature(keyring, bytes.NewReader(body), bytes.NewReader(sig), nil); err != nil {
		t.Fatalf("verify detach: %v", err)
	}
}

func TestDetachSignRejectsBadPriv(t *testing.T) {
	t.Parallel()
	if _, err := omrcrypto.DetachSign("-----BEGIN PGP PRIVATE KEY BLOCK-----\n\n-----END PGP PRIVATE KEY BLOCK-----\n", []byte("x")); err == nil {
		t.Fatal("expected error for malformed armored key")
	}
}

func TestGenerateRepoKeyRejectsSmallBits(t *testing.T) {
	t.Parallel()
	if _, _, _, err := omrcrypto.GenerateRepoKey("x", 1024); err == nil {
		t.Fatal("expected rejection for bits<2048")
	}
	if _, _, _, err := omrcrypto.GenerateRepoKey("", testBits); err == nil {
		t.Fatal("expected rejection for empty uid")
	}
}
