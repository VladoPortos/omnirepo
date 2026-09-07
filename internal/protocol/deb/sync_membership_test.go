package deb_test

import (
	"context"
	"database/sql"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"os"
	"path/filepath"
	"testing"
)

func TestDEBSyncReusesDigestAcrossSuiteMemberships(t *testing.T) {
	for _, prior := range []struct{ suite, component, arch string }{
		{"oldstable", "main", "amd64"}, {"stable", "contrib", "amd64"}, {"stable", "main", "arm64"},
	} {
		t.Run(prior.suite+"/"+prior.component+"/"+prior.arch, func(t *testing.T) {
			f := newDEBDriftFixture(t)
			seedDEBRow(t, f, "acme", "1.0", "all", "abc123")
			if err := f.db.WriteTx(context.Background(), func(tx *sql.Tx) error {
				sid, err := metadata.NewAptSuitesRepo(f.db).Insert(context.Background(), tx, f.repoID, prior.suite, prior.component, prior.arch)
				if err != nil {
					return err
				}
				_, err = tx.Exec(`UPDATE deb_packages SET suite_id=? WHERE repo_id=?`, sid, f.repoID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			f.setPackages(debPackagesEntry("acme", "1.0", "all", "abc123"))
			debRunSync(t, f)
			rows, err := metadata.NewDEBPackagesRepo(f.db).ListByRepo(context.Background(), f.repoID)
			if err != nil || len(rows) != 2 {
				t.Fatalf("want two suite publications, got %d: %v", len(rows), err)
			}
		})
	}
}

func TestDEBDriftRetainsSharedPoolBytes(t *testing.T) {
	f := newDEBDriftFixture(t)
	seedDEBRow(t, f, "acme", "1.0", "amd64", "abc123")
	row, err := metadata.NewDEBPackagesRepo(f.db).FindByDigest(context.Background(), f.repoID, "sha256:abc123")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.WriteTx(context.Background(), func(tx *sql.Tx) error {
		sid, err := metadata.NewAptSuitesRepo(f.db).Insert(context.Background(), tx, f.repoID, "oldstable", "main", "amd64")
		if err != nil {
			return err
		}
		cp := *row
		cp.SuiteID = sid
		_, err = metadata.NewDEBPackagesRepo(f.db).Insert(context.Background(), tx, &cp)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// Keep another upstream entry so the empty-upstream safeguard does not fire.
	seedDEBRow(t, f, "keep", "1.0", "amd64", "def456")
	f.setPackages(debPackagesEntry("keep", "1.0", "amd64", "def456"))
	debEnableDriftPurge(t, f.db, f.repoID)
	debRunSync(t, f)
	path := filepath.Join(f.dataRoot, "repos", "dp", "deb", "r1", row.StoragePoolPath)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("retained oldstable package bytes moved: %v", err)
	}
	if n := debTrashCount(t, f.trashRoot, "deb_package_drift"); n != 1 {
		t.Fatalf("want restore snapshot for removed membership, got %d", n)
	}
}
