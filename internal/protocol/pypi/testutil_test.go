package pypi_test

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/auth"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
	"github.com/dxc-internal/omnirepo/internal/protocol/pypi"
	"github.com/dxc-internal/omnirepo/internal/protocol/regen"
	"github.com/dxc-internal/omnirepo/internal/storage"
)

type handlerFixture struct {
	t        *testing.T
	db       *metadata.DB
	users    *metadata.UsersRepo
	apiKeys  *metadata.APIKeysRepo
	repos    *metadata.ReposRepo
	projects *metadata.ProjectsRepo
	pypiRepo *metadata.PyPIFilesRepo
	scans    *metadata.ScansRepo
	auditLog audit.Logger
	srv      *httptest.Server
	dataRoot string
	repoRoot string
	login    string
	password string
	userID   int64

	kickCounts sync.Map
	registry   *regen.Registry
	pep694     *pypi.PEP694Sessions
}

func (f *handlerFixture) kickCount(repoID int64) int64 {
	v, ok := f.kickCounts.Load(repoID)
	if !ok {
		return 0
	}
	return v.(*atomic.Int64).Load()
}

func newHandlerFixture(t *testing.T) *handlerFixture {
	t.Helper()
	db := sqlitetest.New(t)
	users := metadata.NewUsersRepo(db)
	apiKeys := metadata.NewAPIKeysRepo(db)
	repos := metadata.NewReposRepo(db)
	projects := metadata.NewProjectsRepo(db)
	pypiRepo := metadata.NewPyPIFilesRepo(db)
	scans := metadata.NewScansRepo(db)
	sessions := metadata.NewSessionsRepo(db)

	login := "pypi-user"
	password := "pypi-test-password-1234567"
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
	if err := os.MkdirAll(repoRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(trashRoot, 0o750); err != nil {
		t.Fatal(err)
	}

	auditPath := filepath.Join(dataRoot, "logs", "audit.log")
	if err := os.MkdirAll(filepath.Dir(auditPath), 0o750); err != nil {
		t.Fatal(err)
	}
	auditLog, err := audit.New(db, auditPath, 10, 1)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	f := &handlerFixture{
		t:        t,
		db:       db,
		users:    users,
		apiKeys:  apiKeys,
		repos:    repos,
		projects: projects,
		pypiRepo: pypiRepo,
		scans:    scans,
		auditLog: auditLog,
		dataRoot: dataRoot,
		repoRoot: repoRoot,
		login:    login,
		password: password,
		userID:   uid,
	}

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

	f.pep694 = pypi.NewPEP694Sessions(1 * time.Second)

	h := pypi.New(pypi.Deps{
		DB:          db,
		Users:       users,
		APIKeys:     apiKeys,
		Sessions:    sessions,
		Repos:       repos,
		Projects:    projects,
		Members:     metadata.NewMembersRepo(db),
		PyPIFiles:   pypiRepo,
		Scans:       scans,
		Coalescer:   f.registry,
		PEP694:      f.pep694,
		Path:        storage.NewPathStore(repoRoot),
		Trash:       storage.NewTrash(trashRoot),
		Audit:       auditLog,
		MaxPutBytes: 4 << 20,
		RepoRoot:    repoRoot,
	})
	r := chi.NewRouter()
	h.Mount(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	f.srv = srv
	return f
}

func (f *handlerFixture) seedRepo(projName, repoName string, publicRead, autoScan bool) (int64, int64) {
	pid, err := f.projects.Create(context.Background(), projName, "test")
	if err != nil {
		f.t.Fatalf("project: %v", err)
	}
	if _, err := f.db.Writer.Exec(`INSERT INTO project_members(project_id, user_id) VALUES (?,?)`, pid, f.userID); err != nil {
		f.t.Fatalf("member: %v", err)
	}
	rid, err := f.repos.Create(context.Background(), pid, "pypi", repoName, "", &autoScan, nil, &publicRead)
	if err != nil {
		f.t.Fatalf("repo: %v", err)
	}
	return pid, rid
}

func (f *handlerFixture) basicAuth() string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(f.login+":"+f.password))
}

func (f *handlerFixture) waitForKick(t *testing.T, repoID int64, expected int64) {
	t.Helper()
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if f.kickCount(repoID) >= expected {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("kick count for repo %d: got %d, want >= %d", repoID, f.kickCount(repoID), expected)
}

// makeWheelBytes builds an in-memory wheel with the supplied metadata.
func makeWheelBytes(t *testing.T, name, version string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	distInfo := name + "-" + version + ".dist-info"
	w, err := zw.Create(distInfo + "/METADATA")
	if err != nil {
		t.Fatal(err)
	}
	body := "Metadata-Version: 2.1\nName: " + name + "\nVersion: " + version + "\nRequires-Python: >=3.8\nSummary: test pkg\n"
	_, _ = w.Write([]byte(body))
	rw, _ := zw.Create(distInfo + "/RECORD")
	_, _ = rw.Write([]byte(""))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// makeSdistBytes builds an in-memory tar.gz sdist.
func makeSdistBytes(t *testing.T, name, version string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	top := name + "-" + version
	body := []byte("Metadata-Version: 2.1\nName: " + name + "\nVersion: " + version + "\nSummary: sdist\n")
	_ = tw.WriteHeader(&tar.Header{Name: top + "/PKG-INFO", Mode: 0o644, Size: int64(len(body))})
	_, _ = tw.Write(body)
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

// twineUpload posts a multipart body emulating `twine upload` to /legacy/.
func twineUpload(t *testing.T, srvURL, projName, repoName, filename string, content []byte, filetype, auth string) *http.Response {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("name", filename)
	_ = mw.WriteField("version", "0")
	_ = mw.WriteField("filetype", filetype)
	fw, err := mw.CreateFormFile("content", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(fw, bytes.NewReader(content)); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	url := fmt.Sprintf("%s/%s/pypi/%s/legacy/", srvURL, projName, repoName)
	req, _ := http.NewRequest(http.MethodPost, url, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	return resp
}
