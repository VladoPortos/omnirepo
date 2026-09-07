package jobs_test

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/vladoportos/omnirepo/internal/storage"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type pausedDeleteCAS struct {
	storage.CAS
	entered chan struct{}
	resume  chan struct{}
}

func (c *pausedDeleteCAS) Delete(ctx context.Context, d string) error {
	close(c.entered)
	<-c.resume
	return c.CAS.Delete(ctx, d)
}
func TestGCReuploadDuringUnlinkPreservesBytes(t *testing.T) {
	for _, fromPath := range []bool{false, true} {
		t.Run(fmt.Sprint("fromPath=", fromPath), func(t *testing.T) {
			f := newGCFixture(t, time.Hour, time.Hour)
			ctx := context.Background()
			body := []byte("reuploaded orphan")
			digest, _, err := f.cas.Put(ctx, bytesReader(body))
			if err != nil {
				t.Fatal(err)
			}
			f.seedOrphanBlob(digest, body, 2*time.Hour)
			wrapped := &pausedDeleteCAS{CAS: f.cas, entered: make(chan struct{}), resume: make(chan struct{})}
			f.handler.CAS = wrapped
			done := make(chan error, 1)
			job := f.enqueueGCJob()
			go func() { done <- f.handler.Handle(ctx, job) }()
			<-wrapped.entered
			uploaded := make(chan error, 1)
			go func() {
				var err error
				if fromPath {
					tmp := filepath.Join(f.dataRoot, "upload-again")
					err = os.WriteFile(tmp, body, 0600)
					if err == nil {
						_, _, err = f.cas.PutFromPath(ctx, tmp)
					}
				} else {
					_, _, err = f.cas.Put(ctx, bytesReader(body))
				}
				if err == nil {
					err = f.db.WriteTx(ctx, func(tx *sql.Tx) error { return f.blobs.UpsertZeroRef(ctx, tx, digest, int64(len(body))) })
				}
				uploaded <- err
			}()
			// A promotion must not finish while GC is between row deletion and unlink.
			select {
			case err := <-uploaded:
				if err != nil {
					t.Fatal(err)
				}
				t.Error("CAS promotion passed GC's pending unlink")
				close(wrapped.resume)
			case <-time.After(100 * time.Millisecond):
				close(wrapped.resume)
				if err := <-uploaded; err != nil {
					t.Fatal(err)
				}
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			exists, err := f.cas.Exists(ctx, digest)
			if err != nil || !exists {
				t.Fatalf("republished bytes missing: exists=%v err=%v", exists, err)
			}
		})
	}
}
