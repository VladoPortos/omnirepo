package pypi_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/protocol/pypi"
	"github.com/vladoportos/omnirepo/internal/storage"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPyPISyncHashlessFilesSurviveDrift(t *testing.T) {
	f := newPyPIDriftFixture(t)
	const fn = "acme-1.0-py3-none-any.whl"
	f.upstreamFiles.Store(map[string]pypiFileBytes{fn: {bytes: []byte("unhashed-wheel")}})
	pypiEnableDriftPurge(t, f.db, f.repoID)
	pypiRunSync(t, f)
	pypiRunSync(t, f)
	row, err := metadata.NewPyPIFilesRepo(f.db).FindByFilename(context.Background(), f.repoID, fn)
	if err != nil || row == nil {
		t.Fatalf("hashless upstream file purged: %v", err)
	}
}

func TestPyPISyncChangedKnownDigestReplacesBytes(t *testing.T) {
	f := newPyPIDriftFixture(t)
	const fn = "acme-1.0-py3-none-any.whl"
	f.setUpstream(t, fn)
	pypiRunSync(t, f)
	b := []byte("replacement-wheel")
	sum := sha256.Sum256(b)
	hexDigest := hex.EncodeToString(sum[:])
	f.upstreamFiles.Store(map[string]pypiFileBytes{fn: {bytes: b, digest: hexDigest}})
	pypiEnableDriftPurge(t, f.db, f.repoID)
	pypiRunSync(t, f)
	row, err := metadata.NewPyPIFilesRepo(f.db).FindByFilename(context.Background(), f.repoID, fn)
	if err != nil || row == nil || row.Digest != "sha256:"+hexDigest {
		t.Fatalf("replacement metadata missing: %+v %v", row, err)
	}
	got, err := os.ReadFile(filepath.Join(f.dataRoot, "repos", f.projName, "pypi", f.repoName, "packages", fn))
	if err != nil || string(got) != string(b) {
		t.Fatalf("replacement bytes = %q, %v", got, err)
	}
}

func TestPyPISyncRefreshesYanksWithoutRedownload(t *testing.T) {
	f := newPyPIDriftFixture(t)
	const fn = "acme-1.0-py3-none-any.whl"
	f.setUpstream(t, fn)
	regen := pypi.RegenFor(pypi.RegenDeps{DB: f.db, Repos: metadata.NewReposRepo(f.db), Projects: metadata.NewProjectsRepo(f.db), PyPIFiles: metadata.NewPyPIFilesRepo(f.db), Locks: storage.NewLocks(), RepoRoot: filepath.Join(f.dataRoot, "repos"), RepoID: f.repoID})
	for _, yank := range []any{true, "broken & unsafe", false} {
		files := f.upstreamFiles.Load().(map[string]pypiFileBytes)
		file := files[fn]
		file.yanked = yank
		f.upstreamFiles.Store(map[string]pypiFileBytes{fn: file})
		pypiRunSync(t, f)
		if err := regen(context.Background()); err != nil {
			t.Fatal(err)
		}
		body, err := os.ReadFile(filepath.Join(f.dataRoot, "repos", f.projName, "pypi", f.repoName, "simple", "acme", "index.html"))
		if err != nil {
			t.Fatal(err)
		}
		want := yank != false
		if strings.Contains(string(body), "data-yanked=") != want {
			t.Fatalf("yank %v not rendered: %s", yank, body)
		}
		if yank == "broken & unsafe" && !strings.Contains(string(body), "broken &amp; unsafe") {
			t.Fatalf("missing yank reason: %s", body)
		}
	}
	if got := f.downloads.Load(); got != 1 {
		t.Fatalf("metadata refresh fetched wheel %d times", got)
	}
}
