package metadata_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
)

// TestReposRepo_WipeDocker_BlobSharedAcrossManifestsInSameRepo pins the H-4
// fix: when two manifests in the SAME repo reference one blob, that blob's
// ref_count is 2. Wiping the repo removes both manifests, so the blob must drop
// to 0 (GC-eligible). The previous WipeDocker collapsed referenced digests into
// a set and DecRef'd once, leaving the blob stuck at 1 forever — a permanent
// CAS leak.
func TestReposRepo_WipeDocker_BlobSharedAcrossManifestsInSameRepo(t *testing.T) {
	db := sqlitetest.New(t)
	ctx := context.Background()
	pid := seedProject(t, db, "p")
	reposRepo := metadata.NewReposRepo(db)
	rid, err := reposRepo.Create(ctx, pid, "docker", "r1", "", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	blobs := metadata.NewDockerBlobsRepo(db)
	manis := metadata.NewDockerManifestsRepo(db)
	tags := metadata.NewDockerTagsRepo(db)

	shared := "sha256:sharedlayer"
	cfg1 := "sha256:cfg1"
	cfg2 := "sha256:cfg2"

	err = db.WriteTx(ctx, func(tx *sql.Tx) error {
		for _, d := range []struct {
			dg   string
			size int64
		}{{shared, 500}, {cfg1, 10}, {cfg2, 10}} {
			if err := blobs.UpsertZeroRef(ctx, tx, d.dg, d.size); err != nil {
				return err
			}
		}
		// Two distinct manifests in the SAME repo, each referencing the shared
		// layer (plus a private config). Mirrors how the push handler IncRefs
		// once per manifest row.
		body1 := []byte(`{"schemaVersion":2,"config":{"digest":"` + cfg1 + `"},"layers":[{"digest":"` + shared + `"}]}`)
		body2 := []byte(`{"schemaVersion":2,"config":{"digest":"` + cfg2 + `"},"layers":[{"digest":"` + shared + `"}]}`)
		if err := manis.Insert(ctx, tx, rid, "sha256:m1", "application/vnd.oci.image.manifest.v1+json", body1); err != nil {
			return err
		}
		if err := blobs.IncRef(ctx, tx, cfg1); err != nil {
			return err
		}
		if err := blobs.IncRef(ctx, tx, shared); err != nil {
			return err
		}
		if err := manis.Insert(ctx, tx, rid, "sha256:m2", "application/vnd.oci.image.manifest.v1+json", body2); err != nil {
			return err
		}
		if err := blobs.IncRef(ctx, tx, cfg2); err != nil {
			return err
		}
		if err := blobs.IncRef(ctx, tx, shared); err != nil {
			return err
		}
		if _, err := tags.Upsert(ctx, tx, rid, "", "v1", "sha256:m1"); err != nil {
			return err
		}
		if _, err := tags.Upsert(ctx, tx, rid, "", "v2", "sha256:m2"); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	if b, _ := blobs.Stat(ctx, shared); b == nil || b.RefCount != 2 {
		t.Fatalf("shared refcount pre: %+v (want 2 — referenced by m1 and m2)", b)
	}

	var bytesFreed int64
	err = db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, bytesFreed, err = reposRepo.WipeDocker(ctx, tx, rid)
		return err
	})
	if err != nil {
		t.Fatalf("WipeDocker: %v", err)
	}

	// Both manifests gone → shared layer no longer referenced → ref_count 0.
	if b, _ := blobs.Stat(ctx, shared); b == nil || b.RefCount != 0 {
		t.Fatalf("shared refcount post: %+v (want 0 — both referencing manifests wiped)", b)
	}
	if b, _ := blobs.Stat(ctx, cfg1); b == nil || b.RefCount != 0 {
		t.Fatalf("cfg1 refcount post: %+v (want 0)", b)
	}
	// bytes_freed must include the shared layer (now at 0): 500 + 10 + 10 = 520.
	if bytesFreed != 520 {
		t.Fatalf("bytes_freed=%d, want 520 (shared 500 + cfg1 10 + cfg2 10 all hit 0)", bytesFreed)
	}
}
