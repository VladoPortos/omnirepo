package metadata_test

import (
	"context"
	"errors"
	"testing"

	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
)

func TestSettingsRepo_GetSetRoundTrip(t *testing.T) {
	db := sqlitetest.New(t)
	ctx := context.Background()
	s := metadata.NewSettingsRepo(db)

	if _, err := s.Get(ctx, "missing"); !errors.Is(err, metadata.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if err := s.Set(ctx, "seeded_from_bootstrap", "2026-04-15T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	v, err := s.Get(ctx, "seeded_from_bootstrap")
	if err != nil {
		t.Fatal(err)
	}
	if v != "2026-04-15T10:00:00Z" {
		t.Fatalf("got %q", v)
	}
	// Idempotent update.
	if err := s.Set(ctx, "seeded_from_bootstrap", "2026-04-15T11:00:00Z"); err != nil {
		t.Fatal(err)
	}
	v, _ = s.Get(ctx, "seeded_from_bootstrap")
	if v != "2026-04-15T11:00:00Z" {
		t.Fatalf("got %q", v)
	}
}

func TestSettingsRepo_GetAll(t *testing.T) {
	db := sqlitetest.New(t)
	ctx := context.Background()
	s := metadata.NewSettingsRepo(db)
	for k, v := range map[string]string{"a": "1", "b": "2"} {
		if err := s.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.GetAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if all["a"] != "1" || all["b"] != "2" {
		t.Fatalf("unexpected: %+v", all)
	}
}
