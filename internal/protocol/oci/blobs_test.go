package oci_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/auth"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
	"github.com/dxc-internal/omnirepo/internal/protocol/oci"
	"github.com/dxc-internal/omnirepo/internal/storage"
)

// blobFixture wires a full /v2 handler with real CAS, audit, and blob repos
// against a sqlitetest DB + tmp data root. Used by every Task 2 test.
type blobFixture struct {
	t         *testing.T
	db        *metadata.DB
	users     *metadata.UsersRepo
	apiKeys   *metadata.APIKeysRepo
	repos     *metadata.ReposRepo
	projects  *metadata.ProjectsRepo
	members   *metadata.MembersRepo
	blobs     *metadata.DockerBlobsRepo
	blobUp    *metadata.BlobUploadsRepo
	sess      *metadata.BlobUploadSessionsRepo
	dataRoot  string
	cas       storage.CAS
	srv       *httptest.Server
	secret    []byte
	userID    int64
	login     string
	password  string
	projectID int64
	repoID    int64
	repoPath  string // "<project>/docker/<repo>"
	token     string // bearer token for the user
	audit     *recordingAudit
}

// recordingAudit is a thin wrapper around a real audit.Logger that also
// captures every event into an in-memory slice for assertions.
type recordingAudit struct {
	real audit.Logger
	mu   sync.Mutex
	evts []audit.Event
}

func (r *recordingAudit) Record(ctx context.Context, e audit.Event) error {
	r.mu.Lock()
	r.evts = append(r.evts, e)
	r.mu.Unlock()
	if r.real != nil {
		return r.real.Record(ctx, e)
	}
	return nil
}

func (r *recordingAudit) kinds() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.evts))
	for _, e := range r.evts {
		out = append(out, string(e.Kind))
	}
	return out
}

func newBlobFixture(t *testing.T) *blobFixture {
	t.Helper()
	db := sqlitetest.New(t)
	dataRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataRoot, "tmp", "uploads"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dataRoot, "blobs"), 0o750); err != nil {
		t.Fatal(err)
	}

	users := metadata.NewUsersRepo(db)
	apiKeys := metadata.NewAPIKeysRepo(db)
	repos := metadata.NewReposRepo(db)
	projects := metadata.NewProjectsRepo(db)
	sessionsRepo := metadata.NewSessionsRepo(db)
	members := metadata.NewMembersRepo(db)
	blobsRepo := metadata.NewDockerBlobsRepo(db)
	blobUploads := metadata.NewBlobUploadsRepo(db)
	sessRepo := metadata.NewBlobUploadSessionsRepo(db)
	cas := storage.NewCAS(filepath.Join(dataRoot, "blobs"))

	ndjson := filepath.Join(dataRoot, "audit.log")
	realAudit, err := audit.New(db, ndjson, 10, 2)
	if err != nil {
		t.Fatal(err)
	}
	rec := &recordingAudit{real: realAudit}

	// Seed a user.
	login := "pusher"
	password := "correct-horse-battery-staple-42"
	pwHash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	uid, err := users.Create(context.Background(), login, "u@example.com", pwHash, false, false)
	if err != nil {
		t.Fatal(err)
	}

	// Seed a project + docker repo; user is a member.
	pid, err := projects.Create(context.Background(), "proj", "test project")
	if err != nil {
		t.Fatal(err)
	}
	rid, err := repos.Create(context.Background(), pid, "docker", "app", "", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := members.Add(context.Background(), pid, uid); err != nil {
		t.Fatal(err)
	}

	secret := []byte("0123456789abcdef0123456789abcdef")
	handler := oci.New(oci.Deps{
		DB:          db,
		Users:       users,
		APIKeys:     apiKeys,
		Repos:       repos,
		Projects:    projects,
		Sessions:    sessionsRepo,
		Members:     members,
		CAS:         cas,
		Blobs:       blobsRepo,
		BlobUploads: blobUploads,
		Sess:        sessRepo,
		Audit:       rec,
		DataRoot:    dataRoot,
		HMACSecret:  secret,
		JWTTTL:      time.Hour,
		// Keep the default chunk cap (64 MiB) to exercise real code path;
		// override in tests that need a smaller cap.
	})
	r := chi.NewRouter()
	handler.Mount(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	f := &blobFixture{
		t:         t,
		db:        db,
		users:     users,
		apiKeys:   apiKeys,
		repos:     repos,
		projects:  projects,
		members:   members,
		blobs:     blobsRepo,
		blobUp:    blobUploads,
		sess:      sessRepo,
		dataRoot:  dataRoot,
		cas:       cas,
		srv:       srv,
		secret:    secret,
		userID:    uid,
		login:     login,
		password:  password,
		projectID: pid,
		repoID:    rid,
		repoPath:  "proj/docker/app",
		audit:     rec,
	}
	f.token = f.mintToken()
	return f
}

func (f *blobFixture) mintToken() string {
	f.t.Helper()
	req, _ := http.NewRequest("GET", f.srv.URL+"/v2/token", nil)
	req.Header.Set("Authorization", "Basic "+basicEncode(f.login+":"+f.password))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatalf("mint token: %v", err)
	}
	defer resp.Body.Close()
	var payload struct {
		Token string `json:"token"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&payload)
	if payload.Token == "" {
		f.t.Fatalf("empty token")
	}
	return payload.Token
}

func basicEncode(s string) string {
	// Avoid import cycle with handler_test.go's basicAuth.
	return b64StdEncode([]byte(s))
}

func b64StdEncode(b []byte) string {
	// simple inline base64 std encode.
	const alpha = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var sb strings.Builder
	n := len(b)
	for i := 0; i < n; i += 3 {
		var chunk [3]byte
		copy(chunk[:], b[i:])
		l := n - i
		if l > 3 {
			l = 3
		}
		sb.WriteByte(alpha[chunk[0]>>2])
		sb.WriteByte(alpha[((chunk[0]&0x03)<<4)|(chunk[1]>>4)])
		if l > 1 {
			sb.WriteByte(alpha[((chunk[1]&0x0f)<<2)|(chunk[2]>>6)])
		} else {
			sb.WriteByte('=')
		}
		if l > 2 {
			sb.WriteByte(alpha[chunk[2]&0x3f])
		} else {
			sb.WriteByte('=')
		}
	}
	return sb.String()
}

// authed is a helper that adds the fixture's bearer token to req.
func (f *blobFixture) authed(req *http.Request) *http.Request {
	req.Header.Set("Authorization", "Bearer "+f.token)
	return req
}

func hexDigest(b []byte) string {
	s := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(s[:])
}

// TestBlobUploadChunked_HappyPath: POST → PATCH (1 chunk) → PUT → GET.
// Asserts all spec headers (Location, Range, Docker-Content-Digest).
func TestBlobUploadChunked_HappyPath(t *testing.T) {
	f := newBlobFixture(t)

	// 1. POST a new upload.
	postReq, _ := http.NewRequest("POST", f.srv.URL+"/v2/"+f.repoPath+"/blobs/uploads/", nil)
	postResp, err := http.DefaultClient.Do(f.authed(postReq))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	postResp.Body.Close()
	if postResp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST status=%d, want 202", postResp.StatusCode)
	}
	loc := postResp.Header.Get("Location")
	if !strings.HasPrefix(loc, "/v2/"+f.repoPath+"/blobs/uploads/") {
		t.Fatalf("POST Location=%q", loc)
	}
	if postResp.Header.Get("Range") != "0-0" {
		t.Fatalf("POST Range=%q, want 0-0", postResp.Header.Get("Range"))
	}

	// 2. PATCH the body. Build the upload URL from the Location header
	// (already absolute path; prepend srv.URL).
	body := bytes.Repeat([]byte("A"), 7)
	patchReq, _ := http.NewRequest("PATCH", f.srv.URL+loc, bytes.NewReader(body))
	patchReq.ContentLength = int64(len(body))
	patchReq.Header.Set("Content-Type", "application/octet-stream")
	patchResp, err := http.DefaultClient.Do(f.authed(patchReq))
	if err != nil {
		t.Fatalf("PATCH: %v", err)
	}
	patchResp.Body.Close()
	if patchResp.StatusCode != http.StatusAccepted {
		t.Fatalf("PATCH status=%d, want 202", patchResp.StatusCode)
	}
	if got := patchResp.Header.Get("Range"); got != "0-6" {
		t.Fatalf("PATCH Range=%q, want 0-6", got)
	}

	// 3. PUT with claimed digest. Use an empty final body (common client pattern).
	digest := hexDigest(body)
	putURL := f.srv.URL + loc + "?digest=" + digest
	putReq, _ := http.NewRequest("PUT", putURL, nil)
	putResp, err := http.DefaultClient.Do(f.authed(putReq))
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	defer putResp.Body.Close()
	if putResp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(putResp.Body)
		t.Fatalf("PUT status=%d, want 201; body=%s", putResp.StatusCode, b)
	}
	if got := putResp.Header.Get("Docker-Content-Digest"); got != digest {
		t.Fatalf("PUT Docker-Content-Digest=%q, want %q", got, digest)
	}
	// Location points at /v2/<name>/blobs/<digest>.
	wantLoc := "/v2/" + f.repoPath + "/blobs/" + digest
	if got := putResp.Header.Get("Location"); got != wantLoc {
		t.Fatalf("PUT Location=%q, want %q", got, wantLoc)
	}

	// 4. docker_blobs row exists at ref_count=0.
	b, err := f.blobs.Stat(context.Background(), digest)
	if err != nil || b == nil {
		t.Fatalf("blobs.Stat: b=%+v err=%v", b, err)
	}
	if b.RefCount != 0 {
		t.Fatalf("ref_count=%d, want 0 (manifest PUT 02-07 will ++)", b.RefCount)
	}
	if b.SizeBytes != int64(len(body)) {
		t.Fatalf("size=%d, want %d", b.SizeBytes, len(body))
	}

	// 5. CAS file is at canonical path.
	hx := digest[len("sha256:"):]
	casPath := filepath.Join(f.dataRoot, "blobs", "sha256", hx[:2], hx)
	if _, err := os.Stat(casPath); err != nil {
		t.Fatalf("CAS file not at %s: %v", casPath, err)
	}

	// 6. Audit event emitted.
	if !containsStr(f.audit.kinds(), string(audit.EvtOCIBlobUploaded)) {
		t.Fatalf("missing audit event; kinds=%v", f.audit.kinds())
	}

	// 7. GET the blob back.
	getReq, _ := http.NewRequest("GET", f.srv.URL+wantLoc, nil)
	getResp, err := http.DefaultClient.Do(f.authed(getReq))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET status=%d", getResp.StatusCode)
	}
	got, _ := io.ReadAll(getResp.Body)
	if !bytes.Equal(got, body) {
		t.Fatalf("GET body=%q, want %q", got, body)
	}
	if got := getResp.Header.Get("Docker-Content-Digest"); got != digest {
		t.Fatalf("GET missing digest header: %q", got)
	}
}

// TestBlobUploadMonolithic_HappyPath: POST with body + ?digest=.
func TestBlobUploadMonolithic_HappyPath(t *testing.T) {
	f := newBlobFixture(t)
	body := []byte("monolithic-payload")
	digest := hexDigest(body)

	url := f.srv.URL + "/v2/" + f.repoPath + "/blobs/uploads/?digest=" + digest
	req, _ := http.NewRequest("POST", url, bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := http.DefaultClient.Do(f.authed(req))
	if err != nil {
		t.Fatalf("POST monolithic: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d, want 201; body=%s", resp.StatusCode, b)
	}
	if got := resp.Header.Get("Docker-Content-Digest"); got != digest {
		t.Fatalf("digest header=%q want=%q", got, digest)
	}
	b, _ := f.blobs.Stat(context.Background(), digest)
	if b == nil || b.SizeBytes != int64(len(body)) {
		t.Fatalf("blob row missing or wrong size: %+v", b)
	}
}

// TestBlobRangeGET ranges into a blob with Range: bytes=10-20.
func TestBlobRangeGET(t *testing.T) {
	f := newBlobFixture(t)
	body := []byte("ABCDEFGHIJ1234567890abcdefghij") // 30 bytes
	digest := f.pushMonolithic(body)

	getReq, _ := http.NewRequest("GET", f.srv.URL+"/v2/"+f.repoPath+"/blobs/"+digest, nil)
	getReq.Header.Set("Range", "bytes=10-20")
	resp, err := http.DefaultClient.Do(f.authed(getReq))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("status=%d, want 206", resp.StatusCode)
	}
	gotSlice, _ := io.ReadAll(resp.Body)
	want := body[10:21]
	if !bytes.Equal(gotSlice, want) {
		t.Fatalf("slice=%q want=%q", gotSlice, want)
	}
	cr := resp.Header.Get("Content-Range")
	if cr != fmt.Sprintf("bytes 10-20/%d", len(body)) {
		t.Fatalf("Content-Range=%q", cr)
	}
}

// TestBlobHEAD verifies 200 + Content-Length + Docker-Content-Digest when
// present, 404 otherwise.
func TestBlobHEAD(t *testing.T) {
	f := newBlobFixture(t)
	body := []byte("head-body")
	digest := f.pushMonolithic(body)

	headReq, _ := http.NewRequest("HEAD", f.srv.URL+"/v2/"+f.repoPath+"/blobs/"+digest, nil)
	resp, err := http.DefaultClient.Do(f.authed(headReq))
	if err != nil {
		t.Fatalf("HEAD: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if got := resp.Header.Get("Docker-Content-Digest"); got != digest {
		t.Fatalf("digest=%q", got)
	}
	if got := resp.Header.Get("Content-Length"); got != fmt.Sprintf("%d", len(body)) {
		t.Fatalf("Content-Length=%q", got)
	}

	// Unknown digest → 404.
	bogus := "sha256:" + strings.Repeat("b", 64)
	headReq2, _ := http.NewRequest("HEAD", f.srv.URL+"/v2/"+f.repoPath+"/blobs/"+bogus, nil)
	resp2, err := http.DefaultClient.Do(f.authed(headReq2))
	if err != nil {
		t.Fatalf("HEAD: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown HEAD status=%d, want 404", resp2.StatusCode)
	}
}

// TestBlobGet_UnknownDigest_DoesNotLeakFSPath is the F-05.2 regression:
// the 404 BLOB_UNKNOWN envelope must not echo the CAS filesystem path in
// its `detail` field. Before the fix, os.Open's PathError.Error() was
// copied verbatim as detail, exposing <dataRoot>/blobs/sha256/<aa>/<hex>.
func TestBlobGet_UnknownDigest_DoesNotLeakFSPath(t *testing.T) {
	f := newBlobFixture(t)
	bogus := "sha256:" + strings.Repeat("c", 64)

	req, _ := http.NewRequest("GET", f.srv.URL+"/v2/"+f.repoPath+"/blobs/"+bogus, nil)
	resp, err := http.DefaultClient.Do(f.authed(req))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var env struct {
		Errors []struct {
			Code   string `json:"code"`
			Detail string `json:"detail"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode envelope: %v; body=%s", err, body)
	}
	if len(env.Errors) == 0 || env.Errors[0].Code != "BLOB_UNKNOWN" {
		t.Fatalf("want BLOB_UNKNOWN; got %s", body)
	}
	// The dataRoot path must not appear anywhere in the response.
	if strings.Contains(string(body), f.dataRoot) {
		t.Fatalf("envelope leaks dataRoot %q: %s", f.dataRoot, body)
	}
	// Belt-and-suspenders: no "/blobs/sha256/" subpath either.
	if strings.Contains(env.Errors[0].Detail, "/blobs/sha256/") {
		t.Fatalf("detail leaks CAS layout: %q", env.Errors[0].Detail)
	}
}

// TestBlobDelete_RefCountZero_AllowsDelete removes the docker_blobs row
// when ref_count==0 and emits the oci.blob.deleted audit event.
func TestBlobDelete_RefCountZero_AllowsDelete(t *testing.T) {
	f := newBlobFixture(t)
	body := []byte("delete-me")
	digest := f.pushMonolithic(body)

	delReq, _ := http.NewRequest("DELETE", f.srv.URL+"/v2/"+f.repoPath+"/blobs/"+digest, nil)
	resp, err := http.DefaultClient.Do(f.authed(delReq))
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status=%d, want 202", resp.StatusCode)
	}
	b, _ := f.blobs.Stat(context.Background(), digest)
	if b != nil {
		t.Fatalf("row still present after delete: %+v", b)
	}
	if !containsStr(f.audit.kinds(), string(audit.EvtOCIBlobDeleted)) {
		t.Fatalf("missing audit event; kinds=%v", f.audit.kinds())
	}
}

// TestBlobDelete_WhenReferenced_405 asserts that DELETE is rejected with
// 405 when ref_count > 0.
func TestBlobDelete_WhenReferenced_405(t *testing.T) {
	f := newBlobFixture(t)
	body := []byte("referenced")
	digest := f.pushMonolithic(body)

	// Manually bump ref_count via a writer tx to simulate a manifest
	// that references the blob (02-07 will do this naturally).
	err := f.db.WriteTx(context.Background(), func(tx *sql.Tx) error {
		return f.blobs.IncRef(context.Background(), tx, digest)
	})
	if err != nil {
		t.Fatal(err)
	}

	delReq, _ := http.NewRequest("DELETE", f.srv.URL+"/v2/"+f.repoPath+"/blobs/"+digest, nil)
	resp, err := http.DefaultClient.Do(f.authed(delReq))
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d, want 405; body=%s", resp.StatusCode, b)
	}
	// Row still present.
	b, _ := f.blobs.Stat(context.Background(), digest)
	if b == nil || b.RefCount != 1 {
		t.Fatalf("row mutated: %+v", b)
	}
}

// TestBlobUpload_DigestMismatch rejects the PUT with 400 DIGEST_INVALID
// when the claimed digest doesn't match the actual bytes.
func TestBlobUpload_DigestMismatch(t *testing.T) {
	f := newBlobFixture(t)

	// POST + PATCH "AAA", but PUT claims sha256 of "BBB".
	postReq, _ := http.NewRequest("POST", f.srv.URL+"/v2/"+f.repoPath+"/blobs/uploads/", nil)
	postResp, _ := http.DefaultClient.Do(f.authed(postReq))
	postResp.Body.Close()
	loc := postResp.Header.Get("Location")

	patchReq, _ := http.NewRequest("PATCH", f.srv.URL+loc, bytes.NewReader([]byte("AAA")))
	patchReq.ContentLength = 3
	patchReq.Header.Set("Content-Type", "application/octet-stream")
	patchResp, _ := http.DefaultClient.Do(f.authed(patchReq))
	patchResp.Body.Close()

	bogus := hexDigest([]byte("BBB"))
	putReq, _ := http.NewRequest("PUT", f.srv.URL+loc+"?digest="+bogus, nil)
	resp, err := http.DefaultClient.Do(f.authed(putReq))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("DIGEST_INVALID")) {
		t.Fatalf("expected DIGEST_INVALID in body: %s", body)
	}
}

// TestBlobUpload_OversizedChunk: small chunk cap → PATCH beyond cap → 413.
func TestBlobUpload_OversizedChunk(t *testing.T) {
	// Build a handler with a tiny chunk cap so the test body is tractable.
	db := sqlitetest.New(t)
	dataRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataRoot, "tmp", "uploads"), 0o750); err != nil {
		t.Fatal(err)
	}

	users := metadata.NewUsersRepo(db)
	repos := metadata.NewReposRepo(db)
	projects := metadata.NewProjectsRepo(db)
	members := metadata.NewMembersRepo(db)

	pwHash, _ := auth.HashPassword("correct-horse-battery-staple-42")
	uid, _ := users.Create(context.Background(), "pusher", "u@e.com", pwHash, false, false)
	pid, _ := projects.Create(context.Background(), "proj", "x")
	_, _ = repos.Create(context.Background(), pid, "docker", "app", "", nil, nil, nil)
	_ = members.Add(context.Background(), pid, uid)

	handler := oci.New(oci.Deps{
		DB:            db,
		Users:         users,
		APIKeys:       metadata.NewAPIKeysRepo(db),
		Repos:         repos,
		Projects:      projects,
		Sessions:      metadata.NewSessionsRepo(db),
		Members:       members,
		CAS:           storage.NewCAS(filepath.Join(dataRoot, "blobs")),
		Blobs:         metadata.NewDockerBlobsRepo(db),
		BlobUploads:   metadata.NewBlobUploadsRepo(db),
		Sess:          metadata.NewBlobUploadSessionsRepo(db),
		DataRoot:      dataRoot,
		HMACSecret:    []byte("0123456789abcdef0123456789abcdef"),
		JWTTTL:        time.Hour,
		ChunkMaxBytes: 16, // 16 bytes
	})
	r := chi.NewRouter()
	handler.Mount(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	// Mint token.
	tokReq, _ := http.NewRequest("GET", srv.URL+"/v2/token", nil)
	tokReq.Header.Set("Authorization", "Basic "+basicEncode("pusher:correct-horse-battery-staple-42"))
	tokResp, _ := http.DefaultClient.Do(tokReq)
	var payload struct {
		Token string `json:"token"`
	}
	_ = json.NewDecoder(tokResp.Body).Decode(&payload)
	tokResp.Body.Close()

	// POST.
	postReq, _ := http.NewRequest("POST", srv.URL+"/v2/proj/docker/app/blobs/uploads/", nil)
	postReq.Header.Set("Authorization", "Bearer "+payload.Token)
	postResp, _ := http.DefaultClient.Do(postReq)
	postResp.Body.Close()
	loc := postResp.Header.Get("Location")

	// PATCH 20 bytes — over the 16-byte cap.
	big := bytes.Repeat([]byte("X"), 20)
	patchReq, _ := http.NewRequest("PATCH", srv.URL+loc, bytes.NewReader(big))
	patchReq.ContentLength = int64(len(big))
	patchReq.Header.Set("Authorization", "Bearer "+payload.Token)
	patchReq.Header.Set("Content-Type", "application/octet-stream")
	resp, err := http.DefaultClient.Do(patchReq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d, want 413; body=%s", resp.StatusCode, b)
	}
}

// TestBlobMount_CrossRepoSameProject mounts a blob from one repo to
// another within the same project.
func TestBlobMount_CrossRepoSameProject(t *testing.T) {
	f := newBlobFixture(t)
	body := []byte("mountable")
	digest := f.pushMonolithic(body)

	// Seed a second docker repo in the same project.
	rid2, err := f.repos.Create(context.Background(), f.projectID, "docker", "app2", "", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = rid2

	// Mount from proj/docker/app into proj/docker/app2.
	url := f.srv.URL + "/v2/proj/docker/app2/blobs/uploads/?mount=" + digest + "&from=proj/docker/app"
	req, _ := http.NewRequest("POST", url, nil)
	resp, err := http.DefaultClient.Do(f.authed(req))
	if err != nil {
		t.Fatalf("mount POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d, want 201; body=%s", resp.StatusCode, b)
	}
	if got := resp.Header.Get("Docker-Content-Digest"); got != digest {
		t.Fatalf("digest header=%q", got)
	}
	wantLoc := "/v2/proj/docker/app2/blobs/" + digest
	if got := resp.Header.Get("Location"); got != wantLoc {
		t.Fatalf("Location=%q, want %q", got, wantLoc)
	}
	if !containsStr(f.audit.kinds(), string(audit.EvtOCIBlobMounted)) {
		t.Fatalf("missing mount audit event; kinds=%v", f.audit.kinds())
	}
}

// TestBlobMount_FallbackWhenBlobMissing returns 202 (normal upload start)
// when the blob isn't in CAS.
func TestBlobMount_FallbackWhenBlobMissing(t *testing.T) {
	f := newBlobFixture(t)
	missing := "sha256:" + strings.Repeat("c", 64)
	// Seed a second repo.
	_, err := f.repos.Create(context.Background(), f.projectID, "docker", "app2", "", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	url := f.srv.URL + "/v2/proj/docker/app2/blobs/uploads/?mount=" + missing + "&from=proj/docker/app"
	req, _ := http.NewRequest("POST", url, nil)
	resp, err := http.DefaultClient.Do(f.authed(req))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status=%d, want 202 (fallback to upload)", resp.StatusCode)
	}
	if resp.Header.Get("Range") != "0-0" {
		t.Fatalf("missing Range header on fallback")
	}
}

// TestBlobMount_FallbackWhenSourceProjectMissing covers the spec §4.2.1
// contract that an unsatisfiable mount must fall through to a normal
// upload session start (202). Docker CLI emits `from=library/alpine`
// when pushing a retagged docker.io/alpine; without fall-through the
// whole push errors out on 404 NAME_UNKNOWN before any blob upload
// starts.
func TestBlobMount_FallbackWhenSourceProjectMissing(t *testing.T) {
	f := newBlobFixture(t)
	missing := "sha256:" + strings.Repeat("d", 64)
	url := f.srv.URL + "/v2/proj/docker/app/blobs/uploads/?mount=" +
		missing + "&from=library/alpine"
	req, _ := http.NewRequest("POST", url, nil)
	resp, err := http.DefaultClient.Do(f.authed(req))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d, want 202 (fallback when from-project missing); body=%s",
			resp.StatusCode, body)
	}
	if resp.Header.Get("Range") != "0-0" {
		t.Fatalf("missing Range header on fallback")
	}
	if resp.Header.Get("Docker-Upload-Uuid") == "" {
		t.Fatalf("missing Docker-Upload-Uuid on fallback")
	}
}

// TestBlobMount_FallbackWhenSourceRepoMissing covers the same fall-through
// contract for the case where the from-project exists locally but the
// named repo within it doesn't.
func TestBlobMount_FallbackWhenSourceRepoMissing(t *testing.T) {
	f := newBlobFixture(t)
	missing := "sha256:" + strings.Repeat("e", 64)
	// Source uses f.projectID's project but a repo that doesn't exist.
	url := f.srv.URL + "/v2/proj/docker/app/blobs/uploads/?mount=" +
		missing + "&from=proj/docker/nosuch"
	req, _ := http.NewRequest("POST", url, nil)
	resp, err := http.DefaultClient.Do(f.authed(req))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d, want 202 (fallback when from-repo missing); body=%s",
			resp.StatusCode, body)
	}
}

// TestBlobMount_ForbiddenSourceRead_StillFallsBack covers Codex review
// 2026-04-21: when the from-source repo EXISTS but the actor lacks read
// access on it, the mount path must NOT return 201 Created (which would
// allow cross-repo data leakage). The current implementation enforces
// the source-read auth check; if it ever flipped to a successful mount
// the actor would have copied bytes they aren't allowed to see. We
// expect 403 DENIED on the source-read check, matching pre-fix behavior.
func TestBlobMount_ForbiddenSourceRead_StillFallsBack(t *testing.T) {
	f := newBlobFixture(t)
	body := []byte("guarded-blob")
	digest := f.pushMonolithic(body)

	// Make the project private (default), so non-member can't read.
	// Seed a stranger user who is not a member of the project.
	pwHash, _ := auth.HashPassword("stranger-pw")
	_, err := f.users.Create(context.Background(), "noread", "n@e.com", pwHash, false, false)
	if err != nil {
		t.Fatal(err)
	}
	tokReq, _ := http.NewRequest("GET", f.srv.URL+"/v2/token", nil)
	tokReq.Header.Set("Authorization", "Basic "+basicEncode("noread:stranger-pw"))
	tokResp, _ := http.DefaultClient.Do(tokReq)
	var payload struct {
		Token string `json:"token"`
	}
	_ = json.NewDecoder(tokResp.Body).Decode(&payload)
	tokResp.Body.Close()
	if payload.Token == "" {
		t.Fatal("no token for stranger")
	}

	// Seed a SECOND project the stranger does belong to so the dest-write
	// auth check passes — that way we are testing the source-read gate
	// specifically, not the dest-write gate.
	otherPID, err := f.projects.Create(context.Background(), "stranger-proj", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.repos.Create(context.Background(), otherPID,
		"docker", "app", "", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	// Find stranger's user id and join them to stranger-proj.
	su, err := f.users.FindByLogin(context.Background(), "noread")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.members.Add(context.Background(), otherPID, su.ID); err != nil {
		t.Fatal(err)
	}

	url := f.srv.URL + "/v2/stranger-proj/docker/app/blobs/uploads/?mount=" +
		digest + "&from=" + f.repoPath
	mountReq, _ := http.NewRequest("POST", url, nil)
	mountReq.Header.Set("Authorization", "Bearer "+payload.Token)
	resp, err := http.DefaultClient.Do(mountReq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d, want 403 DENIED on source-read; body=%s", resp.StatusCode, b)
	}
}

// TestBlobUpload_ForbiddenActor asserts a non-member user is rejected with
// 403 DENIED on POST /v2/...blobs/uploads/.
func TestBlobUpload_ForbiddenActor(t *testing.T) {
	f := newBlobFixture(t)

	// Seed a SECOND user who is NOT a member of the project.
	pwHash, _ := auth.HashPassword("another-pw")
	uid2, err := f.users.Create(context.Background(), "stranger", "s@e.com", pwHash, false, false)
	if err != nil {
		t.Fatal(err)
	}
	_ = uid2

	// Mint that user's token.
	req, _ := http.NewRequest("GET", f.srv.URL+"/v2/token", nil)
	req.Header.Set("Authorization", "Basic "+basicEncode("stranger:another-pw"))
	resp, _ := http.DefaultClient.Do(req)
	var payload struct {
		Token string `json:"token"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&payload)
	resp.Body.Close()
	if payload.Token == "" {
		t.Fatal("no token for stranger")
	}

	// POST new upload as stranger.
	postReq, _ := http.NewRequest("POST", f.srv.URL+"/v2/"+f.repoPath+"/blobs/uploads/", nil)
	postReq.Header.Set("Authorization", "Bearer "+payload.Token)
	postResp, err := http.DefaultClient.Do(postReq)
	if err != nil {
		t.Fatal(err)
	}
	defer postResp.Body.Close()
	if postResp.StatusCode != http.StatusForbidden {
		b, _ := io.ReadAll(postResp.Body)
		t.Fatalf("status=%d, want 403; body=%s", postResp.StatusCode, b)
	}
}

// TestBlobUploadSurvivesConcurrentGC is the SCAN-12 regression gate.
// Sequence:
//   1. PATCH an upload fully (tmp file exists, no CAS yet).
//   2. Simulate a GC run that reads the blob_uploads exclusion set and
//      then blocks before sweeping — we coordinate via channels.
//   3. PUT the upload. Inside PUT, blob_uploads.Start happens BEFORE
//      cas.PutFromPath. So if the simulated GC ran BEFORE PUT started,
//      the blob_uploads row wasn't in its snapshot, but the CAS file
//      ALSO wasn't there — GC would have nothing to delete. If the
//      simulated GC runs AFTER PUT started, the blob_uploads row IS in
//      its snapshot and GC skips the digest.
//
// Either ordering preserves the blob. The test drives the harder case:
// GC takes its snapshot, then PUT runs to completion, then GC tries to
// delete — and must not find the blob eligible.
func TestBlobUploadSurvivesConcurrentGC(t *testing.T) {
	f := newBlobFixture(t)

	// 1. POST + PATCH a full blob without PUT.
	body := bytes.Repeat([]byte("R"), 512)
	digest := hexDigest(body)

	postReq, _ := http.NewRequest("POST", f.srv.URL+"/v2/"+f.repoPath+"/blobs/uploads/", nil)
	postResp, _ := http.DefaultClient.Do(f.authed(postReq))
	postResp.Body.Close()
	loc := postResp.Header.Get("Location")

	patchReq, _ := http.NewRequest("PATCH", f.srv.URL+loc, bytes.NewReader(body))
	patchReq.ContentLength = int64(len(body))
	patchReq.Header.Set("Content-Type", "application/octet-stream")
	patchResp, _ := http.DefaultClient.Do(f.authed(patchReq))
	patchResp.Body.Close()

	// 2. Simulate GC worker running in background. GC will:
	//    - read blob_uploads exclusion set (snapshot)
	//    - compute GC candidates from docker_blobs (ref_count=0 & quiesced)
	//    - wait for the PUT to finish via <-putDone
	//    - delete any candidate NOT in its snapshot
	gcStarted := make(chan struct{})
	putDone := make(chan struct{})
	gcResult := make(chan error, 1)
	go func() {
		ctx := context.Background()
		// Snapshot: digests currently in blob_uploads.
		var snapshot map[string]struct{}
		rows, err := f.db.Reader.QueryContext(ctx, `SELECT digest FROM blob_uploads`)
		if err != nil {
			gcResult <- err
			return
		}
		snapshot = make(map[string]struct{})
		for rows.Next() {
			var d string
			if err := rows.Scan(&d); err != nil {
				_ = rows.Close()
				gcResult <- err
				return
			}
			snapshot[d] = struct{}{}
		}
		_ = rows.Close()

		close(gcStarted)
		<-putDone

		// GC candidates: iterate docker_blobs, delete any not in snapshot
		// with ref_count=0. Use GCCandidates with zero quiescence so the
		// just-promoted blob is in the candidate set if the SCAN-12
		// ordering is broken.
		cands, err := f.blobs.GCCandidates(ctx, 0)
		if err != nil {
			gcResult <- err
			return
		}
		for _, c := range cands {
			if _, excluded := snapshot[c.Digest]; excluded {
				continue
			}
			// Would-be-deleted: CAS file + row. We don't actually delete
			// in this test — we only assert that the just-PUTd digest
			// is EXCLUDED from the candidate set.
			if c.Digest == digest {
				gcResult <- fmt.Errorf("GC would delete digest %s (blob_uploads snapshot missed it)", digest)
				return
			}
		}
		gcResult <- nil
	}()

	// Wait for GC worker to reach the point where it's done snapshotting.
	<-gcStarted

	// 3. Run PUT.
	putReq, _ := http.NewRequest("PUT", f.srv.URL+loc+"?digest="+digest, nil)
	putResp, err := http.DefaultClient.Do(f.authed(putReq))
	if err != nil {
		t.Fatal(err)
	}
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT status=%d", putResp.StatusCode)
	}

	close(putDone)

	// 4. GC must not have flagged our blob.
	if err := <-gcResult; err != nil {
		t.Fatalf("SCAN-12 regression: %v", err)
	}

	// 5. Blob is in CAS + docker_blobs row exists.
	b, _ := f.blobs.Stat(context.Background(), digest)
	if b == nil {
		t.Fatalf("docker_blobs row missing after survival test")
	}
	hx := digest[len("sha256:"):]
	casPath := filepath.Join(f.dataRoot, "blobs", "sha256", hx[:2], hx)
	if _, err := os.Stat(casPath); err != nil {
		t.Fatalf("CAS file missing: %v", err)
	}

	// 6. Sanity: at the source level, the handler calls blobUploads.Start
	// BEFORE cas.PutFromPath. Grep proves the ordering invariant holds.
	src, err := os.ReadFile("blobs.go")
	if err != nil {
		t.Fatal(err)
	}
	startIdx := bytes.Index(src, []byte("h.blobUploads.Start"))
	casIdx := bytes.Index(src, []byte("h.cas.PutFromPath(r.Context(), tmpPath)"))
	if startIdx < 0 || casIdx < 0 || startIdx > casIdx {
		t.Fatalf("SCAN-12 ordering violated in source: Start at %d, cas at %d", startIdx, casIdx)
	}
}

// TestBlobUpload_UnknownRepo returns 404 NAME_UNKNOWN.
func TestBlobUpload_UnknownRepo(t *testing.T) {
	f := newBlobFixture(t)
	req, _ := http.NewRequest("POST", f.srv.URL+"/v2/proj/docker/does-not-exist/blobs/uploads/", nil)
	resp, err := http.DefaultClient.Do(f.authed(req))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("NAME_UNKNOWN")) {
		t.Fatalf("expected NAME_UNKNOWN: %s", body)
	}
}

// TestBlobUpload_NonDockerTypeRejected returns 400 NAME_INVALID.
func TestBlobUpload_NonDockerTypeRejected(t *testing.T) {
	f := newBlobFixture(t)
	// Seed a raw repo.
	_, err := f.repos.Create(context.Background(), f.projectID, "raw", "weird", "", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("POST", f.srv.URL+"/v2/proj/raw/weird/blobs/uploads/", nil)
	resp, err := http.DefaultClient.Do(f.authed(req))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", resp.StatusCode)
	}
}

// ---- helpers below ----

// pushMonolithic uploads body via the monolithic POST path and returns its
// digest. Fatals on error.
func (f *blobFixture) pushMonolithic(body []byte) string {
	f.t.Helper()
	digest := hexDigest(body)
	url := f.srv.URL + "/v2/" + f.repoPath + "/blobs/uploads/?digest=" + digest
	req, _ := http.NewRequest("POST", url, bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := http.DefaultClient.Do(f.authed(req))
	if err != nil {
		f.t.Fatalf("push monolithic: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		f.t.Fatalf("push monolithic status=%d; body=%s", resp.StatusCode, b)
	}
	return digest
}

func containsStr(s []string, want string) bool {
	for _, x := range s {
		if x == want {
			return true
		}
	}
	return false
}

// TestBlobUpload_MalformedUUIDRejected is the WR-02 regression. chi's default
// {uuid} regex is a greedy [^/]+ match, so without the isUploadUUID check any
// non-UUID string (including traversal-flavored payloads) gets interpolated
// into filesystem paths. The handler must reject malformed UUIDs with 400
// BLOB_UPLOAD_INVALID before touching sess.Lookup or the tmp path.
func TestBlobUpload_MalformedUUIDRejected(t *testing.T) {
	f := newBlobFixture(t)

	// Sanity: a real POST yields a valid UUID Location.
	postReq, _ := http.NewRequest("POST", f.srv.URL+"/v2/"+f.repoPath+"/blobs/uploads/", nil)
	postResp, err := http.DefaultClient.Do(f.authed(postReq))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	postResp.Body.Close()

	// Traversal-flavored strings that survive chi's URL normalization land
	// as the {uuid} chi param — not a UUID, must be rejected.
	malformed := []string{
		"not-a-uuid",
		"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee-toolong",
		"xx",
		"%2E%2E", // encoded ".." — chi leaves as-is in a single segment
	}
	for _, bad := range malformed {
		url := f.srv.URL + "/v2/" + f.repoPath + "/blobs/uploads/" + bad

		// PATCH
		patchReq, _ := http.NewRequest("PATCH", url, bytes.NewReader([]byte("x")))
		patchResp, err := http.DefaultClient.Do(f.authed(patchReq))
		if err != nil {
			t.Fatalf("PATCH %q: %v", bad, err)
		}
		body, _ := io.ReadAll(patchResp.Body)
		patchResp.Body.Close()
		if patchResp.StatusCode != http.StatusBadRequest {
			t.Fatalf("PATCH %q status=%d want 400 body=%s", bad, patchResp.StatusCode, body)
		}
		if !strings.Contains(string(body), "BLOB_UPLOAD_INVALID") {
			t.Fatalf("PATCH %q body missing BLOB_UPLOAD_INVALID: %s", bad, body)
		}

		// PUT
		putReq, _ := http.NewRequest("PUT", url+"?digest=sha256:"+strings.Repeat("a", 64), nil)
		putResp, err := http.DefaultClient.Do(f.authed(putReq))
		if err != nil {
			t.Fatalf("PUT %q: %v", bad, err)
		}
		body, _ = io.ReadAll(putResp.Body)
		putResp.Body.Close()
		if putResp.StatusCode != http.StatusBadRequest {
			t.Fatalf("PUT %q status=%d want 400 body=%s", bad, putResp.StatusCode, body)
		}

		// GET status
		getReq, _ := http.NewRequest("GET", url, nil)
		getResp, err := http.DefaultClient.Do(f.authed(getReq))
		if err != nil {
			t.Fatalf("GET %q: %v", bad, err)
		}
		getResp.Body.Close()
		if getResp.StatusCode != http.StatusBadRequest {
			t.Fatalf("GET status %q status=%d want 400", bad, getResp.StatusCode)
		}
	}
}

// --------------------------------------------------------------------------
// Phase 8 Plan 01 (MIRROR-03) — MirrorGuard rejects OCI blob POST/PUT on
// mirror-flagged repos.
// --------------------------------------------------------------------------

func TestOCIBlobPut_MirrorRepoReturns403(t *testing.T) {
	f := newBlobFixture(t)
	if err := f.db.WriteTx(context.Background(), func(tx *sql.Tx) error {
		return f.repos.SetMirrorConfigInTx(context.Background(), tx, f.repoID, metadata.MirrorConfig{
			IsMirror:    true,
			UpstreamURL: "https://registry-1.docker.io",
			FilterJSON:  `{}`,
			CredID:      nil,
			ScanOnSync:  false,
		})
	}); err != nil {
		t.Fatalf("set mirror cfg: %v", err)
	}
	// Monolithic POST upload.
	body := []byte("mirror-rejected-blob")
	digest := hexDigest(body)
	url := f.srv.URL + "/v2/" + f.repoPath + "/blobs/uploads/?digest=" + digest
	req, _ := http.NewRequest("POST", url, bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := http.DefaultClient.Do(f.authed(req))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 403; body=%s", resp.StatusCode, b)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "repo_is_mirror") {
		t.Fatalf("body missing repo_is_mirror: %s", b)
	}
}

func TestOCIBlobPut_NonMirrorStillWorks(t *testing.T) {
	f := newBlobFixture(t)
	body := []byte("plain-blob")
	digest := hexDigest(body)
	url := f.srv.URL + "/v2/" + f.repoPath + "/blobs/uploads/?digest=" + digest
	req, _ := http.NewRequest("POST", url, bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := http.DefaultClient.Do(f.authed(req))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 201 (non-mirror); body=%s", resp.StatusCode, b)
	}
}
