package app_test

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/app"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
)

func TestBootEnsureDockerJWTSecret_GeneratesOnFirstCall(t *testing.T) {
	ctx := context.Background()
	db := sqlitetest.New(t)
	settings := metadata.NewSettingsRepo(db)

	secret, err := app.BootEnsureDockerJWTSecret(ctx, settings)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if len(secret) != 32 {
		t.Fatalf("secret length: %d; want 32", len(secret))
	}

	// Verify it's persisted as base64.
	stored, err := settings.Get(ctx, "docker_token_hmac_secret")
	if err != nil {
		t.Fatalf("get setting: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(stored)
	if err != nil {
		t.Fatalf("stored value not base64: %v", err)
	}
	if len(raw) != 32 {
		t.Fatalf("stored length: %d", len(raw))
	}
	for i := range secret {
		if secret[i] != raw[i] {
			t.Fatalf("secret/stored mismatch at %d", i)
		}
	}
}

func TestBootEnsureDockerJWTSecret_IdempotentOnSecondCall(t *testing.T) {
	ctx := context.Background()
	db := sqlitetest.New(t)
	settings := metadata.NewSettingsRepo(db)

	first, err := app.BootEnsureDockerJWTSecret(ctx, settings)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := app.BootEnsureDockerJWTSecret(ctx, settings)
	if err != nil {
		t.Fatalf("second: %v", err)
	}

	if len(first) != len(second) {
		t.Fatalf("len mismatch: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("secret rotated unexpectedly at %d", i)
		}
	}
}

func TestBootEnsureDockerJWTSecret_RejectsCorruptStoredValue(t *testing.T) {
	ctx := context.Background()
	db := sqlitetest.New(t)
	settings := metadata.NewSettingsRepo(db)

	// Plant a bad (non-32-byte) value.
	if err := settings.Set(ctx, "docker_token_hmac_secret",
		base64.StdEncoding.EncodeToString([]byte("too-short"))); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := app.BootEnsureDockerJWTSecret(ctx, settings); err == nil {
		t.Fatal("expected error on corrupt stored secret; got nil")
	}
}
