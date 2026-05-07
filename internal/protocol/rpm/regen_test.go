package rpm_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pgpopen "github.com/ProtonMail/go-crypto/openpgp"

	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/protocol/rpm"
	"github.com/dxc-internal/omnirepo/internal/storage"
)

// regenFixture seeds a project + repo + signing key + N rpm_packages rows
// and returns everything the regen factory needs.
type regenFixture struct {
	t           *testing.T
	db          *metadata.DB
	repos       *metadata.ReposRepo
	rpmPackages *metadata.RPMPackagesRepo
	signKeys    *metadata.SigningKeysRepo
	repoRoot    string
	repoID      int64
	repoDir     string
	projects    *metadata.ProjectsRepo
	pubArmored  string
}

func newRegenFixture(t *testing.T) *regenFixture {
	t.Helper()
	signKeys, db := newSigningKeysRepoForRPM(t)
	rid, pub := seedRepoWithSigningKey(t, db, signKeys)

	root := t.TempDir()
	projects := metadata.NewProjectsRepo(db)
	repos := metadata.NewReposRepo(db)
	pkgs := metadata.NewRPMPackagesRepo(db)
	repoDir := filepath.Join(root, "proj", "rpm", "myrepo")
	if err := os.MkdirAll(repoDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	return &regenFixture{
		t:           t,
		db:          db,
		repos:       repos,
		rpmPackages: pkgs,
		signKeys:    signKeys,
		repoRoot:    root,
		repoID:      rid,
		repoDir:     repoDir,
		projects:    projects,
		pubArmored:  pub,
	}
}

// newSigningKeysRepoForRPM mirrors newSigningKeysRepo from public_key_test.go
// but is duplicated here to avoid cross-file shared state in this _test pkg.
func newSigningKeysRepoForRPM(t *testing.T) (*metadata.SigningKeysRepo, *metadata.DB) {
	return newSigningKeysRepo(t)
}

func (f *regenFixture) seedPackage(name, ver, arch string) {
	f.t.Helper()
	if err := f.db.WriteTx(context.Background(), func(tx *sql.Tx) error {
		_, err := f.rpmPackages.Insert(context.Background(), tx, &metadata.RPMPackage{
			RepoID: f.repoID, Name: name, Version: ver, Release: "1.el7", Arch: arch,
			Summary: name + " summary", Description: "desc", License: "MIT",
			URL: "https://example.com/" + name, SourceRPM: name + "-" + ver + "-1.el7.src.rpm",
			SizeBytes: 1234, Digest: "sha256:" + strings.Repeat("a", 64), Filename: name + "-" + ver + "-1.el7." + arch + ".rpm",
		})
		return err
	}); err != nil {
		f.t.Fatalf("insert pkg: %v", err)
	}
}

func (f *regenFixture) regenFn() func(context.Context) error {
	return rpm.RegenFor(rpm.RegenDeps{
		DB:          f.db,
		Repos:       f.repos,
		Projects:    f.projects,
		RPMPackages: f.rpmPackages,
		SigningKeys: f.signKeys,
		Locks:       storage.NewLocks(),
		RepoRoot:    f.repoRoot,
		RepoID:      f.repoID,
	})
}

func TestRPMRegenWritesAllFiles(t *testing.T) {
	f := newRegenFixture(t)
	f.seedPackage("alpha", "1.0", "x86_64")
	f.seedPackage("beta", "2.0", "noarch")

	if err := f.regenFn()(context.Background()); err != nil {
		t.Fatalf("regen: %v", err)
	}
	repodata := filepath.Join(f.repoDir, "repodata")
	for _, want := range []string{"repomd.xml", "repomd.xml.asc"} {
		if _, err := os.Stat(filepath.Join(repodata, want)); err != nil {
			t.Errorf("missing %s: %v", want, err)
		}
	}
	matches, _ := filepath.Glob(filepath.Join(repodata, "primary-*.xml.gz"))
	if len(matches) != 1 {
		t.Errorf("primary-*.xml.gz count=%d, want 1", len(matches))
	}
	if matches, _ := filepath.Glob(filepath.Join(repodata, "filelists-*.xml.gz")); len(matches) != 1 {
		t.Errorf("filelists count=%d", len(matches))
	}
	if matches, _ := filepath.Glob(filepath.Join(repodata, "other-*.xml.gz")); len(matches) != 1 {
		t.Errorf("other count=%d", len(matches))
	}
	state, _, err := f.repos.GetMetadataState(context.Background(), f.repoID)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if state != metadata.MetadataStateClean {
		t.Errorf("state=%q want clean", state)
	}
}

func TestRPMRegenRepomdReferencesHashNames(t *testing.T) {
	f := newRegenFixture(t)
	f.seedPackage("alpha", "1.0", "x86_64")

	if err := f.regenFn()(context.Background()); err != nil {
		t.Fatalf("regen: %v", err)
	}
	repodata := filepath.Join(f.repoDir, "repodata")
	body, err := os.ReadFile(filepath.Join(repodata, "repomd.xml"))
	if err != nil {
		t.Fatalf("read repomd: %v", err)
	}
	var root rpm.RepomdRoot
	if err := xml.Unmarshal(body, &root); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, d := range root.Data {
		// Verify the referenced file exists on disk.
		abs := filepath.Join(f.repoDir, d.Location.Href)
		if _, err := os.Stat(abs); err != nil {
			t.Errorf("repomd %s href=%q not on disk: %v", d.Type, d.Location.Href, err)
		}
		if !strings.Contains(d.Location.Href, "-"+d.Checksum.Value+".xml.gz") {
			t.Errorf("href %q does not embed checksum %q", d.Location.Href, d.Checksum.Value)
		}
	}
}

func TestRPMRegenSignatureVerifies(t *testing.T) {
	f := newRegenFixture(t)
	f.seedPackage("alpha", "1.0", "x86_64")

	if err := f.regenFn()(context.Background()); err != nil {
		t.Fatalf("regen: %v", err)
	}
	repodata := filepath.Join(f.repoDir, "repodata")
	repomd, err := os.ReadFile(filepath.Join(repodata, "repomd.xml"))
	if err != nil {
		t.Fatalf("read repomd: %v", err)
	}
	sig, err := os.ReadFile(filepath.Join(repodata, "repomd.xml.asc"))
	if err != nil {
		t.Fatalf("read sig: %v", err)
	}
	keyring, err := pgpopen.ReadArmoredKeyRing(strings.NewReader(f.pubArmored))
	if err != nil {
		t.Fatalf("read keyring: %v", err)
	}
	signer, err := pgpopen.CheckArmoredDetachedSignature(keyring, bytes.NewReader(repomd), bytes.NewReader(sig), nil)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if signer == nil {
		t.Errorf("nil signer")
	}
}

func TestRPMRegenStaleSweep(t *testing.T) {
	f := newRegenFixture(t)
	f.seedPackage("alpha", "1.0", "x86_64")

	if err := f.regenFn()(context.Background()); err != nil {
		t.Fatalf("regen 1: %v", err)
	}
	// Drop a stale hash-named file.
	repodata := filepath.Join(f.repoDir, "repodata")
	stale := filepath.Join(repodata, "primary-deadbeef.xml.gz")
	if err := os.WriteFile(stale, []byte("stale"), 0o640); err != nil {
		t.Fatalf("write stale: %v", err)
	}
	if err := f.regenFn()(context.Background()); err != nil {
		t.Fatalf("regen 2: %v", err)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stale file should be swept, got err=%v", err)
	}
}

func TestRPMRegenFailureMarksDirty(t *testing.T) {
	f := newRegenFixture(t)
	f.seedPackage("alpha", "1.0", "x86_64")

	// Drop the signing key to force LookupPrivate failure.
	if _, err := f.db.Writer.Exec(`DELETE FROM signing_keys WHERE repo_id=?`, f.repoID); err != nil {
		t.Fatalf("delete key: %v", err)
	}
	err := f.regenFn()(context.Background())
	if err == nil {
		t.Fatalf("expected regen error after key deletion")
	}
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

func TestRPMRegenPackageOrderDeterministic(t *testing.T) {
	f := newRegenFixture(t)
	f.seedPackage("alpha", "1.0", "x86_64")
	f.seedPackage("beta", "2.0", "noarch")

	if err := f.regenFn()(context.Background()); err != nil {
		t.Fatalf("regen 1: %v", err)
	}
	repodata := filepath.Join(f.repoDir, "repodata")
	first, err := snapshotPrimary(repodata)
	if err != nil {
		t.Fatalf("snap 1: %v", err)
	}

	// Run regen again. Same package set should produce the same primary
	// hash → same primary-<sha>.xml.gz filename.
	if err := f.regenFn()(context.Background()); err != nil {
		t.Fatalf("regen 2: %v", err)
	}
	second, err := snapshotPrimary(repodata)
	if err != nil {
		t.Fatalf("snap 2: %v", err)
	}
	if first != second {
		t.Errorf("non-deterministic primary filename: %q vs %q", first, second)
	}
}

// snapshotPrimary returns the basename of the single primary-*.xml.gz file.
func snapshotPrimary(repodata string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(repodata, "primary-*.xml.gz"))
	if err != nil {
		return "", err
	}
	if len(matches) != 1 {
		return "", errors.New("primary count != 1")
	}
	return filepath.Base(matches[0]), nil
}
