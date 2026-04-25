package deb_test

// Plan 05-04 STREAMIO-07 (audit finding #3 budget guard for DEB): a 4 GiB
// authenticated DEB upload must complete within a documented heap-allocation
// budget (~50 MB on top of pre-upload baseline) rather than allocating
// proportional to the artifact size. Forcing function for any future
// regression that re-introduces a `bytes.Buffer` for body bytes in put.go.
//
// Skip conditions (cleanly skipped, NOT failed):
//   - testing.Short(): too slow for default `go test ./...`.
//   - runtime.GOOS != "linux": Statfs is Linux-only here.
//   - free disk under repo root < 5 GiB: insufficient room for the upload.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
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

// zeroReader yields up to remaining zero bytes without allocating the whole
// buffer. Used by the budget tests to send a 4 GiB body without ever
// allocating the body in the test process.
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

type debBudgetFixture struct {
	t        *testing.T
	srv      *httptest.Server
	repoRoot string
	login    string
	password string
}

func (f *debBudgetFixture) basicAuth() string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(f.login+":"+f.password))
}

// newDEBBudgetFixture mirrors newDEBFixture but with an 8 GiB MaxPutBytes
// cap so the 4 GiB budget upload does not 413.
func newDEBBudgetFixture(t *testing.T) *debBudgetFixture {
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

	login := "deb-budget-user"
	password := "deb-budget-test-password-12345"
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

	publicKeyCache := deb.NewPublicKeyCache(signingKeys)
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
		Coalescer:      registry,
		PublicKeyCache: publicKeyCache,
		Path:           storage.NewPathStore(repoRoot),
		Trash:          storage.NewTrash(trashRoot),
		Audit:          auditLogger,
		MaxPutBytes:    8 << 30, // 8 GiB headroom for the 4 GiB upload.
		RepoRoot:       repoRoot,
	})
	r := chi.NewRouter()
	h.Mount(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	pid, err := projects.Create(context.Background(), "proj", "budget test")
	if err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := db.Writer.Exec(`INSERT INTO project_members(project_id, user_id) VALUES (?, ?)`, pid, uid); err != nil {
		t.Fatalf("seed member: %v", err)
	}
	autoScan := false
	publicRead := false
	rid, err := repos.Create(context.Background(), pid, "deb", "myrepo", "", &autoScan, nil, &publicRead)
	if err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	priv, pub, fp, err := omrcrypto.GenerateRepoKey("proj-myrepo-omnirepo", 2048)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	if err := db.WriteTx(context.Background(), func(tx *sql.Tx) error {
		if _, err := signingKeys.Insert(context.Background(), tx, rid, pub, priv, fp); err != nil {
			return err
		}
		return aptSuites.InsertBatch(context.Background(), tx, rid, []metadata.AptSuite{
			{RepoID: rid, Suite: "stable", Component: "main", Architecture: "amd64"},
		})
	}); err != nil {
		t.Fatalf("seed signing key + suites: %v", err)
	}

	return &debBudgetFixture{
		t:        t,
		srv:      srv,
		repoRoot: repoRoot,
		login:    login,
		password: password,
	}
}

// buildLargeDEBPrefix builds the small head of a 4 GiB .deb body: ar global
// header + debian-binary member + control.tar.gz member + the ar header for
// a data.tar member sized to fill the rest of the body. Returns the prefix
// bytes (small, ~few KB) plus the exact byte count the data.tar payload
// should occupy. ParseDeb iterates ar members, parses control.tar, returns
// — never reads the data.tar payload bytes (drains via io.Discard at most,
// and even then bounded by io.Copy buffer size).
func buildLargeDEBPrefix(t *testing.T, totalBodySize int64, pkgName, version string) (prefix []byte, dataMemberSize int64) {
	t.Helper()

	// control.tar.gz with ./control paragraph naming a real package + arch.
	var ctlBuf bytes.Buffer
	tw := tar.NewWriter(&ctlBuf)
	ctl := "Package: " + pkgName + "\n" +
		"Version: " + version + "\n" +
		"Architecture: amd64\n" +
		"Maintainer: Test <t@e.com>\n" +
		"Description: budget test package\n"
	if err := tw.WriteHeader(&tar.Header{Name: "./control", Mode: 0o644, Size: int64(len(ctl))}); err != nil {
		t.Fatalf("control tar header: %v", err)
	}
	if _, err := tw.Write([]byte(ctl)); err != nil {
		t.Fatalf("control tar write: %v", err)
	}
	_ = tw.Close()

	var ctlGzBuf bytes.Buffer
	gz := gzip.NewWriter(&ctlGzBuf)
	_, _ = gz.Write(ctlBuf.Bytes())
	_ = gz.Close()
	ctlGz := ctlGzBuf.Bytes()

	// Build the prefix: ar global header + debian-binary member + control.tar.gz.
	var prefBuf bytes.Buffer
	aw := ar.NewWriter(&prefBuf)
	if err := aw.WriteGlobalHeader(); err != nil {
		t.Fatalf("ar global header: %v", err)
	}
	debianBinary := []byte("2.0\n")
	if err := aw.WriteHeader(&ar.Header{Name: "debian-binary", Size: int64(len(debianBinary)), Mode: 0o644}); err != nil {
		t.Fatalf("debian-binary header: %v", err)
	}
	if _, err := aw.Write(debianBinary); err != nil {
		t.Fatalf("debian-binary write: %v", err)
	}
	if err := aw.WriteHeader(&ar.Header{Name: "control.tar.gz", Size: int64(len(ctlGz)), Mode: 0o644}); err != nil {
		t.Fatalf("control.tar.gz header: %v", err)
	}
	if _, err := aw.Write(ctlGz); err != nil {
		t.Fatalf("control.tar.gz write: %v", err)
	}

	prefBuilt := prefBuf.Bytes()

	// Now compute how many bytes the data.tar member should be so the TOTAL
	// body length equals totalBodySize. The ar member header itself is
	// HEADER_BYTE_SIZE (60) bytes; member size must be even (ar pads with \n
	// otherwise). Use even sizes so no extra padding byte.
	const arHeaderSize = 60
	remaining := totalBodySize - int64(len(prefBuilt)) - arHeaderSize
	if remaining < 0 {
		t.Fatalf("totalBodySize %d too small (prefix already %d B)", totalBodySize, len(prefBuilt))
	}
	if remaining%2 != 0 {
		// Force even — caller passes even totals (4 GiB is divisible by 2).
		remaining--
	}

	// Append the data.tar header (size = remaining bytes of zeros) to the prefix.
	if err := aw.WriteHeader(&ar.Header{Name: "data.tar", Size: remaining, Mode: 0o644}); err != nil {
		t.Fatalf("data.tar header: %v", err)
	}
	// We do NOT call aw.Write for the data.tar payload — those bytes will be
	// streamed via zeroReader concatenated below. The header is now in
	// prefBuf.
	return prefBuf.Bytes(), remaining
}

// TestDEBPut_MemoryBudget proves STREAMIO-07: a 4 GiB authenticated DEB
// upload completes within a documented HeapAlloc budget (50 MB) rather than
// allocating proportional to the artifact size. The body is a real ar
// archive whose data.tar member is 4 GiB of zeros. ParseDeb iterates ar
// members and returns after control.tar.gz — never materialises the
// data.tar payload in memory; pathStore.Put streams the bytes from the
// staged tmp file via io.Copy.
//
// Run explicitly: go test -run TestDEBPut_MemoryBudget -timeout 5m ./internal/protocol/deb/
func TestDEBPut_MemoryBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("memory-budget test skipped in -short mode")
	}
	if runtime.GOOS != "linux" {
		t.Skip("memory-budget test is Linux-only (Statfs portability)")
	}
	f := newDEBBudgetFixture(t)
	if !fixtureHasDiskSpace(t, f.repoRoot, 5*1024*1024*1024) {
		t.Skip("insufficient disk space for 4 GiB upload (need >=5 GiB free under repo root)")
	}

	const bodySize = int64(4 * 1024 * 1024 * 1024)
	const maxBudget = int64(50 * 1024 * 1024) // 50 MB documented per CONTEXT D-11

	prefix, dataMemberSize := buildLargeDEBPrefix(t, bodySize, "mypkg", "1.0-1")
	bodyReader := io.MultiReader(
		bytes.NewReader(prefix),
		&zeroReader{remaining: dataMemberSize},
	)
	actualBodySize := int64(len(prefix)) + dataMemberSize

	urlPath := "/proj/deb/myrepo/pool/m/mypkg/mypkg_1.0-1_amd64.deb?suite=stable&component=main"
	req, _ := http.NewRequest(http.MethodPut, f.srv.URL+urlPath, io.NopCloser(bodyReader))
	req.ContentLength = actualBodySize
	req.Header.Set("Authorization", f.basicAuth())

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
		beforeStats.HeapAlloc, afterStats.HeapAlloc, delta, maxBudget, actualBodySize)
	if delta > maxBudget {
		t.Errorf("HeapAlloc delta %d B exceeds documented budget %d B (artifact %d B); regression: streaming pipeline likely buffering body",
			delta, maxBudget, actualBodySize)
	}

	// Sanity: on-disk size matches.
	diskPath := filepath.Join(f.repoRoot, "proj", "deb", "myrepo",
		"pool", "m", "mypkg", "mypkg_1.0-1_amd64.deb")
	st, statErr := os.Stat(diskPath)
	if statErr != nil {
		t.Fatalf("stat on-disk: %v", statErr)
	}
	if st.Size() != actualBodySize {
		t.Errorf("on-disk size=%d want %d (streaming truncated body)", st.Size(), actualBodySize)
	}

	// Verify the prefix bytes (parser-visible region) round-tripped intact.
	verifyF, err := os.Open(diskPath)
	if err != nil {
		t.Fatalf("open on-disk: %v", err)
	}
	defer func() { _ = verifyF.Close() }()
	got := make([]byte, len(prefix))
	if _, err := io.ReadFull(verifyF, got); err != nil {
		t.Fatalf("read prefix: %v", err)
	}
	wantSum := sha256.Sum256(prefix)
	gotSum := sha256.Sum256(got)
	if hex.EncodeToString(wantSum[:]) != hex.EncodeToString(gotSum[:]) {
		t.Errorf("on-disk prefix sha256 mismatch — streaming corrupted parser-visible bytes")
	}
}
