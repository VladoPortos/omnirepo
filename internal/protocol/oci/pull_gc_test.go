package oci_test

import (
	"bytes"
	"context"
	"database/sql"
	"github.com/vladoportos/omnirepo/internal/jobs"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/protocol/oci"
	"github.com/vladoportos/omnirepo/internal/storage"
	"io"
	"path/filepath"
	"testing"
	"time"
)

type sweepAfterPut struct {
	storage.CAS
	gc *jobs.GCHandler
}

func (s *sweepAfterPut) Put(ctx context.Context, r io.Reader) (string, int64, error) {
	d, n, err := s.CAS.Put(ctx, r)
	if err == nil {
		err = s.gc.Handle(ctx, 0)
	}
	return d, n, err
}
func TestPullExternalReusedOrphanSurvivesGCBeforeManifestCommit(t *testing.T) {
	f := newPullFixture(t, false)
	ctx := context.Background()
	for digest, body := range f.up.blobs {
		if _, _, err := f.cas.Put(ctx, bytes.NewReader(body)); err != nil {
			t.Fatal(err)
		}
		if err := f.db.WriteTx(ctx, func(tx *sql.Tx) error {
			if err := f.blobs.UpsertZeroRef(ctx, tx, digest, int64(len(body))); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `UPDATE docker_blobs SET last_touched_at=datetime('now','-2 hours') WHERE digest=?`, digest)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	uploads := metadata.NewBlobUploadsRepo(f.db)
	gc := jobs.NewGCHandler(jobs.GCHandler{DB: f.db, Blobs: f.blobs, BlobUploads: uploads, Sessions: metadata.NewBlobUploadSessionsRepo(f.db), CAS: f.cas, Trash: storage.NewTrash(filepath.Join(f.dataRoot, "trash")), DataRoot: f.dataRoot, Quiescence: time.Hour})
	h := oci.New(oci.Deps{DB: f.db, Repos: f.repos, Projects: f.projects, Blobs: f.blobs, Manifests: f.manifests, Tags: f.tags})
	f.pull = oci.NewPullExternalHandler(oci.PullExternalDeps{DB: f.db, CAS: &sweepAfterPut{CAS: f.cas, gc: gc}, Blobs: f.blobs, Repos: f.repos, Projects: f.projects, Creds: f.creds, SyncJobs: metadata.NewSyncJobsRepo(f.db), OCI: h})
	if err := f.runPull(oci.PullExternalJob{SrcImage: f.up.srcImageRef(), DstTag: "reused"}); err != nil {
		t.Fatal(err)
	}
	for d := range f.up.blobs {
		if owned, err := f.blobs.HasInRepo(ctx, f.repoID, d); err != nil || !owned {
			t.Fatalf("pulled blob not owned: %v %v", owned, err)
		}
		if exists, err := f.cas.Exists(ctx, d); err != nil || !exists {
			t.Fatalf("blob lost %s: %v", d, err)
		}
	}
}
