package backend_test

import (
	"bytes"
	"context"
	"github.com/johannesboyne/gofakes3"
	"io"
	"testing"
)

func TestRepairAliasesAndRollback(t *testing.T) {
	for _, mode := range []string{"alias", "put", "multipart"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			if err := f.b.CreateBucket("bucket1"); err != nil {
				t.Fatal(err)
			}
			if _, err := f.b.PutObject("bucket1", "a/b", nil, bytes.NewBufferString("old"), 3, nil); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "alias":
				for _, key := range []string{"a//b", "a/b/"} {
					if _, err := f.b.DeleteObject("bucket1", key); err == nil {
						t.Errorf("accepted alias %q", key)
					}
				}
			case "put", "multipart":
				if _, err := f.db.Writer.Exec(`CREATE TRIGGER fail_s3 BEFORE UPDATE ON s3_objects BEGIN SELECT RAISE(ABORT, 'failpoint'); END`); err != nil {
					t.Fatal(err)
				}
				if mode == "put" {
					if _, err := f.b.PutObject("bucket1", "a/b", nil, bytes.NewBufferString("new"), 3, nil); err == nil {
						t.Fatal("expected failure")
					}
				} else {
					id, err := fixtureCreateMPU(t, f, "bucket1", "a/b", nil)
					if err != nil {
						t.Fatal(err)
					}
					etag, err := f.b.UploadPart("bucket1", "a/b", id, 1, 3, bytes.NewBufferString("new"))
					if err != nil {
						t.Fatal(err)
					}
					if _, _, err := f.b.CompleteMultipartUpload("bucket1", "a/b", id, &gofakes3.CompleteMultipartUploadRequest{Parts: []gofakes3.CompletedPart{{PartNumber: 1, ETag: etag}}}); err == nil {
						t.Fatal("expected failure")
					}
				}
			}
			obj, err := f.b.GetObject("bucket1", "a/b", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer obj.Contents.Close()
			body, _ := io.ReadAll(obj.Contents)
			if string(body) != "old" {
				t.Fatalf("old payload lost: %q", body)
			}
		})
	}
}

func TestRepairLiteralPrefix(t *testing.T) {
	f := newFixture(t)
	if err := f.b.CreateBucket("bucket1"); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"a%one", "axone", "A%one", "a_one"} {
		if _, err := f.b.PutObject("bucket1", key, nil, bytes.NewBufferString("x"), 1, nil); err != nil {
			t.Fatal(err)
		}
	}
	var id int64
	if err := f.db.Reader.QueryRow(`SELECT id FROM s3_buckets WHERE name='bucket1'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	page, err := f.b.Objects.ListByBucket(context.Background(), id, "a%", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Objects) != 1 || page.Objects[0].Key != "a%one" {
		t.Fatalf("nonliteral prefix: %+v", page.Objects)
	}
}
