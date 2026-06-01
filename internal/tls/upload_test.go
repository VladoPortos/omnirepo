package tls

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fingerprint(t *testing.T, pemBytes []byte) string {
	t.Helper()
	c := parseCert(t, pemBytes)
	sum := sha256.Sum256(c.Raw)
	return hex.EncodeToString(sum[:])
}

func TestApplyUploadWritesHistoryAndSwapsHolder(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "certs"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Seed holder with an initial (first-boot style) cert.
	initialCert, initialKey, err := GenerateSelfSigned([]string{"h"}, time.Hour, 2048)
	if err != nil {
		t.Fatalf("gen initial: %v", err)
	}
	h := NewCertHolder()
	if err := h.Swap(initialCert, initialKey); err != nil {
		t.Fatalf("seed swap: %v", err)
	}
	fp1 := fingerprint(t, initialCert)

	// Admin-uploaded pair.
	adminCert, adminKey, err := GenerateSelfSigned([]string{"uploaded.example"}, time.Hour, 2048)
	if err != nil {
		t.Fatalf("gen admin: %v", err)
	}
	if err := ApplyUpload(context.Background(), adminCert, adminKey, root, h); err != nil {
		t.Fatalf("ApplyUpload: %v", err)
	}

	// Live files present and equal to uploaded bytes.
	liveCrt, err := os.ReadFile(filepath.Join(root, "certs", "server.crt"))
	if err != nil {
		t.Fatalf("read live crt: %v", err)
	}
	if string(liveCrt) != string(adminCert) {
		t.Fatalf("live cert does not match uploaded bytes")
	}
	liveKey, err := os.ReadFile(filepath.Join(root, "certs", "server.key"))
	if err != nil {
		t.Fatalf("read live key: %v", err)
	}
	if string(liveKey) != string(adminKey) {
		t.Fatalf("live key does not match uploaded bytes")
	}

	// History dir created with both files.
	entries, err := os.ReadDir(filepath.Join(root, "certs", "uploaded"))
	if err != nil {
		t.Fatalf("read history: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 history dir, got %d", len(entries))
	}
	histDir := filepath.Join(root, "certs", "uploaded", entries[0].Name())
	if _, err := os.Stat(filepath.Join(histDir, "server.crt")); err != nil {
		t.Fatalf("history crt missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(histDir, "server.key")); err != nil {
		t.Fatalf("history key missing: %v", err)
	}

	// Holder now serves the uploaded cert — fingerprint differs.
	c, err := h.Get(nil)
	if err != nil {
		t.Fatalf("Get after upload: %v", err)
	}
	sum := sha256.Sum256(c.Leaf.Raw)
	fp2 := hex.EncodeToString(sum[:])
	if fp1 == fp2 {
		t.Fatalf("fingerprint unchanged after upload")
	}
}

// TestApplyUploadLivePairNeverMismatched is the regression gate: after
// a successful upload, the live cert and key on disk must form a matching
// pair (public key of cert matches private key). A pre-existing cert+key
// pair on disk must not be left in a half-updated state if any intermediate
// step fails — we simulate that by pre-creating a read-only certs/ dir that
// blocks the stage writes and verify the live files are untouched.
func TestApplyUploadLivePairNeverMismatched(t *testing.T) {
	root := t.TempDir()
	certsDir := filepath.Join(root, "certs")
	if err := os.MkdirAll(certsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	// Seed a valid initial live pair (the "OLD" pair).
	oldCert, oldKey, err := GenerateSelfSigned([]string{"old"}, time.Hour, 2048)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(certsDir, "server.crt"), oldCert, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(certsDir, "server.key"), oldKey, 0o600); err != nil {
		t.Fatal(err)
	}
	h := NewCertHolder()
	if err := h.Swap(oldCert, oldKey); err != nil {
		t.Fatal(err)
	}

	// Try an upload whose KEY stage write will fail: create a DIRECTORY
	// named server.key.new in certs/ so os.OpenFile (O_WRONLY|O_CREATE|O_TRUNC)
	// errors out. The cert stage succeeds, but the key stage fails — previous
	// implementation would have already renamed the cert on top of the old
	// cert by then.
	blockerDir := filepath.Join(certsDir, "server.key.new")
	if err := os.MkdirAll(blockerDir, 0o750); err != nil {
		t.Fatal(err)
	}
	// Put a file inside so os.Remove (cleanup of stale stage) fails and the
	// subsequent OpenFile(O_WRONLY) on the directory also fails.
	if err := os.WriteFile(filepath.Join(blockerDir, "sentinel"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	newCert, newKey, err := GenerateSelfSigned([]string{"new"}, time.Hour, 2048)
	if err != nil {
		t.Fatal(err)
	}
	err = ApplyUpload(context.Background(), newCert, newKey, root, h)
	if err == nil {
		t.Fatalf("expected upload failure due to blocked key stage")
	}

	// Live pair on disk must still be the OLD matched pair (because the
	// staging step failed BEFORE any rename).
	gotCrt, err := os.ReadFile(filepath.Join(certsDir, "server.crt"))
	if err != nil {
		t.Fatal(err)
	}
	gotKey, err := os.ReadFile(filepath.Join(certsDir, "server.key"))
	if err != nil {
		t.Fatal(err)
	}
	if string(gotCrt) != string(oldCert) {
		t.Fatalf("live cert was modified despite key-stage failure")
	}
	if string(gotKey) != string(oldKey) {
		t.Fatalf("live key was modified despite key-stage failure")
	}
	// Recover test fixture (remove the blocker dir) so t.TempDir cleanup works.
	_ = os.RemoveAll(blockerDir)
}

func TestApplyUploadRejectsMalformedPEM(t *testing.T) {
	root := t.TempDir()
	certPEM, keyPEM, _ := GenerateSelfSigned([]string{"h"}, time.Hour, 2048)
	h := NewCertHolder()
	if err := h.Swap(certPEM, keyPEM); err != nil {
		t.Fatalf("seed swap: %v", err)
	}
	err := ApplyUpload(context.Background(), []byte("not-a-pem"), []byte("also-not"), root, h)
	if err == nil {
		t.Fatalf("expected error on malformed PEM")
	}
	if !strings.Contains(err.Error(), "tls:") {
		t.Fatalf("expected tls: prefix, got %v", err)
	}
	// Live files must NOT have been created.
	if _, statErr := os.Stat(filepath.Join(root, "certs", "server.crt")); statErr == nil {
		t.Fatalf("live crt should not exist after rejected upload")
	}
}
