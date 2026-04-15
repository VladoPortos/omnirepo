package metadata_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	omrcrypto "github.com/dxc-internal/omnirepo/internal/crypto"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
)

// newRepo returns a fresh DB + constructed UpstreamCredsRepo with a random
// AEAD key, plus a helper that seeds a project row and returns its id.
func newRepo(t *testing.T) (*metadata.DB, *metadata.UpstreamCredsRepo, func(name string) int64) {
	t.Helper()
	db := sqlitetest.New(t)
	key, err := omrcrypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	aead, err := omrcrypto.New(key)
	if err != nil {
		t.Fatalf("New aead: %v", err)
	}
	r := metadata.NewUpstreamCredsRepo(db, aead)
	seedProject := func(name string) int64 {
		var id int64
		if err := db.WriteTx(context.Background(), func(tx *sql.Tx) error {
			res, err := tx.ExecContext(context.Background(),
				`INSERT INTO projects(name) VALUES (?)`, name)
			if err != nil {
				return err
			}
			id, err = res.LastInsertId()
			return err
		}); err != nil {
			t.Fatalf("seed project %s: %v", name, err)
		}
		return id
	}
	return db, r, seedProject
}

func TestUpstreamCredsCreateLookupRoundTrip(t *testing.T) {
	_, r, seed := newRepo(t)
	proj := seed("proj-a")
	ctx := context.Background()

	id, err := r.Create(ctx, proj, "registry.example.com", metadata.CredKindDocker,
		"alice", "s3cr3t-p@ss", "", 0)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id == 0 {
		t.Fatal("got zero id")
	}

	u, pw, tok, host, err := r.Lookup(ctx, proj, id)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if u != "alice" || pw != "s3cr3t-p@ss" || tok != "" || host != "registry.example.com" {
		t.Fatalf("round-trip mismatch: got u=%q pw=%q tok=%q host=%q", u, pw, tok, host)
	}
}

func TestUpstreamCredsCreateWithTokenOnly(t *testing.T) {
	_, r, seed := newRepo(t)
	proj := seed("proj-a")
	ctx := context.Background()

	id, err := r.Create(ctx, proj, "ghcr.io", metadata.CredKindDocker,
		"bot", "", "ghp_abc123XYZ", 0)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, pw, tok, _, err := r.Lookup(ctx, proj, id)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if pw != "" || tok != "ghp_abc123XYZ" {
		t.Fatalf("token-only round-trip mismatch: pw=%q tok=%q", pw, tok)
	}
}

func TestUpstreamCredsRequiresAtLeastOneSecret(t *testing.T) {
	_, r, seed := newRepo(t)
	proj := seed("proj-a")
	ctx := context.Background()

	_, err := r.Create(ctx, proj, "h", metadata.CredKindDocker, "u", "", "", 0)
	if !errors.Is(err, metadata.ErrSecretRequired) {
		t.Fatalf("Create both empty: %v, want ErrSecretRequired", err)
	}

	id, err := r.Create(ctx, proj, "h", metadata.CredKindDocker, "u", "pw", "", 0)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := r.Update(ctx, proj, id, "u2", "", ""); !errors.Is(err, metadata.ErrSecretRequired) {
		t.Fatalf("Update both empty: %v, want ErrSecretRequired", err)
	}
}

func TestUpstreamCredsCreateRejectsUnknownKind(t *testing.T) {
	_, r, seed := newRepo(t)
	proj := seed("proj-a")
	ctx := context.Background()
	_, err := r.Create(ctx, proj, "h", metadata.CredKind("bogus"), "u", "pw", "", 0)
	if err == nil {
		t.Fatal("want error for unknown kind")
	}
}

func TestUpstreamCredsUniqueConstraint(t *testing.T) {
	_, r, seed := newRepo(t)
	proj := seed("proj-a")
	ctx := context.Background()
	if _, err := r.Create(ctx, proj, "h", metadata.CredKindDocker, "u", "pw", "", 0); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if _, err := r.Create(ctx, proj, "h", metadata.CredKindDocker, "u2", "pw2", "", 0); err == nil {
		t.Fatal("expected UNIQUE(project_id, host, kind) to reject duplicate")
	}
}

func TestUpstreamCredsUpdate(t *testing.T) {
	_, r, seed := newRepo(t)
	proj := seed("proj-a")
	ctx := context.Background()

	id, _ := r.Create(ctx, proj, "h", metadata.CredKindDocker, "u1", "pw1", "", 0)

	if err := r.Update(ctx, proj, id, "u2", "pw2", ""); err != nil {
		t.Fatalf("Update: %v", err)
	}
	u, pw, _, _, err := r.Lookup(ctx, proj, id)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if u != "u2" || pw != "pw2" {
		t.Fatalf("post-update got u=%q pw=%q", u, pw)
	}

	// Updating token only keeps the existing password.
	if err := r.Update(ctx, proj, id, "u3", "", "tok3"); err != nil {
		t.Fatalf("Update tok-only: %v", err)
	}
	u, pw, tok, _, err := r.Lookup(ctx, proj, id)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if u != "u3" || pw != "pw2" || tok != "tok3" {
		t.Fatalf("token-only update got u=%q pw=%q tok=%q", u, pw, tok)
	}
}

func TestUpstreamCredsDelete(t *testing.T) {
	_, r, seed := newRepo(t)
	proj := seed("proj-a")
	ctx := context.Background()

	id, _ := r.Create(ctx, proj, "h", metadata.CredKindDocker, "u", "pw", "", 0)
	if err := r.Delete(ctx, proj, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := r.Get(ctx, proj, id); !errors.Is(err, metadata.ErrNotFound) {
		t.Fatalf("post-delete Get: %v, want ErrNotFound", err)
	}
}

func TestUpstreamCredsForeignProjectIsolation(t *testing.T) {
	_, r, seed := newRepo(t)
	projA := seed("proj-a")
	projB := seed("proj-b")
	ctx := context.Background()

	id, _ := r.Create(ctx, projA, "h", metadata.CredKindDocker, "u", "pw", "", 0)

	if _, err := r.Get(ctx, projB, id); !errors.Is(err, metadata.ErrForeignProject) {
		t.Fatalf("cross-project Get: %v, want ErrForeignProject", err)
	}
	if err := r.Update(ctx, projB, id, "x", "y", ""); !errors.Is(err, metadata.ErrForeignProject) {
		t.Fatalf("cross-project Update: %v, want ErrForeignProject", err)
	}
	if err := r.Delete(ctx, projB, id); !errors.Is(err, metadata.ErrForeignProject) {
		t.Fatalf("cross-project Delete: %v, want ErrForeignProject", err)
	}
	if _, _, _, _, err := r.Lookup(ctx, projB, id); !errors.Is(err, metadata.ErrForeignProject) {
		t.Fatalf("cross-project Lookup: %v, want ErrForeignProject", err)
	}
}

func TestUpstreamCredsCascadeOnProjectDelete(t *testing.T) {
	db, r, seed := newRepo(t)
	proj := seed("proj-a")
	ctx := context.Background()
	id, _ := r.Create(ctx, proj, "h", metadata.CredKindDocker, "u", "pw", "", 0)

	// Hard-delete the project row — ON DELETE CASCADE should remove the cred row.
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM projects WHERE id=?`, proj)
		return err
	}); err != nil {
		t.Fatalf("delete project: %v", err)
	}
	if _, err := r.Get(ctx, proj, id); !errors.Is(err, metadata.ErrNotFound) {
		t.Fatalf("post-cascade Get: %v, want ErrNotFound", err)
	}
}

// TestListNeverExposesSecrets asserts the JSON-serialized projection of
// CredMeta does not carry "password" or "token" keys, so an accidental
// REST handler that just json-encodes a CredMeta cannot leak secrets.
func TestListNeverExposesSecrets(t *testing.T) {
	_, r, seed := newRepo(t)
	proj := seed("proj-a")
	ctx := context.Background()

	_, err := r.Create(ctx, proj, "h1", metadata.CredKindDocker, "u1", "VERY-SECRET-PW", "SECRET-TOKEN", 0)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	metas, err := r.List(ctx, proj)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(metas) != 1 {
		t.Fatalf("list len = %d, want 1", len(metas))
	}
	payload, err := json.Marshal(metas)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// The plaintext secrets must never appear.
	if bytes.Contains(payload, []byte("VERY-SECRET-PW")) || bytes.Contains(payload, []byte("SECRET-TOKEN")) {
		t.Fatalf("list JSON leaked plaintext: %s", payload)
	}
	// The enc columns must not appear in the JSON shape either.
	lower := strings.ToLower(string(payload))
	for _, banned := range []string{"\"password\"", "\"token\"", "password_enc", "token_enc"} {
		if strings.Contains(lower, banned) {
			t.Fatalf("list JSON carries banned key %q: %s", banned, payload)
		}
	}
}

func TestLookupDetectsTamperedCiphertext(t *testing.T) {
	db, r, seed := newRepo(t)
	proj := seed("proj-a")
	ctx := context.Background()
	id, _ := r.Create(ctx, proj, "h", metadata.CredKindDocker, "u", "pw1", "", 0)

	// Manually mutate the password_enc ciphertext so the AEAD tag check fails.
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE upstream_creds SET password_enc='AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=' WHERE id=?`, id)
		return err
	}); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	if _, _, _, _, err := r.Lookup(ctx, proj, id); err == nil {
		t.Fatal("Lookup accepted tampered ciphertext")
	}
}

func TestGetNotFound(t *testing.T) {
	_, r, seed := newRepo(t)
	proj := seed("proj-a")
	if _, err := r.Get(context.Background(), proj, 9999); !errors.Is(err, metadata.ErrNotFound) {
		t.Fatalf("Get missing: %v, want ErrNotFound", err)
	}
}
