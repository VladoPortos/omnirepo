package rpm_test

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	omrcrypto "github.com/vladoportos/omnirepo/internal/crypto"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
	"github.com/vladoportos/omnirepo/internal/protocol/rpm"
)

func newSigningKeysRepo(t *testing.T) (*metadata.SigningKeysRepo, *metadata.DB) {
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

// seedRepoWithSigningKey creates a project + repo + signing_keys row and
// returns the (repoID, armored public key).
func seedRepoWithSigningKey(t *testing.T, db *metadata.DB, signKeys *metadata.SigningKeysRepo) (int64, string) {
	t.Helper()
	priv, pub, fp, err := omrcrypto.GenerateRepoKey("proj-myrepo", 2048)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	pid, err := metadata.NewProjectsRepo(db).Create(context.Background(), "proj", "")
	if err != nil {
		t.Fatalf("proj: %v", err)
	}
	rid, err := metadata.NewReposRepo(db).Create(context.Background(), pid, "rpm", "myrepo", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("repo: %v", err)
	}
	if err := db.WriteTx(context.Background(), func(tx *sql.Tx) error {
		_, err := signKeys.Insert(context.Background(), tx, rid, pub, priv, fp)
		return err
	}); err != nil {
		t.Fatalf("insert signing key: %v", err)
	}
	return rid, pub
}

func TestPublicKeyServeReturnsArmored(t *testing.T) {
	signKeys, db := newSigningKeysRepo(t)
	rid, pub := seedRepoWithSigningKey(t, db, signKeys)

	cache := rpm.NewPublicKeyCache(signKeys)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/x/rpm/y/public-key.asc", nil)
	cache.ServePublicKey(w, r, rid)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "application/pgp-keys" {
		t.Errorf("content-type=%q", got)
	}
	body := w.Body.String()
	if !strings.HasPrefix(body, "-----BEGIN PGP PUBLIC KEY BLOCK-----") {
		t.Errorf("not armored:\n%s", body)
	}
	if body != pub {
		t.Errorf("body != stored armored")
	}
}

func TestPublicKeyCached(t *testing.T) {
	signKeys, db := newSigningKeysRepo(t)
	rid, _ := seedRepoWithSigningKey(t, db, signKeys)

	cache := rpm.NewPublicKeyCache(signKeys)
	if _, err := cache.Lookup(context.Background(), rid); err != nil {
		t.Fatalf("first lookup: %v", err)
	}
	// Drop the row from under the cache; second lookup must still succeed
	// because it serves from the in-memory cache.
	if _, err := db.Writer.Exec(`DELETE FROM signing_keys WHERE repo_id=?`, rid); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := cache.Lookup(context.Background(), rid); err != nil {
		t.Fatalf("second lookup (should hit cache): %v", err)
	}
}

func TestPublicKeyMissReturnsNotFound(t *testing.T) {
	signKeys, _ := newSigningKeysRepo(t)
	cache := rpm.NewPublicKeyCache(signKeys)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/x/rpm/y/public-key.asc", nil)
	cache.ServePublicKey(w, r, 9999)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}
