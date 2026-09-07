package migrations_test

import (
	"context"
	"github.com/vladoportos/omnirepo/internal/metadata/migrations"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

func TestMigration044BackfillsOnlyManifestBlobs(t *testing.T) {
	db := openFreshDB(t)
	ctx := context.Background()
	legacy := fstest.MapFS{}
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() < "044" && strings.HasSuffix(e.Name(), ".up.sql") {
			body, err := fs.ReadFile(migrations.FS, e.Name())
			if err != nil {
				t.Fatal(err)
			}
			legacy[e.Name()] = &fstest.MapFile{Data: body}
		}
	}
	if _, err := migrations.ApplyFSForTest(ctx, db.Writer, legacy); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO projects(id,name) VALUES(1,'p')`,
		`INSERT INTO repos(id,project_id,type,name) VALUES(1,1,'docker','a'),(2,1,'docker','b')`,
		`UPDATE repos SET deleted_at=CURRENT_TIMESTAMP WHERE id=1`,
		`INSERT INTO docker_manifests(repo_id,digest,media_type,body,size_bytes) VALUES(2,'invalid-layers','application/vnd.oci.image.manifest.v1+json',CAST('{"layers":{"nested":{"digest":"unrelated"}}}' AS BLOB),1)`,
		`INSERT INTO docker_manifests(repo_id,digest,media_type,body,size_bytes) VALUES(2,'index','application/vnd.oci.image.index.v1+json',CAST('{"manifests":[],"config":{"digest":"unrelated"},"layers":[{"digest":"unrelated"}]}' AS BLOB),1)`,
		`INSERT INTO docker_blobs(digest,size_bytes) VALUES('config',1),('layer',1),('unrelated',1)`,
		`INSERT INTO docker_manifests(repo_id,digest,media_type,body,size_bytes) VALUES(1,'manifest','application/vnd.oci.image.manifest.v1+json',CAST('{"config":{"digest":"config"},"layers":[{"digest":"layer"},"bad"],"annotations":{"digest":"unrelated"}}' AS BLOB),1)`,
	} {
		if _, err := db.Writer.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := migrations.Apply(ctx, db.Writer); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Reader.QueryContext(ctx, `SELECT repo_id,digest FROM docker_repo_blobs ORDER BY digest`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var repo int64
		var digest string
		if err := rows.Scan(&repo, &digest); err != nil {
			t.Fatal(err)
		}
		if repo != 1 {
			t.Errorf("unrelated repo linked: %d", repo)
		}
		got = append(got, digest)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "config,layer" {
		t.Fatalf("backfill=%v", got)
	}
}
