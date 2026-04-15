package metadata_test

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"

	omrcrypto "github.com/dxc-internal/omnirepo/internal/crypto"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
)

func newAEAD(t *testing.T) *omrcrypto.AEAD {
	t.Helper()
	key, err := omrcrypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	aead, err := omrcrypto.New(key)
	if err != nil {
		t.Fatalf("new aead: %v", err)
	}
	return aead
}

func seedRPMRepo(t *testing.T, db *metadata.DB) int64 {
	t.Helper()
	ctx := context.Background()
	if _, err := db.Writer.ExecContext(ctx, `INSERT INTO projects(name) VALUES ('proj')`); err != nil {
		t.Fatalf("project: %v", err)
	}
	res, err := db.Writer.ExecContext(ctx,
		`INSERT INTO repos(project_id,type,name) VALUES (1,'rpm','repo1')`,
	)
	if err != nil {
		t.Fatalf("repo: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestSigningKeysRoundTrip(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	repoID := seedRPMRepo(t, db)
	aead := newAEAD(t)
	repo := metadata.NewSigningKeysRepo(db, aead)
	ctx := context.Background()

	const pub = "-----BEGIN PGP PUBLIC KEY BLOCK-----\npub\n-----END-----\n"
	const priv = "-----BEGIN PGP PRIVATE KEY BLOCK-----\npriv\n-----END-----\n"
	const fp = "ABCDEF1234567890ABCDEF1234567890ABCDEF12"

	var id int64
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		v, err := repo.Insert(ctx, tx, repoID, pub, priv, fp)
		id = v
		return err
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if id == 0 {
		t.Fatal("Insert returned zero id")
	}

	meta, err := repo.Lookup(ctx, repoID)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if meta.PublicArmored != pub || meta.Fingerprint != fp {
		t.Fatalf("meta mismatch: %+v", meta)
	}
	if meta.Scope != "repo" || meta.KeyKind != "gpg_rsa4096" {
		t.Fatalf("meta defaults wrong: scope=%q kind=%q", meta.Scope, meta.KeyKind)
	}

	got, err := repo.LookupPrivate(ctx, repoID)
	if err != nil {
		t.Fatalf("lookup private: %v", err)
	}
	if got != priv {
		t.Fatalf("LookupPrivate did not round-trip: want %q got %q", priv, got)
	}

	// Verify the on-disk bytes are encrypted (not equal to plaintext).
	var encBytes []byte
	if err := db.Reader.QueryRow(
		`SELECT private_enc FROM signing_keys WHERE repo_id=?`, repoID,
	).Scan(&encBytes); err != nil {
		t.Fatalf("raw scan: %v", err)
	}
	if strings.Contains(string(encBytes), "PRIVATE KEY BLOCK") {
		t.Fatal("private_enc on disk contains plaintext markers — encryption skipped")
	}
}

func TestSigningKeysUNIQUEPerRepoScope(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	repoID := seedRPMRepo(t, db)
	repo := metadata.NewSigningKeysRepo(db, newAEAD(t))
	ctx := context.Background()

	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := repo.Insert(ctx, tx, repoID, "pub", "priv", "fp1")
		return err
	}); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := repo.Insert(ctx, tx, repoID, "pub2", "priv2", "fp2")
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "UNIQUE") {
		t.Fatalf("duplicate insert should have failed with UNIQUE, got %v", err)
	}
}

func TestSigningKeysLookupMissing(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	repo := metadata.NewSigningKeysRepo(db, newAEAD(t))
	_, err := repo.Lookup(context.Background(), 999)
	if !errors.Is(err, metadata.ErrNotFound) {
		t.Fatalf("Lookup missing: want ErrNotFound, got %v", err)
	}
	_, err = repo.LookupPrivate(context.Background(), 999)
	if !errors.Is(err, metadata.ErrNotFound) {
		t.Fatalf("LookupPrivate missing: want ErrNotFound, got %v", err)
	}
}

func TestSigningKeyMetaHasNoPrivateField(t *testing.T) {
	// Structural guarantee (D-03): SigningKeyMeta never carries private key
	// bytes. A future refactor that adds PrivateEnc / PrivArmored should
	// fail this test.
	tp := reflect.TypeOf(metadata.SigningKeyMeta{})
	for i := 0; i < tp.NumField(); i++ {
		name := tp.Field(i).Name
		lowered := strings.ToLower(name)
		if strings.Contains(lowered, "private") || strings.Contains(lowered, "priv") {
			t.Fatalf("SigningKeyMeta must not carry private-key-bearing field %q", name)
		}
	}
}

func TestSigningKeysDelete(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	repoID := seedRPMRepo(t, db)
	repo := metadata.NewSigningKeysRepo(db, newAEAD(t))
	ctx := context.Background()
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := repo.Insert(ctx, tx, repoID, "pub", "priv", "fp")
		return err
	})
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return repo.Delete(ctx, tx, repoID)
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := repo.Lookup(ctx, repoID); !errors.Is(err, metadata.ErrNotFound) {
		t.Fatalf("after Delete: want ErrNotFound, got %v", err)
	}
}
