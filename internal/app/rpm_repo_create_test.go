package app_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/app"
	omrcrypto "github.com/dxc-internal/omnirepo/internal/crypto"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
)

func newSigningKeysRepoForApp(t *testing.T) (*metadata.SigningKeysRepo, *metadata.DB) {
	t.Helper()
	db := sqlitetest.New(t)
	key, err := omrcrypto.GenerateKey()
	if err != nil {
		t.Fatalf("aead key: %v", err)
	}
	aead, err := omrcrypto.New(key)
	if err != nil {
		t.Fatalf("aead: %v", err)
	}
	return metadata.NewSigningKeysRepo(db, aead), db
}

// TestRepoCreateRPMGeneratesSigningKey: when type=rpm, the hook generates
// + commits a signing_keys row inside the repo-create writer tx.
func TestRepoCreateRPMGeneratesSigningKey(t *testing.T) {
	signKeys, db := newSigningKeysRepoForApp(t)

	pid, err := metadata.NewProjectsRepo(db).Create(context.Background(), "proj", "")
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	repos := metadata.NewReposRepo(db)

	var (
		repoID int64
		fp     string
	)
	if err := db.WriteTx(context.Background(), func(tx *sql.Tx) error {
		rid, err := repos.CreateInTx(context.Background(), tx, pid, "rpm", "myrepo", "", nil, nil, nil)
		if err != nil {
			return err
		}
		repoID = rid
		gen := func(uid string, bits int) (priv, pub, fp string, err error) {
			// Use 2048-bit for speed in tests.
			return omrcrypto.GenerateRepoKey(uid, 2048)
		}
		fingerprint, err := app.CreateRPMRepoHook(context.Background(), tx, rid, "rpm", "proj", "myrepo", signKeys, 2048, gen)
		fp = fingerprint
		return err
	}); err != nil {
		t.Fatalf("tx: %v", err)
	}
	if fp == "" || len(fp) != 40 {
		t.Errorf("fingerprint=%q", fp)
	}
	meta, err := signKeys.Lookup(context.Background(), repoID)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if meta == nil || meta.Fingerprint != fp {
		t.Fatalf("got %+v want fp=%s", meta, fp)
	}
	if !strings.HasPrefix(meta.PublicArmored, "-----BEGIN PGP PUBLIC KEY BLOCK-----") {
		t.Errorf("public not armored: %q", meta.PublicArmored[:50])
	}
	if meta.KeyKind != "gpg_rsa4096" {
		t.Errorf("kind=%q", meta.KeyKind)
	}
}

// TestRepoCreateRPMKeyGenFailureRollsBack: a key-gen error must roll back
// the repos INSERT — atomic guarantee per T-03-04-06.
func TestRepoCreateRPMKeyGenFailureRollsBack(t *testing.T) {
	signKeys, db := newSigningKeysRepoForApp(t)

	pid, err := metadata.NewProjectsRepo(db).Create(context.Background(), "proj", "")
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	repos := metadata.NewReposRepo(db)

	failingGen := func(uid string, bits int) (priv, pub, fp string, err error) {
		return "", "", "", errors.New("simulated keygen failure")
	}
	txErr := db.WriteTx(context.Background(), func(tx *sql.Tx) error {
		rid, err := repos.CreateInTx(context.Background(), tx, pid, "rpm", "myrepo", "", nil, nil, nil)
		if err != nil {
			return err
		}
		_, err = app.CreateRPMRepoHook(context.Background(), tx, rid, "rpm", "proj", "myrepo", signKeys, 2048, failingGen)
		return err
	})
	if txErr == nil {
		t.Fatalf("expected tx err, got nil")
	}
	// Repo row must NOT exist.
	rr, err := repos.FindByTriple(context.Background(), pid, "rpm", "myrepo")
	if err != nil && !errors.Is(err, metadata.ErrNotFound) {
		t.Fatalf("FindByTriple: %v", err)
	}
	if rr != nil {
		t.Errorf("repos row should be rolled back, got %+v", rr)
	}
	// signing_keys row must NOT exist for any repo.
	var n int
	if err := db.Reader.QueryRow(`SELECT COUNT(*) FROM signing_keys`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("signing_keys count=%d after rollback", n)
	}
}

// TestRepoCreateRPMHookSkipsNonRPMTypes: type=raw|docker|helm|pypi → no key.
func TestRepoCreateRPMHookSkipsNonRPMTypes(t *testing.T) {
	signKeys, db := newSigningKeysRepoForApp(t)
	pid, _ := metadata.NewProjectsRepo(db).Create(context.Background(), "proj", "")
	repos := metadata.NewReposRepo(db)

	for _, typ := range []string{"raw", "helm", "pypi", "docker"} {
		t.Run(typ, func(t *testing.T) {
			var fp string
			if err := db.WriteTx(context.Background(), func(tx *sql.Tx) error {
				rid, err := repos.CreateInTx(context.Background(), tx, pid, typ, typ+"-r", "", nil, nil, nil)
				if err != nil {
					return err
				}
				fp, err = app.CreateRPMRepoHook(context.Background(), tx, rid, typ, "proj", typ+"-r", signKeys, 2048, nil)
				return err
			}); err != nil {
				t.Fatalf("tx: %v", err)
			}
			if fp != "" {
				t.Errorf("expected empty fp for type=%s, got %q", typ, fp)
			}
		})
	}
}

// TestRepoCreateRPMHookDEBAlsoGenerates: deb type also gets a key.
func TestRepoCreateRPMHookDEBAlsoGenerates(t *testing.T) {
	signKeys, db := newSigningKeysRepoForApp(t)
	pid, _ := metadata.NewProjectsRepo(db).Create(context.Background(), "proj", "")
	repos := metadata.NewReposRepo(db)

	gen := func(uid string, bits int) (string, string, string, error) {
		return omrcrypto.GenerateRepoKey(uid, 2048)
	}
	var (
		repoID int64
		fp     string
	)
	if err := db.WriteTx(context.Background(), func(tx *sql.Tx) error {
		rid, err := repos.CreateInTx(context.Background(), tx, pid, "deb", "debrepo", "", nil, nil, nil)
		if err != nil {
			return err
		}
		repoID = rid
		fp, err = app.CreateRPMRepoHook(context.Background(), tx, rid, "deb", "proj", "debrepo", signKeys, 2048, gen)
		return err
	}); err != nil {
		t.Fatalf("tx: %v", err)
	}
	if fp == "" {
		t.Fatalf("expected fp for type=deb")
	}
	meta, _ := signKeys.Lookup(context.Background(), repoID)
	if meta == nil || meta.Fingerprint != fp {
		t.Fatalf("lookup mismatch")
	}
}
