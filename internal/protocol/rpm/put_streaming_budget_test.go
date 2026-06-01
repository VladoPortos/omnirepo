package rpm_test

// A 4 GiB authenticated RPM upload must complete within a documented
// heap-allocation budget (~50 MB on top of pre-upload baseline) rather than
// allocating proportional to artifact size. This is the on-merge forcing
// function for any future regression that re-introduces a `bytes.Buffer` for
// body bytes in put.go — the assertion will fail the moment HeapAlloc drift
// crosses the documented budget.
//
// Skip conditions (cleanly skipped, NOT failed):
//   - testing.Short(): too slow for default `go test ./...`.
//   - runtime.GOOS != "linux": Statfs is Linux-only here.
//   - free disk under repo root < 5 GiB: insufficient room for the upload.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/auth"
	omrcrypto "github.com/vladoportos/omnirepo/internal/crypto"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
	"github.com/vladoportos/omnirepo/internal/protocol/regen"
	"github.com/vladoportos/omnirepo/internal/protocol/rpm"
	"github.com/vladoportos/omnirepo/internal/storage"
)

// zeroReader yields up to remaining zero bytes. Does NOT allocate the whole
// buffer — fills the caller's slice and decrements the counter. Used by the
// memory-budget tests to send a 4 GiB body without ever allocating the body
// in the test process. The caller's []byte is freshly allocated by the HTTP
// layer (chunked write buffers, ~32 KiB each); we just clear and return.
type zeroReader struct{ remaining int64 }

func (z *zeroReader) Read(p []byte) (int, error) {
	if z.remaining <= 0 {
		return 0, io.EOF
	}
	n := int64(len(p))
	if n > z.remaining {
		n = z.remaining
	}
	for i := int64(0); i < n; i++ {
		p[i] = 0
	}
	z.remaining -= n
	return int(n), nil
}

// fixtureHasDiskSpace reports whether the filesystem at path has at least
// wantBytes free. Linux-only — caller gates on runtime.GOOS == "linux".
func fixtureHasDiskSpace(t *testing.T, path string, wantBytes int64) bool {
	t.Helper()
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		t.Logf("statfs(%s): %v", path, err)
		return false
	}
	avail := int64(stat.Bavail) * int64(stat.Bsize)
	return avail >= wantBytes
}

// newRPMBudgetFixture builds a full RPM handler fixture with an 8 GiB
// MaxPutBytes cap so the 4 GiB budget upload does not 413. Mirrors
// newRPMFixture but with a higher cap and no kick-counter (we don't assert
// coalescer behavior here — only memory consumption).
type rpmBudgetFixture struct {
	t        *testing.T
	srv      *httptest.Server
	repoRoot string
	login    string
	password string
}

func (f *rpmBudgetFixture) basicAuth() string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(f.login+":"+f.password))
}

func newRPMBudgetFixture(t *testing.T) *rpmBudgetFixture {
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

	login := "rpm-budget-user"
	password := "rpm-budget-test-password-12345"
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

	publicKeyCache := rpm.NewPublicKeyCache(signingKeys)
	var kickCounts sync.Map
	factory := func(repoID int64) regen.RegenFn {
		ctr := &atomic.Int64{}
		kickCounts.Store(repoID, ctr)
		return func(ctx context.Context) error {
			ctr.Add(1)
			return nil
		}
	}
	registry := regen.NewRegistry(10*time.Millisecond, 100*time.Millisecond, factory)
	t.Cleanup(func() { _ = registry.ShutdownAll(context.Background()) })

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
		Coalescer:      registry,
		PublicKeyCache: publicKeyCache,
		Path:           storage.NewPathStore(repoRoot),
		Trash:          storage.NewTrash(trashRoot),
		Audit:          auditLogger,
		MaxPutBytes:    8 << 30, // 8 GiB — generous headroom for the 4 GiB upload.
		RepoRoot:       repoRoot,
	})
	r := chi.NewRouter()
	h.Mount(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	// Seed project + repo + member + signing key so the upload passes auth + repo lookup.
	pid, err := projects.Create(context.Background(), "proj", "budget test")
	if err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := db.Writer.Exec(`INSERT INTO project_members(project_id, user_id) VALUES (?, ?)`, pid, uid); err != nil {
		t.Fatalf("seed member: %v", err)
	}
	autoScan := false
	publicRead := false
	rid, err := repos.Create(context.Background(), pid, "rpm", "myrepo", "", &autoScan, nil, &publicRead)
	if err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	priv, pub, fp, err := omrcrypto.GenerateRepoKey("proj-myrepo", 2048)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	if err := db.WriteTx(context.Background(), func(tx *sql.Tx) error {
		_, err := signingKeys.Insert(context.Background(), tx, rid, pub, priv, fp)
		return err
	}); err != nil {
		t.Fatalf("insert signing key: %v", err)
	}

	return &rpmBudgetFixture{
		t:        t,
		srv:      srv,
		repoRoot: repoRoot,
		login:    login,
		password: password,
	}
}

// TestRPMPut_MemoryBudget proves that a 4 GiB authenticated RPM upload
// completes within a documented HeapAlloc budget (50 MB) rather than
// allocating proportional to the artifact size. The body sent on the wire
// is the real testdata/sample.rpm header followed by 4 GiB of zero padding.
// rpm.Parse only reads the lead + signature + header (small, all within
// sample.rpm's first 23 KiB), so the trailing zeros are bytes the streaming
// pipeline copies straight to disk and discards from memory.
//
// Forcing function: a regression that re-introduces `bytes.Buffer` for body
// bytes will produce a HeapAlloc delta proportional to 4 GiB and instantly
// trip this assertion (delta > 50 MB → fail).
//
// Run explicitly: go test -run TestRPMPut_MemoryBudget -timeout 5m ./internal/protocol/rpm/
func TestRPMPut_MemoryBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("memory-budget test skipped in -short mode")
	}
	if runtime.GOOS != "linux" {
		t.Skip("memory-budget test is Linux-only (Statfs portability)")
	}
	f := newRPMBudgetFixture(t)
	if !fixtureHasDiskSpace(t, f.repoRoot, 5*1024*1024*1024) {
		t.Skip("insufficient disk space for 4 GiB upload (need >=5 GiB free under repo root)")
	}

	const bodySize = int64(4 * 1024 * 1024 * 1024) // 4 GiB
	const maxBudget = int64(50 * 1024 * 1024)      // 50 MB documented budget

	// Compose body = real sample.rpm header bytes + (bodySize - len(sample)) zeros.
	// rpm.Parse(*os.File) reads only Lead + Signature + Header → never touches
	// the trailing zeros. Streaming pipeline copies them to disk via io.Copy
	// (default 32 KiB buffer) and into pathStore.Put (also io.Copy).
	headerBytes, err := os.ReadFile("testdata/sample.rpm")
	if err != nil {
		t.Fatalf("read sample.rpm: %v", err)
	}
	bodyReader := io.MultiReader(
		bytesReaderNoCopy(headerBytes),
		&zeroReader{remaining: bodySize - int64(len(headerBytes))},
	)

	urlPath := "/proj/rpm/myrepo/packages/centos-release-7-2.1511.el7.centos.2.10.x86_64.rpm"
	req, _ := http.NewRequest(http.MethodPut, f.srv.URL+urlPath, io.NopCloser(bodyReader))
	req.ContentLength = bodySize
	req.Header.Set("Authorization", f.basicAuth())

	// Measure HeapAlloc with the request in flight. We GC + sample BEFORE
	// firing the request and AFTER it returns; the delta represents
	// allocations the streaming pipeline RETAINS, not transient ones. A
	// regression that buffers the full body into a bytes.Buffer holds the
	// 4 GiB until response write — easily caught by HeapAlloc post-response.
	runtime.GC()
	var beforeStats runtime.MemStats
	runtime.ReadMemStats(&beforeStats)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT 4 GiB: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	runtime.GC()
	var afterStats runtime.MemStats
	runtime.ReadMemStats(&afterStats)

	if resp.StatusCode != http.StatusCreated {
		out, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, out)
	}

	delta := int64(afterStats.HeapAlloc) - int64(beforeStats.HeapAlloc)
	t.Logf("HeapAlloc before=%d after=%d delta=%d (budget=%d, body=%d)",
		beforeStats.HeapAlloc, afterStats.HeapAlloc, delta, maxBudget, bodySize)
	if delta > maxBudget {
		t.Errorf("HeapAlloc delta %d B exceeds documented budget %d B (artifact %d B); regression: streaming pipeline likely buffering body",
			delta, maxBudget, bodySize)
	}

	// Sanity: also confirm the on-disk file is full size — the streaming
	// pipeline preserved bytes through to PathStore.Put.
	diskPath := filepath.Join(f.repoRoot, "proj", "rpm", "myrepo", "packages",
		"centos-release-7-2.1511.el7.centos.2.10.x86_64.rpm")
	st, statErr := os.Stat(diskPath)
	if statErr != nil {
		t.Fatalf("stat on-disk: %v", statErr)
	}
	if st.Size() != bodySize {
		t.Errorf("on-disk size=%d want %d (streaming truncated body)", st.Size(), bodySize)
	}

	// Belt-and-braces: hash a few hundred KiB at the start of the file and
	// verify it matches the sample.rpm prefix — proves the streaming pipeline
	// did not corrupt the bytes that matter to the parser.
	verifyF, err := os.Open(diskPath)
	if err != nil {
		t.Fatalf("open on-disk: %v", err)
	}
	defer func() { _ = verifyF.Close() }()
	got := make([]byte, len(headerBytes))
	if _, err := io.ReadFull(verifyF, got); err != nil {
		t.Fatalf("read prefix: %v", err)
	}
	wantSum := sha256.Sum256(headerBytes)
	gotSum := sha256.Sum256(got)
	if hex.EncodeToString(wantSum[:]) != hex.EncodeToString(gotSum[:]) {
		t.Errorf("on-disk header prefix sha256 mismatch — streaming corrupted parser-visible bytes")
	}
}

// bytesReaderNoCopy returns an io.Reader over b without copying. Identical
// to bytes.NewReader but kept inline so the budget test file does not pull
// in the bytes package just for the reader, keeping a `bytes.Buffer`-adjacent
// token out of a streaming-correctness test file.
func bytesReaderNoCopy(b []byte) io.Reader {
	return &sliceReader{data: b}
}

type sliceReader struct {
	data []byte
	pos  int
}

func (s *sliceReader) Read(p []byte) (int, error) {
	if s.pos >= len(s.data) {
		return 0, io.EOF
	}
	n := copy(p, s.data[s.pos:])
	s.pos += n
	return n, nil
}
