package metadata_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
)

func seedUser(t *testing.T, db *metadata.DB, login string) int64 {
	t.Helper()
	repo := metadata.NewUsersRepo(db)
	id, err := repo.Create(context.Background(), login, login+"@x", "h", false, false)
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return id
}

func TestSessionsRepo_CreateAndFind(t *testing.T) {
	db := sqlitetest.New(t)
	uid := seedUser(t, db, "alice")
	repo := metadata.NewSessionsRepo(db)
	ctx := context.Background()
	now := time.Now().UTC()
	id, err := repo.Create(ctx, uid, "abcdefgh", "sha", now, now.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	s, err := repo.FindByPrefixSha(ctx, "abcdefgh", "sha")
	if err != nil {
		t.Fatalf("FindByPrefixSha: %v", err)
	}
	if s.ID != id || s.UserID != uid {
		t.Fatalf("unexpected session %+v", s)
	}
}

func TestSessionsRepo_FindWrongShaReturnsNotFound(t *testing.T) {
	db := sqlitetest.New(t)
	uid := seedUser(t, db, "alice")
	repo := metadata.NewSessionsRepo(db)
	ctx := context.Background()
	now := time.Now().UTC()
	if _, err := repo.Create(ctx, uid, "abcdefgh", "sha", now, now.Add(24*time.Hour)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, err := repo.FindByPrefixSha(ctx, "abcdefgh", "wrongsha")
	if !errors.Is(err, metadata.ErrNotFound) {
		t.Fatalf("wrong sha: %v, want ErrNotFound", err)
	}
}

func TestSessionsRepo_FindExpiredReturnsNotFound(t *testing.T) {
	db := sqlitetest.New(t)
	uid := seedUser(t, db, "alice")
	repo := metadata.NewSessionsRepo(db)
	ctx := context.Background()
	past := time.Now().Add(-1 * time.Hour).UTC()
	if _, err := repo.Create(ctx, uid, "abcdefgh", "sha", past.Add(-24*time.Hour), past); err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, err := repo.FindByPrefixSha(ctx, "abcdefgh", "sha")
	if !errors.Is(err, metadata.ErrNotFound) {
		t.Fatalf("expired: %v, want ErrNotFound", err)
	}
}

func TestSessionsRepo_TouchLastSeen(t *testing.T) {
	db := sqlitetest.New(t)
	uid := seedUser(t, db, "alice")
	repo := metadata.NewSessionsRepo(db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	id, err := repo.Create(ctx, uid, "abcdefgh", "sha", now, now.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	later := now.Add(5 * time.Minute)
	if err := repo.TouchLastSeen(ctx, id, later); err != nil {
		t.Fatalf("TouchLastSeen: %v", err)
	}
	s, err := repo.FindByPrefixSha(ctx, "abcdefgh", "sha")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if !s.LastSeenAt.Equal(later) {
		t.Fatalf("LastSeenAt: %v, want %v", s.LastSeenAt, later)
	}
}

func TestSessionsRepo_Delete(t *testing.T) {
	db := sqlitetest.New(t)
	uid := seedUser(t, db, "alice")
	repo := metadata.NewSessionsRepo(db)
	ctx := context.Background()
	now := time.Now().UTC()
	id, err := repo.Create(ctx, uid, "abcdefgh", "sha", now, now.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := repo.Delete(ctx, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, err = repo.FindByPrefixSha(ctx, "abcdefgh", "sha")
	if !errors.Is(err, metadata.ErrNotFound) {
		t.Fatalf("post-delete: %v, want ErrNotFound", err)
	}
}
