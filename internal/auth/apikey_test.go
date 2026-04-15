package auth_test

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/auth"
)

func TestGenerateAPIKey_UserShape(t *testing.T) {
	k, err := auth.GenerateAPIKey(auth.APIKeyKindUser)
	if err != nil {
		t.Fatalf("GenerateAPIKey: %v", err)
	}
	if !auth.APIKeyRegex.MatchString(k.Plaintext) {
		t.Fatalf("plaintext %q does not match APIKeyRegex", k.Plaintext)
	}
	if !strings.HasPrefix(k.Plaintext, "omr_u_") {
		t.Fatalf("prefix: got %q, want omr_u_...", k.Plaintext)
	}
	if len(k.Prefix) != 8 {
		t.Fatalf("Prefix len: got %d, want 8", len(k.Prefix))
	}
	suffix := strings.TrimPrefix(k.Plaintext, "omr_u_")
	if k.Prefix != suffix[:8] {
		t.Fatalf("Prefix %q != first 8 of suffix %q", k.Prefix, suffix[:8])
	}
	if m, err := regexp.MatchString(`^[0-9a-f]{64}$`, k.SHA256); err != nil || !m {
		t.Fatalf("SHA256 shape: %q", k.SHA256)
	}
	// sha256 is of the plaintext
	sum := sha256.Sum256([]byte(k.Plaintext))
	if k.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("SHA256 mismatch")
	}
	if k.Kind != auth.APIKeyKindUser {
		t.Fatalf("Kind: got %q, want u", k.Kind)
	}
}

func TestGenerateAPIKey_ProjectShape(t *testing.T) {
	k, err := auth.GenerateAPIKey(auth.APIKeyKindProject)
	if err != nil {
		t.Fatalf("GenerateAPIKey: %v", err)
	}
	if !strings.HasPrefix(k.Plaintext, "omr_p_") {
		t.Fatalf("prefix: got %q, want omr_p_...", k.Plaintext)
	}
	if k.Kind != auth.APIKeyKindProject {
		t.Fatalf("Kind: got %q, want p", k.Kind)
	}
}

func TestGenerateAPIKey_InvalidKind(t *testing.T) {
	if _, err := auth.GenerateAPIKey(auth.APIKeyKind("x")); err == nil {
		t.Fatalf("expected error for invalid kind")
	}
}

func TestParseAPIKey_Valid(t *testing.T) {
	token := "omr_u_abcdefghijklmnopqrstuvwxyz01"
	kind, prefix, sha, err := auth.ParseAPIKey(token)
	if err != nil {
		t.Fatalf("ParseAPIKey: %v", err)
	}
	if kind != auth.APIKeyKindUser {
		t.Fatalf("kind: %q", kind)
	}
	if prefix != "abcdefgh" {
		t.Fatalf("prefix: %q", prefix)
	}
	sum := sha256.Sum256([]byte(token))
	if sha != hex.EncodeToString(sum[:]) {
		t.Fatalf("sha mismatch")
	}
}

func TestParseAPIKey_Invalid(t *testing.T) {
	cases := []string{
		"",
		"omr_x_abcdefghijklmnopqrstuvwxyz01", // bad kind
		"omr_u_tooshort",                     // wrong length
		"omr_u_abcdefghijklmnopqrstuvwxyz0!", // invalid char
		"omr_u_abcdefghijklmnopqrstuvwxyz0",  // 27 chars
		"prefix_omr_u_abcdefghijklmnopqrstuvwxyz01", // leading junk
	}
	for _, c := range cases {
		_, _, _, err := auth.ParseAPIKey(c)
		if err == nil {
			t.Errorf("ParseAPIKey(%q): err=nil, want non-nil", c)
		}
	}
}

func TestEqualSHA256_ConstantTime(t *testing.T) {
	a := strings.Repeat("a", 64)
	b := strings.Repeat("a", 64)
	c := strings.Repeat("b", 64)
	if !auth.EqualSHA256(a, b) {
		t.Fatalf("equal sha256 returned false")
	}
	if auth.EqualSHA256(a, c) {
		t.Fatalf("unequal sha256 returned true")
	}
	if auth.EqualSHA256("short", a) {
		t.Fatalf("short string accepted")
	}
	// Repeated verification should not panic (constant-time path).
	for i := 0; i < 1000; i++ {
		_ = auth.EqualSHA256(a, b)
	}
}
