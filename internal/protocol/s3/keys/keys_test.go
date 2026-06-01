package s3keys_test

import (
	"context"
	"database/sql"
	"regexp"
	"testing"
	"time"

	omrcrypto "github.com/vladoportos/omnirepo/internal/crypto"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
	s3keys "github.com/vladoportos/omnirepo/internal/protocol/s3/keys"
	"github.com/vladoportos/omnirepo/internal/protocol/s3/sigv4"
)

// akidRE matches the spec: AKIA + 16 upper-case base32 chars (A-Z2-7).
var akidRE = regexp.MustCompile(`^AKIA[A-Z2-7]{16}$`)

// secretRE matches the spec: 40 base64url chars (no padding).
var secretRE = regexp.MustCompile(`^[A-Za-z0-9_-]{40}$`)

// helper: create a Service backed by a real SQLite DB + AEAD.
func newTestService(t *testing.T) (*s3keys.Service, *metadata.S3KeysRepo, *omrcrypto.AEAD, *metadata.DB) {
	t.Helper()
	db := sqlitetest.New(t)
	key, err := omrcrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	aead, err := omrcrypto.New(key)
	if err != nil {
		t.Fatal(err)
	}
	repo := metadata.NewS3KeysRepo(db)
	svc := s3keys.NewService(repo, aead)
	return svc, repo, aead, db
}

// seedProject inserts a minimal project + user so FK constraints pass.
func seedProject(t *testing.T, db *metadata.DB) (projectID, userID int64) {
	t.Helper()
	ctx := context.Background()
	err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO users(login, email, password_hash) VALUES ('s3tester', 's3@test', 'x')`)
		if err != nil {
			return err
		}
		userID, _ = res.LastInsertId()
		res, err = tx.ExecContext(ctx, `INSERT INTO projects(name) VALUES ('testproj')`)
		if err != nil {
			return err
		}
		projectID, _ = res.LastInsertId()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return projectID, userID
}

// insertKey generates a key, encrypts the secret, and inserts the row.
// Returns the AKID and the plaintext secret.
func insertKey(t *testing.T, db *metadata.DB, repo *metadata.S3KeysRepo, aead *omrcrypto.AEAD, projectID, userID int64) (string, string) {
	t.Helper()
	akid, secret, err := s3keys.GenerateS3AccessKey()
	if err != nil {
		t.Fatal(err)
	}
	enc, err := aead.Encrypt([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	err = db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := repo.Insert(ctx, tx, &metadata.S3AccessKey{
			ProjectID:       projectID,
			AccessKeyID:     akid,
			SecretEnc:       []byte(enc),
			Label:           "test-key",
			CreatedByUserID: userID,
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return akid, secret
}

func TestGenerateS3AccessKey_FormatAndUniqueness(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for i := 0; i < 1000; i++ {
		akid, secret, err := s3keys.GenerateS3AccessKey()
		if err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
		if !akidRE.MatchString(akid) {
			t.Fatalf("iter %d: akid %q does not match %s", i, akid, akidRE)
		}
		if !secretRE.MatchString(secret) {
			t.Fatalf("iter %d: secret %q does not match %s", i, secret, secretRE)
		}
		if _, dup := seen[akid]; dup {
			t.Fatalf("iter %d: duplicate akid %s", i, akid)
		}
		seen[akid] = struct{}{}
	}
}

func TestLookup_RoundTrip(t *testing.T) {
	svc, repo, aead, db := newTestService(t)
	pid, uid := seedProject(t, db)
	akid, secret := insertKey(t, db, repo, aead, pid, uid)

	got, err := svc.Lookup(akid)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got != secret {
		t.Fatalf("Lookup returned %q; want %q", got, secret)
	}
}

func TestLookup_MissingKey(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	_, err := svc.Lookup("AKIAMISSING000000000")
	if err != sigv4.ErrInvalidAccessKeyId {
		t.Fatalf("Lookup missing: got %v; want ErrInvalidAccessKeyId", err)
	}
}

func TestLookup_RevokedKey(t *testing.T) {
	svc, repo, aead, db := newTestService(t)
	pid, uid := seedProject(t, db)
	akid, _ := insertKey(t, db, repo, aead, pid, uid)

	// Revoke the key.
	ctx := context.Background()
	row, err := repo.FindByAKID(ctx, akid)
	if err != nil {
		t.Fatal(err)
	}
	err = db.WriteTx(ctx, func(tx *sql.Tx) error {
		return repo.Revoke(ctx, tx, row.ID)
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = svc.Lookup(akid)
	if err != sigv4.ErrInvalidAccessKeyId {
		t.Fatalf("Lookup revoked: got %v; want ErrInvalidAccessKeyId", err)
	}
}

func TestLookup_TouchLastUsed(t *testing.T) {
	svc, repo, aead, db := newTestService(t)
	pid, uid := seedProject(t, db)
	akid, _ := insertKey(t, db, repo, aead, pid, uid)

	// Confirm last_used_at is nil before lookup.
	row, err := repo.FindByAKID(context.Background(), akid)
	if err != nil {
		t.Fatal(err)
	}
	if row.LastUsedAt != nil {
		t.Fatal("last_used_at should be nil before first lookup")
	}

	_, err = svc.Lookup(akid)
	if err != nil {
		t.Fatal(err)
	}

	// Wait a bit for the fire-and-forget goroutine.
	time.Sleep(100 * time.Millisecond)

	row, err = repo.FindByAKID(context.Background(), akid)
	if err != nil {
		t.Fatal(err)
	}
	if row.LastUsedAt == nil {
		t.Fatal("last_used_at should be set after Lookup")
	}
}

func TestResolveProject(t *testing.T) {
	svc, repo, aead, db := newTestService(t)
	pid, uid := seedProject(t, db)
	akid, _ := insertKey(t, db, repo, aead, pid, uid)

	gotPID, err := svc.ResolveProject(akid)
	if err != nil {
		t.Fatalf("ResolveProject: %v", err)
	}
	if gotPID != pid {
		t.Fatalf("ResolveProject: got %d; want %d", gotPID, pid)
	}

	// Missing key.
	_, err = svc.ResolveProject("AKIAMISSING000000000")
	if err != sigv4.ErrInvalidAccessKeyId {
		t.Fatalf("ResolveProject missing: got %v; want ErrInvalidAccessKeyId", err)
	}
}
