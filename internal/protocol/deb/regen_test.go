package deb_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pgpopen "github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/clearsign"

	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/protocol/deb"
	"github.com/dxc-internal/omnirepo/internal/storage"
)

// debRegenFixture seeds a project + deb repo + signing key + apt_suites
// matrix + N deb_packages rows and returns everything RegenFor needs.
type debRegenFixture struct {
	t           *testing.T
	db          *metadata.DB
	repos       *metadata.ReposRepo
	debPackages *metadata.DEBPackagesRepo
	aptSuites   *metadata.AptSuitesRepo
	signKeys    *metadata.SigningKeysRepo
	projects    *metadata.ProjectsRepo
	repoRoot    string
	repoID      int64
	repoDir     string
	pubArmored  string
	suiteRows   map[string]int64 // (suite|component|arch) -> suite_id
}

func newDEBRegenFixture(t *testing.T, suites []metadata.AptSuite) *debRegenFixture {
	t.Helper()
	sk, db := newSigningKeysRepoForDEB(t)
	rid, pub := seedDEBRepoWithKey(t, db, sk)

	aptSuites := metadata.NewAptSuitesRepo(db)
	debPackages := metadata.NewDEBPackagesRepo(db)

	suiteRows := make(map[string]int64)
	if err := db.WriteTx(context.Background(), func(tx *sql.Tx) error {
		for _, s := range suites {
			id, err := aptSuites.Insert(context.Background(), tx, rid, s.Suite, s.Component, s.Architecture)
			if err != nil {
				return err
			}
			suiteRows[s.Suite+"|"+s.Component+"|"+s.Architecture] = id
		}
		return nil
	}); err != nil {
		t.Fatalf("seed suites: %v", err)
	}

	root := t.TempDir()
	repoDir := filepath.Join(root, "proj", "deb", "debrepo")
	if err := os.MkdirAll(repoDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	return &debRegenFixture{
		t: t, db: db,
		repos:       metadata.NewReposRepo(db),
		debPackages: debPackages,
		aptSuites:   aptSuites,
		signKeys:    sk,
		projects:    metadata.NewProjectsRepo(db),
		repoRoot:    root,
		repoID:      rid,
		repoDir:     repoDir,
		pubArmored:  pub,
		suiteRows:   suiteRows,
	}
}

func (f *debRegenFixture) seedPackage(suiteKey, pkg, version, arch string) {
	f.t.Helper()
	sid, ok := f.suiteRows[suiteKey]
	if !ok {
		f.t.Fatalf("unknown suiteKey %q", suiteKey)
	}
	if err := f.db.WriteTx(context.Background(), func(tx *sql.Tx) error {
		_, err := f.debPackages.Insert(context.Background(), tx, &metadata.DEBPackage{
			RepoID: f.repoID, SuiteID: sid,
			Package: pkg, Version: version, Architecture: arch,
			Maintainer: "Test <t@e.com>", Section: "misc", Priority: "optional",
			Depends:     "libc6",
			Description: pkg + " summary\n multi-line\n",
			SizeBytes:   1024, Digest: "sha256:" + strings.Repeat("a", 64),
			Filename: pkg + "_" + version + "_" + arch + ".deb",
		})
		return err
	}); err != nil {
		f.t.Fatalf("insert pkg: %v", err)
	}
}

func (f *debRegenFixture) regenFn() func(context.Context) error {
	return deb.RegenFor(deb.RegenDeps{
		DB:          f.db,
		Repos:       f.repos,
		Projects:    f.projects,
		DEBPackages: f.debPackages,
		AptSuites:   f.aptSuites,
		SigningKeys: f.signKeys,
		Locks:       storage.NewLocks(),
		RepoRoot:    f.repoRoot,
		RepoID:      f.repoID,
	})
}

func TestDEBRegenSingleSuite(t *testing.T) {
	f := newDEBRegenFixture(t, []metadata.AptSuite{
		{Suite: "stable", Component: "main", Architecture: "amd64"},
		{Suite: "stable", Component: "main", Architecture: "arm64"},
	})
	f.seedPackage("stable|main|amd64", "mypkg", "1.0-1", "amd64")
	f.seedPackage("stable|main|arm64", "mypkg", "1.0-1", "arm64")

	if err := f.regenFn()(context.Background()); err != nil {
		t.Fatalf("regen: %v", err)
	}
	distsDir := filepath.Join(f.repoDir, "dists", "stable")
	for _, want := range []string{
		"Release", "InRelease", "Release.gpg",
		"main/binary-amd64/Packages", "main/binary-amd64/Packages.gz",
		"main/binary-arm64/Packages", "main/binary-arm64/Packages.gz",
	} {
		if _, err := os.Stat(filepath.Join(distsDir, want)); err != nil {
			t.Errorf("missing %s: %v", want, err)
		}
	}
	state, _, err := f.repos.GetMetadataState(context.Background(), f.repoID)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if state != metadata.MetadataStateClean {
		t.Errorf("state=%q want clean", state)
	}
}

func TestDEBRegenMultiSuite(t *testing.T) {
	f := newDEBRegenFixture(t, []metadata.AptSuite{
		{Suite: "stable", Component: "main", Architecture: "amd64"},
		{Suite: "testing", Component: "main", Architecture: "amd64"},
	})
	f.seedPackage("stable|main|amd64", "mypkg", "1.0-1", "amd64")
	f.seedPackage("testing|main|amd64", "newpkg", "2.0-1", "amd64")

	if err := f.regenFn()(context.Background()); err != nil {
		t.Fatalf("regen: %v", err)
	}
	for _, suite := range []string{"stable", "testing"} {
		if _, err := os.Stat(filepath.Join(f.repoDir, "dists", suite, "InRelease")); err != nil {
			t.Errorf("missing dists/%s/InRelease: %v", suite, err)
		}
	}
}

func TestDEBRegenInReleaseVerifies(t *testing.T) {
	f := newDEBRegenFixture(t, []metadata.AptSuite{
		{Suite: "stable", Component: "main", Architecture: "amd64"},
	})
	f.seedPackage("stable|main|amd64", "mypkg", "1.0-1", "amd64")

	if err := f.regenFn()(context.Background()); err != nil {
		t.Fatalf("regen: %v", err)
	}
	inrelease, err := os.ReadFile(filepath.Join(f.repoDir, "dists", "stable", "InRelease"))
	if err != nil {
		t.Fatalf("read InRelease: %v", err)
	}
	block, rest := clearsign.Decode(inrelease)
	if block == nil {
		t.Fatalf("clearsign.Decode returned nil; rest=%q", rest)
	}
	keyring, err := pgpopen.ReadArmoredKeyRing(strings.NewReader(f.pubArmored))
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	_, err = pgpopen.CheckDetachedSignature(keyring, bytes.NewReader(block.Bytes), block.ArmoredSignature.Body, nil)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestDEBRegenReleaseGpgVerifies(t *testing.T) {
	f := newDEBRegenFixture(t, []metadata.AptSuite{
		{Suite: "stable", Component: "main", Architecture: "amd64"},
	})
	f.seedPackage("stable|main|amd64", "mypkg", "1.0-1", "amd64")

	if err := f.regenFn()(context.Background()); err != nil {
		t.Fatalf("regen: %v", err)
	}
	distsDir := filepath.Join(f.repoDir, "dists", "stable")
	release, err := os.ReadFile(filepath.Join(distsDir, "Release"))
	if err != nil {
		t.Fatalf("read release: %v", err)
	}
	sig, err := os.ReadFile(filepath.Join(distsDir, "Release.gpg"))
	if err != nil {
		t.Fatalf("read sig: %v", err)
	}
	keyring, err := pgpopen.ReadArmoredKeyRing(strings.NewReader(f.pubArmored))
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	signer, err := pgpopen.CheckArmoredDetachedSignature(keyring, bytes.NewReader(release), bytes.NewReader(sig), nil)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if signer == nil {
		t.Errorf("nil signer")
	}
}

func TestDEBRegenAtomicSwapLeavesNoTmp(t *testing.T) {
	f := newDEBRegenFixture(t, []metadata.AptSuite{
		{Suite: "stable", Component: "main", Architecture: "amd64"},
	})
	f.seedPackage("stable|main|amd64", "mypkg", "1.0-1", "amd64")

	if err := f.regenFn()(context.Background()); err != nil {
		t.Fatalf("regen: %v", err)
	}
	distsDir := filepath.Join(f.repoDir, "dists")
	entries, err := os.ReadDir(distsDir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, ".tmp") {
			t.Errorf("stale .tmp dir: %s", name)
		}
		if strings.HasPrefix(name, ".trash-") {
			t.Errorf("stale trash dir: %s", name)
		}
		if strings.HasPrefix(name, ".tmp-deb-regen") {
			t.Errorf("stale regen scratch: %s", name)
		}
	}
}

func TestDEBRegenDeterministic(t *testing.T) {
	f := newDEBRegenFixture(t, []metadata.AptSuite{
		{Suite: "stable", Component: "main", Architecture: "amd64"},
	})
	f.seedPackage("stable|main|amd64", "alpha", "1.0", "amd64")
	f.seedPackage("stable|main|amd64", "beta", "2.0", "amd64")

	if err := f.regenFn()(context.Background()); err != nil {
		t.Fatalf("regen 1: %v", err)
	}
	p1, err := os.ReadFile(filepath.Join(f.repoDir, "dists", "stable", "main", "binary-amd64", "Packages"))
	if err != nil {
		t.Fatalf("read1: %v", err)
	}
	if err := f.regenFn()(context.Background()); err != nil {
		t.Fatalf("regen 2: %v", err)
	}
	p2, err := os.ReadFile(filepath.Join(f.repoDir, "dists", "stable", "main", "binary-amd64", "Packages"))
	if err != nil {
		t.Fatalf("read2: %v", err)
	}
	if !bytes.Equal(p1, p2) {
		t.Errorf("Packages not deterministic across runs")
	}
}

func TestDEBRegenFailureRestoresCurrent(t *testing.T) {
	f := newDEBRegenFixture(t, []metadata.AptSuite{
		{Suite: "stable", Component: "main", Architecture: "amd64"},
	})
	f.seedPackage("stable|main|amd64", "mypkg", "1.0-1", "amd64")

	// First regen succeeds, producing a known-good dists/stable/.
	if err := f.regenFn()(context.Background()); err != nil {
		t.Fatalf("regen 1: %v", err)
	}
	releaseOrig, err := os.ReadFile(filepath.Join(f.repoDir, "dists", "stable", "Release"))
	if err != nil {
		t.Fatalf("read release: %v", err)
	}

	// Drop the signing key → second regen MUST fail at LookupPrivate, and the
	// on-disk dists/stable/ must remain intact (no partial swap).
	if _, err := f.db.Writer.Exec(`DELETE FROM signing_keys WHERE repo_id=?`, f.repoID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// Bust the pubkey cache isn't relevant — regen calls LookupPrivate.
	err = f.regenFn()(context.Background())
	if err == nil {
		t.Fatalf("expected regen error")
	}
	// Previous Release unchanged.
	releaseNow, err := os.ReadFile(filepath.Join(f.repoDir, "dists", "stable", "Release"))
	if err != nil {
		t.Fatalf("read release after fail: %v", err)
	}
	if !bytes.Equal(releaseOrig, releaseNow) {
		t.Errorf("Release mutated despite regen failure")
	}
	// metadata_state=dirty with last_regen_error set.
	state, lastErr, sErr := f.repos.GetMetadataState(context.Background(), f.repoID)
	if sErr != nil {
		t.Fatalf("state: %v", sErr)
	}
	if state != metadata.MetadataStateDirty {
		t.Errorf("state=%q want dirty", state)
	}
	if lastErr == "" {
		t.Errorf("last_regen_error empty")
	}
}

func TestDEBRegenPackagesGzMatchesPackages(t *testing.T) {
	f := newDEBRegenFixture(t, []metadata.AptSuite{
		{Suite: "stable", Component: "main", Architecture: "amd64"},
	})
	f.seedPackage("stable|main|amd64", "mypkg", "1.0-1", "amd64")

	if err := f.regenFn()(context.Background()); err != nil {
		t.Fatalf("regen: %v", err)
	}
	base := filepath.Join(f.repoDir, "dists", "stable", "main", "binary-amd64")
	plain, err := os.ReadFile(filepath.Join(base, "Packages"))
	if err != nil {
		t.Fatalf("read Packages: %v", err)
	}
	gzBytes, err := os.ReadFile(filepath.Join(base, "Packages.gz"))
	if err != nil {
		t.Fatalf("read Packages.gz: %v", err)
	}
	gr, err := gzip.NewReader(bytes.NewReader(gzBytes))
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	defer func() { _ = gr.Close() }()
	restored, _ := io.ReadAll(gr)
	if !bytes.Equal(restored, plain) {
		t.Errorf("Packages.gz content != Packages (len %d vs %d)", len(restored), len(plain))
	}
}

// Guard — unused warnings.
var _ = errors.New
