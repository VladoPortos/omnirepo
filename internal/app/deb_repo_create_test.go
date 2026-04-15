package app_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/app"
	omrcrypto "github.com/dxc-internal/omnirepo/internal/crypto"
	"github.com/dxc-internal/omnirepo/internal/metadata"
)

// TestCreateDEBRepoHookSeedsDefaultMatrix verifies the 3 default apt_suites
// rows commit atomically with the repos INSERT (D-23).
func TestCreateDEBRepoHookSeedsDefaultMatrix(t *testing.T) {
	signKeys, db := newSigningKeysRepoForApp(t)
	aptSuites := metadata.NewAptSuitesRepo(db)

	pid, err := metadata.NewProjectsRepo(db).Create(context.Background(), "proj", "")
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	repos := metadata.NewReposRepo(db)

	gen := func(uid string, bits int) (string, string, string, error) {
		return omrcrypto.GenerateRepoKey(uid, 2048)
	}
	var repoID int64
	if err := db.WriteTx(context.Background(), func(tx *sql.Tx) error {
		rid, err := repos.CreateInTx(context.Background(), tx, pid, "deb", "myrepo", "", nil, nil, nil)
		if err != nil {
			return err
		}
		repoID = rid
		if _, err := app.CreateRPMRepoHook(context.Background(), tx, rid, "deb", "proj", "myrepo", signKeys, 2048, gen); err != nil {
			return err
		}
		return app.CreateDEBRepoHook(context.Background(), tx, rid, "deb", aptSuites)
	}); err != nil {
		t.Fatalf("tx: %v", err)
	}

	// Signing key committed.
	meta, err := signKeys.Lookup(context.Background(), repoID)
	if err != nil || meta == nil {
		t.Fatalf("signing key not committed: %v", err)
	}
	// 3 apt_suites rows committed in the same tx.
	rows, err := aptSuites.ListByRepo(context.Background(), repoID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d apt_suites rows, want 3: %+v", len(rows), rows)
	}
	want := map[string]bool{"amd64": true, "arm64": true, "all": true}
	for _, r := range rows {
		if r.Suite != "stable" || r.Component != "main" {
			t.Errorf("row %+v not (stable, main)", r)
		}
		if !want[r.Architecture] {
			t.Errorf("unexpected arch %q", r.Architecture)
		}
	}
}

// TestCreateDEBRepoHookSkipsNonDEBTypes verifies type != deb is a no-op.
func TestCreateDEBRepoHookSkipsNonDEBTypes(t *testing.T) {
	_, db := newSigningKeysRepoForApp(t)
	aptSuites := metadata.NewAptSuitesRepo(db)
	pid, _ := metadata.NewProjectsRepo(db).Create(context.Background(), "proj", "")
	repos := metadata.NewReposRepo(db)

	for _, typ := range []string{"raw", "rpm", "helm", "pypi", "docker"} {
		t.Run(typ, func(t *testing.T) {
			var repoID int64
			if err := db.WriteTx(context.Background(), func(tx *sql.Tx) error {
				rid, err := repos.CreateInTx(context.Background(), tx, pid, typ, typ+"-r", "", nil, nil, nil)
				if err != nil {
					return err
				}
				repoID = rid
				return app.CreateDEBRepoHook(context.Background(), tx, rid, typ, aptSuites)
			}); err != nil {
				t.Fatalf("tx: %v", err)
			}
			rows, _ := aptSuites.ListByRepo(context.Background(), repoID)
			if len(rows) != 0 {
				t.Errorf("type=%s got %d apt_suites rows", typ, len(rows))
			}
		})
	}
}

// TestCreateDEBRepoHookRollback: if the downstream signing-key hook fails,
// the apt_suites rows must also roll back (atomicity).
func TestCreateDEBRepoHookRollback(t *testing.T) {
	signKeys, db := newSigningKeysRepoForApp(t)
	aptSuites := metadata.NewAptSuitesRepo(db)
	pid, _ := metadata.NewProjectsRepo(db).Create(context.Background(), "proj", "")
	repos := metadata.NewReposRepo(db)

	failingGen := func(uid string, bits int) (string, string, string, error) {
		return "", "", "", errInjectedFailure{}
	}
	txErr := db.WriteTx(context.Background(), func(tx *sql.Tx) error {
		rid, err := repos.CreateInTx(context.Background(), tx, pid, "deb", "myrepo", "", nil, nil, nil)
		if err != nil {
			return err
		}
		// Insert default suites first — they should roll back when the
		// signing-key hook below fails.
		if err := app.CreateDEBRepoHook(context.Background(), tx, rid, "deb", aptSuites); err != nil {
			return err
		}
		_, err = app.CreateRPMRepoHook(context.Background(), tx, rid, "deb", "proj", "myrepo", signKeys, 2048, failingGen)
		return err
	})
	if txErr == nil {
		t.Fatalf("expected tx err, got nil")
	}
	var n int
	_ = db.Reader.QueryRow(`SELECT COUNT(*) FROM apt_suites`).Scan(&n)
	if n != 0 {
		t.Errorf("apt_suites rows=%d after rollback", n)
	}
}

type errInjectedFailure struct{}

func (errInjectedFailure) Error() string { return "injected keygen failure" }
