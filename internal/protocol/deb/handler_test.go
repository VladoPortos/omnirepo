package deb_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blakesmith/ar"
	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/auth"
	omrcrypto "github.com/dxc-internal/omnirepo/internal/crypto"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
	"github.com/dxc-internal/omnirepo/internal/protocol/deb"
	"github.com/dxc-internal/omnirepo/internal/protocol/regen"
	"github.com/dxc-internal/omnirepo/internal/storage"
)

type debFixture struct {
	t              *testing.T
	db             *metadata.DB
	users          *metadata.UsersRepo
	apiKeys        *metadata.APIKeysRepo
	repos          *metadata.ReposRepo
	projects       *metadata.ProjectsRepo
	debPackages    *metadata.DEBPackagesRepo
	aptSuites      *metadata.AptSuitesRepo
	signingKeys    *metadata.SigningKeysRepo
	scans          *metadata.ScansRepo
	srv            *httptest.Server
	dataRoot       string
	repoRoot       string
	login          string
	password       string
	userID         int64
	publicKeyCache *deb.PublicKeyCache
	kickCounts     sync.Map
	registry       *regen.Registry
}

func (f *debFixture) kickCount(repoID int64) int64 {
	v, ok := f.kickCounts.Load(repoID)
	if !ok {
		return 0
	}
	return v.(*atomic.Int64).Load()
}

func newDEBFixture(t *testing.T) *debFixture {
	t.Helper()
	db := sqlitetest.New(t)
	users := metadata.NewUsersRepo(db)
	apiKeys := metadata.NewAPIKeysRepo(db)
	repos := metadata.NewReposRepo(db)
	projects := metadata.NewProjectsRepo(db)
	debPackages := metadata.NewDEBPackagesRepo(db)
	aptSuites := metadata.NewAptSuitesRepo(db)
	scans := metadata.NewScansRepo(db)
	sessions := metadata.NewSessionsRepo(db)

	key, err := omrcrypto.GenerateKey()
	if err != nil {
		t.Fatalf("aead key: %v", err)
	}
	aead, err := omrcrypto.New(key)
	if err != nil {
		t.Fatalf("aead: %v", err)
	}
	signingKeys := metadata.NewSigningKeysRepo(db, aead)

	login := "deb-user"
	password := "deb-test-password-12345"
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("hash pw: %v", err)
	}
	uid, err := users.Create(context.Background(), login, "u@example.com", hash, false, false)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	dataRoot := t.TempDir()
	repoRoot := filepath.Join(dataRoot, "repos")
	trashRoot := filepath.Join(dataRoot, "trash")
	for _, p := range []string{repoRoot, trashRoot, filepath.Join(dataRoot, "logs")} {
		if err := os.MkdirAll(p, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
	}
	auditLogger, err := audit.New(db, filepath.Join(dataRoot, "logs", "audit.log"), 10, 1)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	f := &debFixture{
		t:           t,
		db:          db,
		users:       users,
		apiKeys:     apiKeys,
		repos:       repos,
		projects:    projects,
		debPackages: debPackages,
		aptSuites:   aptSuites,
		signingKeys: signingKeys,
		scans:       scans,
		dataRoot:    dataRoot,
		repoRoot:    repoRoot,
		login:       login,
		password:    password,
		userID:      uid,
	}
	f.publicKeyCache = deb.NewPublicKeyCache(signingKeys)

	factory := func(repoID int64) regen.RegenFn {
		ctr := &atomic.Int64{}
		f.kickCounts.Store(repoID, ctr)
		return func(ctx context.Context) error { ctr.Add(1); return nil }
	}
	f.registry = regen.NewRegistry(10*time.Millisecond, 100*time.Millisecond, factory)
	t.Cleanup(func() { _ = f.registry.ShutdownAll(context.Background()) })

	h := deb.New(deb.Deps{
		DB:             db,
		Users:          users,
		APIKeys:        apiKeys,
		Sessions:       sessions,
		Repos:          repos,
		Projects:       projects,
		Members:        metadata.NewMembersRepo(db),
		DEBPackages:    debPackages,
		AptSuites:      aptSuites,
		SigningKeys:    signingKeys,
		Scans:          scans,
		Coalescer:      f.registry,
		PublicKeyCache: f.publicKeyCache,
		Path:           storage.NewPathStore(repoRoot),
		Trash:          storage.NewTrash(trashRoot),
		Audit:          auditLogger,
		MaxPutBytes:    1 << 20,
		RepoRoot:       repoRoot,
	})
	r := chi.NewRouter()
	h.Mount(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	f.srv = srv
	return f
}

// seedDEBRepo creates a project + deb repo + member + signing-key row + 3
// default apt_suites rows.
func (f *debFixture) seedDEBRepo(projName, repoName string, publicRead bool) (projectID, repoID int64) {
	pid, err := f.projects.Create(context.Background(), projName, "test")
	if err != nil {
		f.t.Fatalf("seed project: %v", err)
	}
	if _, err := f.db.Writer.Exec(`INSERT INTO project_members(project_id, user_id) VALUES (?, ?)`, pid, f.userID); err != nil {
		f.t.Fatalf("seed member: %v", err)
	}
	autoScan := false
	rid, err := f.repos.Create(context.Background(), pid, "deb", repoName, "", &autoScan, nil, &publicRead)
	if err != nil {
		f.t.Fatalf("seed repo: %v", err)
	}
	priv, pub, fp, err := omrcrypto.GenerateRepoKey(projName+"-"+repoName+"-omnirepo", 2048)
	if err != nil {
		f.t.Fatalf("gen key: %v", err)
	}
	if err := f.db.WriteTx(context.Background(), func(tx *sql.Tx) error {
		if _, err := f.signingKeys.Insert(context.Background(), tx, rid, pub, priv, fp); err != nil {
			return err
		}
		return f.aptSuites.InsertBatch(context.Background(), tx, rid, []metadata.AptSuite{
			{RepoID: rid, Suite: "stable", Component: "main", Architecture: "amd64"},
			{RepoID: rid, Suite: "stable", Component: "main", Architecture: "arm64"},
			{RepoID: rid, Suite: "stable", Component: "main", Architecture: "all"},
		})
	}); err != nil {
		f.t.Fatalf("seed signing key + suites: %v", err)
	}
	return pid, rid
}

func (f *debFixture) basicAuth() string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(f.login+":"+f.password))
}

func (f *debFixture) do(t *testing.T, method, urlPath string, body []byte, auth bool) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, _ := http.NewRequest(method, f.srv.URL+urlPath, reader)
	if auth {
		req.Header.Set("Authorization", f.basicAuth())
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, urlPath, err)
	}
	return resp
}

// buildTestDeb synthesizes a .deb with the given Architecture field. Returns
// (debBytes, package, version).
func buildTestDeb(t *testing.T, pkgName, version, arch string) []byte {
	t.Helper()
	ctl := "Package: " + pkgName + "\n" +
		"Version: " + version + "\n" +
		"Architecture: " + arch + "\n" +
		"Maintainer: Test <t@e.com>\n" +
		"Description: test package\n" +
		" multi-line description\n"
	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	if err := tw.WriteHeader(&tar.Header{Name: "./control", Mode: 0o644, Size: int64(len(ctl))}); err != nil {
		t.Fatalf("tar hdr: %v", err)
	}
	if _, err := tw.Write([]byte(ctl)); err != nil {
		t.Fatalf("tar w: %v", err)
	}
	_ = tw.Close()

	var gzBuf bytes.Buffer
	gz := gzip.NewWriter(&gzBuf)
	_, _ = gz.Write(tarBuf.Bytes())
	_ = gz.Close()

	var arBuf bytes.Buffer
	aw := ar.NewWriter(&arBuf)
	if err := aw.WriteGlobalHeader(); err != nil {
		t.Fatalf("ar global: %v", err)
	}
	writeMember := func(name string, body []byte) {
		hdr := &ar.Header{Name: name, Size: int64(len(body)), Mode: 0o644}
		_ = aw.WriteHeader(hdr)
		_, _ = aw.Write(body)
	}
	writeMember("debian-binary", []byte("2.0\n"))
	writeMember("control.tar.gz", gzBuf.Bytes())
	writeMember("data.tar", []byte{})
	return arBuf.Bytes()
}

func TestDEBPutRoundTrip(t *testing.T) {
	f := newDEBFixture(t)
	_, repoID := f.seedDEBRepo("proj", "myrepo", false)

	body := buildTestDeb(t, "mypkg", "1.0-1", "amd64")
	resp := f.do(t, http.MethodPut, "/proj/deb/myrepo/pool/m/mypkg/mypkg_1.0-1_amd64.deb?suite=stable&component=main", body, true)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		out, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, out)
	}

	pkgs, err := f.debPackages.ListByRepo(context.Background(), repoID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(pkgs) != 1 {
		t.Fatalf("got %d rows", len(pkgs))
	}
	if pkgs[0].Package != "mypkg" || pkgs[0].Version != "1.0-1" || pkgs[0].Architecture != "amd64" {
		t.Errorf("row mismatch: %+v", pkgs[0])
	}
	if pkgs[0].Filename != "mypkg_1.0-1_amd64.deb" {
		t.Errorf("filename=%q", pkgs[0].Filename)
	}

	// deb_fts row present.
	var n int
	_ = f.db.Reader.QueryRow(`SELECT COUNT(*) FROM deb_fts WHERE repo_id=?`, repoID).Scan(&n)
	if n != 1 {
		t.Errorf("fts rows=%d", n)
	}

	state, _, err := f.repos.GetMetadataState(context.Background(), repoID)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if state != metadata.MetadataStateDirty {
		t.Errorf("state=%q want dirty", state)
	}
	// Coalescer kick observed.
	deadline := time.Now().Add(500 * time.Millisecond)
	for f.kickCount(repoID) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if f.kickCount(repoID) == 0 {
		t.Errorf("coalescer not kicked")
	}
}

func TestDEBPutUnknownSuite(t *testing.T) {
	f := newDEBFixture(t)
	_, _ = f.seedDEBRepo("proj", "myrepo", false)

	body := buildTestDeb(t, "mypkg", "1.0-1", "amd64")
	// suite=unstable was never declared.
	resp := f.do(t, http.MethodPut, "/proj/deb/myrepo/pool/m/mypkg/mypkg_1.0-1_amd64.deb?suite=unstable&component=main", body, true)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	out, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(out, []byte("unknown_suite_or_component")) {
		t.Errorf("body=%s", out)
	}
}

// TestDEBPutArchDerivedFromControl: if client picks ?suite=stable but the
// control says Architecture: arm64, the deb_packages row stores arm64 (arch
// comes from control, not query — D-24).
func TestDEBPutArchDerivedFromControl(t *testing.T) {
	f := newDEBFixture(t)
	_, repoID := f.seedDEBRepo("proj", "myrepo", false)

	body := buildTestDeb(t, "mypkg", "1.0-1", "arm64")
	resp := f.do(t, http.MethodPut, "/proj/deb/myrepo/pool/m/mypkg/mypkg_1.0-1_arm64.deb?suite=stable&component=main", body, true)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		out, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, out)
	}
	pkgs, _ := f.debPackages.ListByRepo(context.Background(), repoID)
	if len(pkgs) != 1 || pkgs[0].Architecture != "arm64" {
		t.Errorf("arch mismatch: %+v", pkgs)
	}
}

func TestDEBPutRejectsBadDeb(t *testing.T) {
	f := newDEBFixture(t)
	_, _ = f.seedDEBRepo("proj", "myrepo", false)

	resp := f.do(t, http.MethodPut, "/proj/deb/myrepo/pool/m/mypkg/mypkg_1.0-1_amd64.deb", []byte("not an ar"), true)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	out, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(out, []byte("invalid_package")) {
		t.Errorf("body=%s", out)
	}
}

func TestDEBDeleteRemovesFTSRow(t *testing.T) {
	f := newDEBFixture(t)
	_, repoID := f.seedDEBRepo("proj", "myrepo", false)

	body := buildTestDeb(t, "mypkg", "1.0-1", "amd64")
	urlPath := "/proj/deb/myrepo/pool/m/mypkg/mypkg_1.0-1_amd64.deb?suite=stable&component=main"
	resp := f.do(t, http.MethodPut, urlPath, body, true)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload: %d", resp.StatusCode)
	}

	// Wait for first kick.
	deadline := time.Now().Add(500 * time.Millisecond)
	for f.kickCount(repoID) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	first := f.kickCount(repoID)

	resp = f.do(t, http.MethodDelete, "/proj/deb/myrepo/pool/m/mypkg/mypkg_1.0-1_amd64.deb", nil, true)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status=%d", resp.StatusCode)
	}

	pkgs, _ := f.debPackages.ListByRepo(context.Background(), repoID)
	if len(pkgs) != 0 {
		t.Errorf("rows after delete: %d", len(pkgs))
	}
	var n int
	_ = f.db.Reader.QueryRow(`SELECT COUNT(*) FROM deb_fts WHERE repo_id=?`, repoID).Scan(&n)
	if n != 0 {
		t.Errorf("fts rows after delete: %d", n)
	}
	// Wait for second kick.
	deadline = time.Now().Add(500 * time.Millisecond)
	for f.kickCount(repoID) <= first && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if f.kickCount(repoID) <= first {
		t.Errorf("coalescer not re-kicked after delete (first=%d now=%d)", first, f.kickCount(repoID))
	}
}

func TestDEBPatchSuitesAddsRow(t *testing.T) {
	f := newDEBFixture(t)
	_, repoID := f.seedDEBRepo("proj", "myrepo", false)

	body, _ := json.Marshal(map[string]any{
		"add": []map[string]string{
			{"suite": "unstable", "component": "main", "architecture": "i386"},
		},
	})
	resp := f.do(t, http.MethodPatch, "/proj/deb/myrepo/suites", body, true)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		out, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, out)
	}
	rows, _ := f.aptSuites.ListByRepo(context.Background(), repoID)
	var found bool
	for _, r := range rows {
		if r.Suite == "unstable" && r.Component == "main" && r.Architecture == "i386" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("new suite row not found; got %+v", rows)
	}
}

func TestDEBPublicKeyEndpoint(t *testing.T) {
	f := newDEBFixture(t)
	_, _ = f.seedDEBRepo("proj", "myrepo", true)

	resp := f.do(t, http.MethodGet, "/proj/deb/myrepo/public-key.asc", nil, false)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/pgp-keys" {
		t.Errorf("ct=%q", got)
	}
	out, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(out), "BEGIN PGP PUBLIC KEY BLOCK") {
		t.Errorf("not armored: %s", out)
	}
}

func TestDEBGetServePackage(t *testing.T) {
	f := newDEBFixture(t)
	_, _ = f.seedDEBRepo("proj", "myrepo", true)

	body := buildTestDeb(t, "mypkg", "1.0-1", "amd64")
	urlPath := "/proj/deb/myrepo/pool/m/mypkg/mypkg_1.0-1_amd64.deb?suite=stable&component=main"
	resp := f.do(t, http.MethodPut, urlPath, body, true)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload: %d", resp.StatusCode)
	}

	resp = f.do(t, http.MethodGet, "/proj/deb/myrepo/pool/m/mypkg/mypkg_1.0-1_amd64.deb", nil, false)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	got, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(got, body) {
		t.Errorf("roundtrip mismatch %d vs %d", len(got), len(body))
	}
}

// TestDEBGetServePackagePercentEncoded covers F-T7: apt percent-encodes `+`
// (and other reserved chars) when deriving download URLs from Filename.
// The handler must URL-decode the wildcard so the on-disk path matches.
func TestDEBGetServePackagePercentEncoded(t *testing.T) {
	f := newDEBFixture(t)
	_, _ = f.seedDEBRepo("proj", "myrepo", true)

	// Upload a package whose filename contains `+` and `~`. The client puts
	// the literal characters in the URL path.
	body := buildTestDeb(t, "zstd", "1.4.8+dfsg~rc1-3build1", "amd64")
	filename := "zstd_1.4.8+dfsg~rc1-3build1_amd64.deb"
	putPath := "/proj/deb/myrepo/pool/z/zstd/" + filename + "?suite=stable&component=main"
	resp := f.do(t, http.MethodPut, putPath, body, true)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload: %d", resp.StatusCode)
	}

	// apt percent-encodes `+` as %2B and `~` as %7E in the GET.
	encodedPath := "/proj/deb/myrepo/pool/z/zstd/zstd_1.4.8%2Bdfsg%7Erc1-3build1_amd64.deb"
	resp = f.do(t, http.MethodGet, encodedPath, nil, false)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("encoded GET status=%d", resp.StatusCode)
	}
	got, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(got, body) {
		t.Errorf("roundtrip mismatch %d vs %d", len(got), len(body))
	}
}

func TestDEBUploadForbiddenForOutsider(t *testing.T) {
	f := newDEBFixture(t)
	_, _ = f.seedDEBRepo("proj", "myrepo", false)
	// Drop membership.
	if _, err := f.db.Writer.Exec(`DELETE FROM project_members WHERE user_id=?`, f.userID); err != nil {
		t.Fatalf("drop member: %v", err)
	}
	body := buildTestDeb(t, "mypkg", "1.0-1", "amd64")
	resp := f.do(t, http.MethodPut, "/proj/deb/myrepo/pool/m/mypkg/mypkg_1.0-1_amd64.deb?suite=stable&component=main", body, true)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}
