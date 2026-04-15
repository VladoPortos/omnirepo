package deb_test

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	omrcrypto "github.com/dxc-internal/omnirepo/internal/crypto"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
	"github.com/dxc-internal/omnirepo/internal/protocol/deb"
)

func newSigningKeysRepoForDEB(t *testing.T) (*metadata.SigningKeysRepo, *metadata.DB) {
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

func seedDEBRepoWithKey(t *testing.T, db *metadata.DB, sk *metadata.SigningKeysRepo) (int64, string) {
	t.Helper()
	priv, pub, fp, err := omrcrypto.GenerateRepoKey("proj-debrepo-omnirepo", 2048)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	pid, err := metadata.NewProjectsRepo(db).Create(context.Background(), "proj", "")
	if err != nil {
		t.Fatalf("proj: %v", err)
	}
	rid, err := metadata.NewReposRepo(db).Create(context.Background(), pid, "deb", "debrepo", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("repo: %v", err)
	}
	if err := db.WriteTx(context.Background(), func(tx *sql.Tx) error {
		_, err := sk.Insert(context.Background(), tx, rid, pub, priv, fp)
		return err
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	return rid, pub
}

func TestPublicKeyServeArmored(t *testing.T) {
	sk, db := newSigningKeysRepoForDEB(t)
	rid, pub := seedDEBRepoWithKey(t, db, sk)

	cache := deb.NewPublicKeyCache(sk)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/proj/deb/debrepo/public-key.asc", nil)
	cache.ServePublicKey(w, r, rid)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "application/pgp-keys" {
		t.Errorf("ct=%q", got)
	}
	if !strings.HasPrefix(w.Body.String(), "-----BEGIN PGP PUBLIC KEY BLOCK-----") {
		t.Errorf("not armored:\n%s", w.Body.String())
	}
	if w.Body.String() != pub {
		t.Errorf("body != stored armored")
	}
}

func TestPublicKeyMissReturnsNotFound(t *testing.T) {
	sk, _ := newSigningKeysRepoForDEB(t)
	cache := deb.NewPublicKeyCache(sk)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/x/deb/y/public-key.asc", nil)
	cache.ServePublicKey(w, r, 9999)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestPublicKeyCached(t *testing.T) {
	sk, db := newSigningKeysRepoForDEB(t)
	rid, _ := seedDEBRepoWithKey(t, db, sk)

	cache := deb.NewPublicKeyCache(sk)
	if _, err := cache.Lookup(context.Background(), rid); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := db.Writer.Exec(`DELETE FROM signing_keys WHERE repo_id=?`, rid); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// Second lookup must still hit cache.
	if _, err := cache.Lookup(context.Background(), rid); err != nil {
		t.Fatalf("cache miss after delete (expected hit): %v", err)
	}
}
