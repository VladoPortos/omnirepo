package auth_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/auth"
)

func TestHashPassword_FormatAndSaltUnique(t *testing.T) {
	h1, err := auth.HashPassword("swordfish")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !strings.HasPrefix(h1, "$argon2id$v=19$m=65536,t=3,p=4$") {
		t.Fatalf("HashPassword prefix wrong: %s", h1)
	}
	h2, err := auth.HashPassword("swordfish")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if h1 == h2 {
		t.Fatalf("two hashes identical; salt must differ")
	}
}

func TestVerifyPassword_Success(t *testing.T) {
	h, err := auth.HashPassword("swordfish")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	ok, err := auth.VerifyPassword(h, "swordfish")
	if err != nil || !ok {
		t.Fatalf("VerifyPassword: ok=%v err=%v", ok, err)
	}
}

func TestVerifyPassword_WrongPassword(t *testing.T) {
	h, err := auth.HashPassword("swordfish")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	ok, err := auth.VerifyPassword(h, "wrong")
	if err != nil {
		t.Fatalf("VerifyPassword err: %v", err)
	}
	if ok {
		t.Fatalf("VerifyPassword: ok=true, want false")
	}
}

func TestVerifyPassword_MalformedHash(t *testing.T) {
	cases := []string{
		"",
		"not-a-hash",
		"$bcrypt$...",
		"$argon2id$v=19$bad$aa$bb",
		"$argon2id$v=18$m=65536,t=3,p=4$aa$bb", // wrong version
		"$argon2id$v=19$m=65536,t=3,p=4$notbase64!$bb",
	}
	for _, c := range cases {
		ok, err := auth.VerifyPassword(c, "x")
		if err == nil {
			t.Errorf("VerifyPassword(%q): err=nil, want non-nil", c)
		}
		if ok {
			t.Errorf("VerifyPassword(%q): ok=true, want false", c)
		}
	}
}

func TestOneTimePassword_ShapeAndEntropy(t *testing.T) {
	alnum := regexp.MustCompile(`^[A-Za-z0-9]{16}$`)
	seen := make(map[string]struct{}, 1000)
	for i := 0; i < 1000; i++ {
		p := auth.OneTimePassword()
		if !alnum.MatchString(p) {
			t.Fatalf("OneTimePassword shape: %q", p)
		}
		seen[p] = struct{}{}
	}
	// Allow up to 10 collisions over 1000 calls (statistical tolerance).
	if len(seen) < 990 {
		t.Fatalf("OneTimePassword entropy: %d/%d distinct; want >= 990", len(seen), 1000)
	}
}
