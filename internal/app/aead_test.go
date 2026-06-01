package app_test

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/vladoportos/omnirepo/internal/app"
	omrcrypto "github.com/vladoportos/omnirepo/internal/crypto"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
)

func TestBootEnsureAEADKeyGeneratesOnFirstCall(t *testing.T) {
	db := sqlitetest.New(t)
	settings := metadata.NewSettingsRepo(db)
	ctx := context.Background()

	// Verify setting is absent beforehand.
	if v, err := settings.Get(ctx, "upstream_creds_aead_key"); err == nil && v != "" {
		t.Fatalf("pre-boot setting should be empty, got %q", v)
	}

	a, err := app.BootEnsureAEADKey(ctx, db, settings)
	if err != nil {
		t.Fatalf("first boot: %v", err)
	}
	if a == nil {
		t.Fatal("first boot returned nil AEAD")
	}

	// Key should now be stored as base64-encoded 32 bytes.
	stored, err := settings.Get(ctx, "upstream_creds_aead_key")
	if err != nil {
		t.Fatalf("get setting: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(stored)
	if err != nil {
		t.Fatalf("decode stored key: %v", err)
	}
	if len(raw) != omrcrypto.KeySize {
		t.Fatalf("stored key size = %d, want %d", len(raw), omrcrypto.KeySize)
	}

	// AEAD should round-trip.
	env, err := a.Encrypt([]byte("test-payload"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	pt, err := a.Decrypt(env)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if string(pt) != "test-payload" {
		t.Fatalf("round-trip got %q", pt)
	}
}

func TestBootEnsureAEADKeyIsIdempotent(t *testing.T) {
	db := sqlitetest.New(t)
	settings := metadata.NewSettingsRepo(db)
	ctx := context.Background()

	a1, err := app.BootEnsureAEADKey(ctx, db, settings)
	if err != nil {
		t.Fatalf("first boot: %v", err)
	}
	storedAfterFirst, err := settings.Get(ctx, "upstream_creds_aead_key")
	if err != nil {
		t.Fatalf("get after first: %v", err)
	}

	a2, err := app.BootEnsureAEADKey(ctx, db, settings)
	if err != nil {
		t.Fatalf("second boot: %v", err)
	}
	storedAfterSecond, err := settings.Get(ctx, "upstream_creds_aead_key")
	if err != nil {
		t.Fatalf("get after second: %v", err)
	}
	if storedAfterFirst != storedAfterSecond {
		t.Fatal("boot key changed between calls — should be idempotent")
	}

	// Both AEADs must be functional AND interoperable (same key).
	env, err := a1.Encrypt([]byte("cross-instance"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	pt, err := a2.Decrypt(env)
	if err != nil {
		t.Fatalf("Decrypt from second AEAD: %v", err)
	}
	if string(pt) != "cross-instance" {
		t.Fatalf("got %q", pt)
	}
}

func TestBootEnsureAEADKeyRejectsCorruptExistingKey(t *testing.T) {
	db := sqlitetest.New(t)
	settings := metadata.NewSettingsRepo(db)
	ctx := context.Background()

	// Seed a wrong-size key.
	if err := settings.Set(ctx, "upstream_creds_aead_key",
		base64.StdEncoding.EncodeToString([]byte("too-short"))); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, err := app.BootEnsureAEADKey(ctx, db, settings)
	if err == nil {
		t.Fatal("want error on wrong-size stored key, got nil")
	}
}
