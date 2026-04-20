package rpm_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
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

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/auth"
	omrcrypto "github.com/dxc-internal/omnirepo/internal/crypto"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
	"github.com/dxc-internal/omnirepo/internal/protocol/regen"
	"github.com/dxc-internal/omnirepo/internal/protocol/rpm"
	"github.com/dxc-internal/omnirepo/internal/storage"
)

type rpmFixture struct {
	t              *testing.T
	db             *metadata.DB
	users          *metadata.UsersRepo
	apiKeys        *metadata.APIKeysRepo
	repos          *metadata.ReposRepo
	projects       *metadata.ProjectsRepo
	rpmPackages    *metadata.RPMPackagesRepo
	signingKeys    *metadata.SigningKeysRepo
	scans          *metadata.ScansRepo
	auditLog       audit.Logger
	srv            *httptest.Server
	dataRoot       string
	repoRoot       string
	login          string
	password       string
	userID         int64
	publicKeyCache *rpm.PublicKeyCache

	kickCounts sync.Map
	registry   *regen.Registry
}

func (f *rpmFixture) kickCount(repoID int64) int64 {
	v, ok := f.kickCounts.Load(repoID)
	if !ok {
		return 0
	}
	return v.(*atomic.Int64).Load()
}

func newRPMFixture(t *testing.T) *rpmFixture {
	t.Helper()
	db := sqlitetest.New(t)
	users := metadata.NewUsersRepo(db)
	apiKeys := metadata.NewAPIKeysRepo(db)
	repos := metadata.NewReposRepo(db)
	projects := metadata.NewProjectsRepo(db)
	rpmPackages := metadata.NewRPMPackagesRepo(db)
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

	login := "rpm-user"
	password := "rpm-test-password-1234567"
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

	f := &rpmFixture{
		t:           t,
		db:          db,
		users:       users,
		apiKeys:     apiKeys,
		repos:       repos,
		projects:    projects,
		rpmPackages: rpmPackages,
		signingKeys: signingKeys,
		scans:       scans,
		auditLog:    auditLogger,
		dataRoot:    dataRoot,
		repoRoot:    repoRoot,
		login:       login,
		password:    password,
		userID:      uid,
	}
	f.publicKeyCache = rpm.NewPublicKeyCache(signingKeys)

	factory := func(repoID int64) regen.RegenFn {
		ctr := &atomic.Int64{}
		f.kickCounts.Store(repoID, ctr)
		return func(ctx context.Context) error {
			ctr.Add(1)
			return nil
		}
	}
	f.registry = regen.NewRegistry(10*time.Millisecond, 100*time.Millisecond, factory)
	t.Cleanup(func() { _ = f.registry.ShutdownAll(context.Background()) })

	h := rpm.New(rpm.Deps{
		DB:             db,
		Users:          users,
		APIKeys:        apiKeys,
		Sessions:       sessions,
		Repos:          repos,
		Projects:       projects,
		Members:        metadata.NewMembersRepo(db),
		RPMPackages:    rpmPackages,
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

// seedRepo creates a project + RPM repo + member + signing-key row.
func (f *rpmFixture) seedRepo(projName, repoName string, publicRead bool) (projectID, repoID int64) {
	pid, err := f.projects.Create(context.Background(), projName, "test")
	if err != nil {
		f.t.Fatalf("seed project: %v", err)
	}
	if _, err := f.db.Writer.Exec(`INSERT INTO project_members(project_id, user_id) VALUES (?, ?)`, pid, f.userID); err != nil {
		f.t.Fatalf("seed member: %v", err)
	}
	autoScan := false
	rid, err := f.repos.Create(context.Background(), pid, "rpm", repoName, "", &autoScan, nil, &publicRead)
	if err != nil {
		f.t.Fatalf("seed repo: %v", err)
	}
	// Eager signing key (mirrors what the production repo-create hook does).
	priv, pub, fp, err := omrcrypto.GenerateRepoKey(projName+"-"+repoName, 2048)
	if err != nil {
		f.t.Fatalf("gen key: %v", err)
	}
	if err := f.db.WriteTx(context.Background(), func(tx *sql.Tx) error {
		_, err := f.signingKeys.Insert(context.Background(), tx, rid, pub, priv, fp)
		return err
	}); err != nil {
		f.t.Fatalf("insert signing key: %v", err)
	}
	return pid, rid
}

func (f *rpmFixture) basicAuth() string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(f.login+":"+f.password))
}

func (f *rpmFixture) put(t *testing.T, urlPath string, body []byte, withAuth bool) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, f.srv.URL+urlPath, bytes.NewReader(body))
	if withAuth {
		req.Header.Set("Authorization", f.basicAuth())
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", urlPath, err)
	}
	return resp
}

func (f *rpmFixture) doMethod(t *testing.T, method, urlPath string, withAuth bool) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, f.srv.URL+urlPath, nil)
	if withAuth {
		req.Header.Set("Authorization", f.basicAuth())
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, urlPath, err)
	}
	return resp
}

// readFixtureRPM loads testdata/sample.rpm bytes.
func readFixtureRPM(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/sample.rpm")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return b
}

func TestRPMPutRoundTrip(t *testing.T) {
	f := newRPMFixture(t)
	_, repoID := f.seedRepo("proj", "myrepo", false)

	body := readFixtureRPM(t)
	resp := f.put(t, "/proj/rpm/myrepo/packages/sample.rpm", body, true)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		out, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, out)
	}

	// rpm_packages row.
	pkgs, err := f.rpmPackages.ListByRepo(context.Background(), repoID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(pkgs) != 1 {
		t.Fatalf("got %d rows, want 1", len(pkgs))
	}
	if pkgs[0].Filename != "sample.rpm" {
		t.Errorf("filename=%q", pkgs[0].Filename)
	}
	if pkgs[0].SizeBytes != int64(len(body)) {
		t.Errorf("size=%d want %d", pkgs[0].SizeBytes, len(body))
	}

	// rpm_fts row.
	var n int
	if err := f.db.Reader.QueryRow(`SELECT COUNT(*) FROM rpm_fts WHERE repo_id=?`, repoID).Scan(&n); err != nil {
		t.Fatalf("fts count: %v", err)
	}
	if n != 1 {
		t.Errorf("fts count=%d", n)
	}
	// metadata_state=dirty.
	state, _, err := f.repos.GetMetadataState(context.Background(), repoID)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if state != metadata.MetadataStateDirty {
		t.Errorf("state=%q want dirty", state)
	}
	// Coalescer kicked.
	wait := time.NewTimer(500 * time.Millisecond)
	defer wait.Stop()
	for f.kickCount(repoID) == 0 {
		select {
		case <-wait.C:
			t.Fatalf("coalescer never kicked")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestRPMPutRejectsBadHeader(t *testing.T) {
	f := newRPMFixture(t)
	_, repoID := f.seedRepo("proj", "myrepo", false)

	resp := f.put(t, "/proj/rpm/myrepo/packages/bogus.rpm", []byte("not an rpm"), true)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	out, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(out, []byte("invalid_package")) {
		t.Errorf("body=%s", out)
	}
	pkgs, _ := f.rpmPackages.ListByRepo(context.Background(), repoID)
	if len(pkgs) != 0 {
		t.Errorf("got %d rows after rejected upload", len(pkgs))
	}
}

func TestRPMUploadForbiddenForOutsider(t *testing.T) {
	f := newRPMFixture(t)
	_, _ = f.seedRepo("proj", "myrepo", false)
	// Drop membership.
	if _, err := f.db.Writer.Exec(`DELETE FROM project_members WHERE user_id=?`, f.userID); err != nil {
		t.Fatalf("drop member: %v", err)
	}
	body := readFixtureRPM(t)
	resp := f.put(t, "/proj/rpm/myrepo/packages/sample.rpm", body, true)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestRPMDeleteRemovesFTSRow(t *testing.T) {
	f := newRPMFixture(t)
	_, repoID := f.seedRepo("proj", "myrepo", false)

	body := readFixtureRPM(t)
	resp := f.put(t, "/proj/rpm/myrepo/packages/sample.rpm", body, true)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload: %d", resp.StatusCode)
	}
	// Wait for first kick observed.
	for i := 0; i < 50 && f.kickCount(repoID) == 0; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	first := f.kickCount(repoID)

	resp = f.doMethod(t, http.MethodDelete, "/proj/rpm/myrepo/packages/sample.rpm", true)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status=%d", resp.StatusCode)
	}

	pkgs, _ := f.rpmPackages.ListByRepo(context.Background(), repoID)
	if len(pkgs) != 0 {
		t.Errorf("got %d rows after delete", len(pkgs))
	}
	var n int
	_ = f.db.Reader.QueryRow(`SELECT COUNT(*) FROM rpm_fts WHERE repo_id=?`, repoID).Scan(&n)
	if n != 0 {
		t.Errorf("fts rows=%d after delete", n)
	}
	// Wait for second kick.
	for i := 0; i < 50 && f.kickCount(repoID) <= first; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if f.kickCount(repoID) <= first {
		t.Errorf("coalescer not re-kicked after delete (first=%d now=%d)", first, f.kickCount(repoID))
	}
}

func TestRPMPublicKeyEndpoint(t *testing.T) {
	f := newRPMFixture(t)
	_, _ = f.seedRepo("proj", "myrepo", true) // public_read=true so anonymous works

	resp := f.doMethod(t, http.MethodGet, "/proj/rpm/myrepo/public-key.asc", false)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/pgp-keys" {
		t.Errorf("ct=%q", got)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("BEGIN PGP PUBLIC KEY BLOCK")) {
		t.Errorf("body not armored: %s", body)
	}
}

func TestRPMInvalidFilenameRejected(t *testing.T) {
	f := newRPMFixture(t)
	_, _ = f.seedRepo("proj", "myrepo", false)
	body := readFixtureRPM(t)
	resp := f.put(t, "/proj/rpm/myrepo/packages/sample.txt", body, true)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestRPMGetServePackage(t *testing.T) {
	f := newRPMFixture(t)
	_, _ = f.seedRepo("proj", "myrepo", true) // public_read so anon GETs work

	body := readFixtureRPM(t)
	resp := f.put(t, "/proj/rpm/myrepo/packages/sample.rpm", body, true)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload: %d", resp.StatusCode)
	}

	resp = f.doMethod(t, http.MethodGet, "/proj/rpm/myrepo/packages/sample.rpm", false)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	got, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(got, body) {
		t.Errorf("body roundtrip mismatch (%d vs %d)", len(got), len(body))
	}
}

// --------------------------------------------------------------------------
// Phase 8 Plan 01 (MIRROR-03) — MirrorGuard rejects RPM uploads on mirror repos.
// --------------------------------------------------------------------------

func TestUpload_MirrorRepoReturns403(t *testing.T) {
	f := newRPMFixture(t)
	_, repoID := f.seedRepo("proj", "mirrored", false)
	if err := f.db.WriteTx(context.Background(), func(tx *sql.Tx) error {
		return f.repos.SetMirrorConfigInTx(context.Background(), tx, repoID, metadata.MirrorConfig{
			IsMirror:    true,
			UpstreamURL: "https://mirror.centos.org/centos/9",
			FilterJSON:  `{}`,
			CredID:      nil,
			ScanOnSync:  false,
		})
	}); err != nil {
		t.Fatalf("set mirror cfg: %v", err)
	}
	body := readFixtureRPM(t)
	resp := f.put(t, "/proj/rpm/mirrored/packages/sample.rpm", body, true)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	bodyBytes, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(bodyBytes), "repo_is_mirror") {
		t.Fatalf("body missing repo_is_mirror: %s", bodyBytes)
	}
}

func TestUpload_NonMirrorRepoStillWorks(t *testing.T) {
	f := newRPMFixture(t)
	_, _ = f.seedRepo("proj", "plain", false)
	body := readFixtureRPM(t)
	resp := f.put(t, "/proj/rpm/plain/packages/sample.rpm", body, true)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		bodyBytes, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 201 (non-mirror pass-through); body=%s", resp.StatusCode, bodyBytes)
	}
}
