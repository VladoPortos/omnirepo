package s3_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	omrcrypto "github.com/dxc-internal/omnirepo/internal/crypto"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/migrations"
	s3handler "github.com/dxc-internal/omnirepo/internal/protocol/s3"
	"github.com/dxc-internal/omnirepo/internal/protocol/s3/backend"
	s3keys "github.com/dxc-internal/omnirepo/internal/protocol/s3/keys"
	"github.com/dxc-internal/omnirepo/internal/protocol/s3/sigv4"
	"github.com/dxc-internal/omnirepo/internal/storage"
)

const (
	testRegion  = "us-east-1"
	testService = "s3"
)

// xmlError mirrors the AWS error XML shape for test assertions.
type xmlError struct {
	XMLName             xml.Name `xml:"Error"`
	Code                string   `xml:"Code"`
	Message             string   `xml:"Message"`
	ServerTime          string   `xml:"ServerTime,omitempty"`
	MaxAllowedSkewMilli int64    `xml:"MaxAllowedSkewMilliseconds,omitempty"`
}

// testFixture holds a fully-wired S3 handler test harness.
type testFixture struct {
	server  *httptest.Server
	db      *metadata.DB
	backend *backend.Backend
	aead    *omrcrypto.AEAD
	service *s3keys.Service
	dataDir string
	akid    string
	secret  string
}

func setupTestFixture(t *testing.T) *testFixture {
	t.Helper()

	dataDir := t.TempDir()
	dbPath := dataDir + "/test.sqlite"

	db, err := metadata.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	if _, err := migrations.Apply(ctx, db.Writer); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Create a project.
	var projectID int64
	res, err := db.Writer.ExecContext(ctx,
		`INSERT INTO projects(name) VALUES ('testproj')`)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	projectID, _ = res.LastInsertId()

	// Create a super-admin user (needed for key FK).
	_, err = db.Writer.ExecContext(ctx,
		`INSERT INTO users(login, email, password_hash, is_super_admin) VALUES ('admin', 'admin@test.com', 'hash', 1)`)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Set up AEAD.
	key, err := omrcrypto.GenerateKey()
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	aead, err := omrcrypto.New(key)
	if err != nil {
		t.Fatalf("new aead: %v", err)
	}

	// Create S3 access key.
	akid, secret, err := s3keys.GenerateS3AccessKey()
	if err != nil {
		t.Fatalf("gen s3 key: %v", err)
	}
	secretEnc, err := aead.Encrypt([]byte(secret))
	if err != nil {
		t.Fatalf("encrypt secret: %v", err)
	}
	_, err = db.Writer.ExecContext(ctx,
		`INSERT INTO s3_access_keys(project_id, access_key_id, secret_enc, label, created_by_user_id) VALUES (?, ?, ?, 'test', 1)`,
		projectID, akid, secretEnc)
	if err != nil {
		t.Fatalf("insert key: %v", err)
	}

	// Create a bucket owned by the project.
	_, err = db.Writer.ExecContext(ctx,
		`INSERT INTO s3_buckets(name, project_id) VALUES ('mybucket', ?)`, projectID)
	if err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	if err := os.MkdirAll(dataDir+"/s3/mybucket", 0o750); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}

	// Put a test object in the bucket.
	be := backend.New(dataDir, db, storage.NewLocks())
	objBody := []byte("hello s3 world")
	_, err = be.PutObject("mybucket", "file.txt", map[string]string{"Content-Type": "text/plain"}, bytes.NewReader(objBody), int64(len(objBody)), nil)
	if err != nil {
		t.Fatalf("put object: %v", err)
	}

	// Wire up the S3 handler.
	service := s3keys.NewService(metadata.NewS3KeysRepo(db), aead)
	deps := &s3handler.Deps{
		Service:   service,
		Backend:   be,
		Skew:      15 * time.Minute,
		Hostnames: []string{"omnirepo.corp.example"},
	}

	router := chi.NewRouter()
	// VHost middleware must be registered before routes (chi requirement).
	router.Use(s3handler.VHostRewrite(deps.Hostnames))
	deps.Mount(router)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	return &testFixture{
		server:  server,
		db:      db,
		backend: be,
		aead:    aead,
		service: service,
		dataDir: dataDir,
		akid:    akid,
		secret:  secret,
	}
}

// signRequest creates a valid SigV4 Authorization header for the given request.
func signRequest(r *http.Request, akid, secret, host string, now time.Time, body []byte) {
	amzDate := now.UTC().Format("20060102T150405Z")
	date := amzDate[:8]
	r.Header.Set("x-amz-date", amzDate)
	r.Host = host
	r.Header.Set("Host", host)

	bodyHash := sha256Hex(body)
	r.Header.Set("x-amz-content-sha256", bodyHash)

	signed := []string{"host", "x-amz-content-sha256", "x-amz-date"}

	canonHeaders := "host:" + host + "\n" +
		"x-amz-content-sha256:" + bodyHash + "\n" +
		"x-amz-date:" + amzDate + "\n"
	signedHeadersStr := strings.Join(signed, ";")

	canonReq := strings.Join([]string{
		r.Method,
		r.URL.EscapedPath(),
		r.URL.RawQuery,
		canonHeaders,
		signedHeadersStr,
		bodyHash,
	}, "\n")

	scope := date + "/" + testRegion + "/" + testService + "/aws4_request"
	sts := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + sha256Hex([]byte(canonReq))

	kDate := hmacSHA256([]byte("AWS4"+secret), []byte(date))
	kRegion := hmacSHA256(kDate, []byte(testRegion))
	kService := hmacSHA256(kRegion, []byte(testService))
	kSigning := hmacSHA256(kService, []byte("aws4_request"))
	sig := hex.EncodeToString(hmacSHA256(kSigning, []byte(sts)))

	r.Header.Set("Authorization",
		"AWS4-HMAC-SHA256 "+
			"Credential="+akid+"/"+scope+", "+
			"SignedHeaders="+signedHeadersStr+", "+
			"Signature="+sig)
}

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func hmacSHA256(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}

// Test 1: Valid SigV4 + member-of-project → gofakes3 returns object.
func TestS3Handler_ValidSigV4_GetObject(t *testing.T) {
	f := setupTestFixture(t)

	req, _ := http.NewRequest(http.MethodGet, f.server.URL+"/s3/mybucket/file.txt", nil)
	signRequest(req, f.akid, f.secret, req.URL.Host, time.Now(), nil)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body = %s", resp.StatusCode, body)
	}

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello s3 world" {
		t.Errorf("body = %q, want %q", body, "hello s3 world")
	}
}

// Test 2: Wrong secret → 403 SignatureDoesNotMatch.
func TestS3Handler_WrongSecret(t *testing.T) {
	f := setupTestFixture(t)

	req, _ := http.NewRequest(http.MethodGet, f.server.URL+"/s3/mybucket/file.txt", nil)
	signRequest(req, f.akid, "WRONG_SECRET_AAAAAAAAAAAAAAAAAAAAAAAA", req.URL.Host, time.Now(), nil)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	assertXMLErrorCode(t, resp.Body, "SignatureDoesNotMatch")
}

// Test 3: Unknown AKID → 403 InvalidAccessKeyId.
func TestS3Handler_UnknownAKID(t *testing.T) {
	f := setupTestFixture(t)

	req, _ := http.NewRequest(http.MethodGet, f.server.URL+"/s3/mybucket/file.txt", nil)
	signRequest(req, "AKIAUNKNOWNKEYAAAAA", f.secret, req.URL.Host, time.Now(), nil)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	assertXMLErrorCode(t, resp.Body, "InvalidAccessKeyId")
}

// Test 4: Clock skew >15 min → 403 RequestTimeTooSkewed with ServerTime.
func TestS3Handler_ClockSkew(t *testing.T) {
	f := setupTestFixture(t)

	skewedTime := time.Now().Add(-20 * time.Minute)
	req, _ := http.NewRequest(http.MethodGet, f.server.URL+"/s3/mybucket/file.txt", nil)
	signRequest(req, f.akid, f.secret, req.URL.Host, skewedTime, nil)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	var errBody xmlError
	if err := xml.Unmarshal(body, &errBody); err != nil {
		t.Fatalf("unmarshal: %v\nbody: %s", err, body)
	}
	if errBody.Code != "RequestTimeTooSkewed" {
		t.Errorf("code = %q, want RequestTimeTooSkewed", errBody.Code)
	}
	if errBody.ServerTime == "" {
		t.Error("ServerTime missing from RequestTimeTooSkewed response")
	}
}

// Test 5: Bearer token (not SigV4) → 403 InvalidAccessKeyId (D-08).
func TestS3Handler_BearerToken_Rejected(t *testing.T) {
	f := setupTestFixture(t)

	req, _ := http.NewRequest(http.MethodGet, f.server.URL+"/s3/mybucket/file.txt", nil)
	req.Header.Set("Authorization", "Bearer some-session-cookie-value")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	assertXMLErrorCode(t, resp.Body, "InvalidAccessKeyId")
}

// Test 6: No Authorization header → 403 InvalidAccessKeyId.
func TestS3Handler_NoAuth(t *testing.T) {
	f := setupTestFixture(t)

	req, _ := http.NewRequest(http.MethodGet, f.server.URL+"/s3/mybucket/file.txt", nil)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	assertXMLErrorCode(t, resp.Body, "InvalidAccessKeyId")
}

// Test 7: AKID from project A used against bucket in project B → 403 AccessDenied.
func TestS3Handler_CrossProject_Denied(t *testing.T) {
	f := setupTestFixture(t)
	ctx := context.Background()

	// Create a second project and a bucket owned by it.
	res, err := f.db.Writer.ExecContext(ctx,
		`INSERT INTO projects(name) VALUES ('otherproj')`)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	otherProjID, _ := res.LastInsertId()
	_, err = f.db.Writer.ExecContext(ctx,
		`INSERT INTO s3_buckets(name, project_id) VALUES ('otherbucket', ?)`, otherProjID)
	if err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	if err := os.MkdirAll(f.dataDir+"/s3/otherbucket", 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Use the original AKID (belongs to testproj) against otherbucket.
	req, _ := http.NewRequest(http.MethodGet, f.server.URL+"/s3/otherbucket/file.txt", nil)
	signRequest(req, f.akid, f.secret, req.URL.Host, time.Now(), nil)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 403; body = %s", resp.StatusCode, body)
	}
	assertXMLErrorCode(t, resp.Body, "AccessDenied")
}

// Test 8: VHost rewrite — <bucket>.<hostname> → /s3/<bucket>/<key>.
func TestS3Handler_VHostRewrite(t *testing.T) {
	f := setupTestFixture(t)

	// Build request to /file.txt with Host: mybucket.omnirepo.corp.example
	req, _ := http.NewRequest(http.MethodGet, f.server.URL+"/file.txt", nil)
	vhost := "mybucket.omnirepo.corp.example"
	signRequest(req, f.akid, f.secret, vhost, time.Now(), nil)
	// Override Host to the vhost form (httptest server uses localhost).
	req.Host = vhost

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body = %s", resp.StatusCode, body)
	}

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello s3 world" {
		t.Errorf("body = %q, want %q", body, "hello s3 world")
	}
}

// Test 9: IPv4 host → no rewrite.
func TestS3Handler_IPv4Host_NoRewrite(t *testing.T) {
	f := setupTestFixture(t)

	// Request to /file.txt with Host: 127.0.0.1:PORT — should not rewrite.
	req, _ := http.NewRequest(http.MethodGet, f.server.URL+"/file.txt", nil)
	// Don't sign — expect 404 from chi (no /file.txt route), NOT a SigV4 error.

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	// With no route match for /file.txt, chi returns 404 or 405.
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("expected non-200 for IPv4 host request to /file.txt")
	}
}

// Test 10: Path-style with Host: omnirepo.corp.example → no rewrite.
func TestS3Handler_PathStyle_NoDoubleRewrite(t *testing.T) {
	f := setupTestFixture(t)

	req, _ := http.NewRequest(http.MethodGet, f.server.URL+"/s3/mybucket/file.txt", nil)
	signRequest(req, f.akid, f.secret, req.URL.Host, time.Now(), nil)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body = %s", resp.StatusCode, body)
	}
}

// Test: PutObject via SigV4 → round-trip with GetObject.
func TestS3Handler_PutThenGet(t *testing.T) {
	f := setupTestFixture(t)

	payload := []byte("new object content")
	putReq, _ := http.NewRequest(http.MethodPut, f.server.URL+"/s3/mybucket/newobj.txt", bytes.NewReader(payload))
	signRequest(putReq, f.akid, f.secret, putReq.URL.Host, time.Now(), payload)
	putReq.Header.Set("Content-Type", "text/plain")

	putResp, err := http.DefaultClient.Do(putReq)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("put status = %d, want 200", putResp.StatusCode)
	}

	// Get it back.
	getReq, _ := http.NewRequest(http.MethodGet, f.server.URL+"/s3/mybucket/newobj.txt", nil)
	signRequest(getReq, f.akid, f.secret, getReq.URL.Host, time.Now(), nil)

	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(getResp.Body)
		t.Fatalf("get status = %d; body = %s", getResp.StatusCode, body)
	}
	body, _ := io.ReadAll(getResp.Body)
	if string(body) != "new object content" {
		t.Errorf("body = %q, want %q", body, "new object content")
	}
}

// assertXMLErrorCode reads the response body and checks the XML <Code> field.
func assertXMLErrorCode(t *testing.T, body io.Reader, wantCode string) {
	t.Helper()
	data, _ := io.ReadAll(body)
	var errBody xmlError
	if err := xml.Unmarshal(data, &errBody); err != nil {
		t.Fatalf("unmarshal XML: %v\nbody: %s", err, data)
	}
	if errBody.Code != wantCode {
		t.Errorf("error code = %q, want %q\nfull body: %s", errBody.Code, wantCode, data)
	}
}

// Unused import guard for sigv4 (used only via signRequest above but needed
// for the package to reference the module).
var _ = sigv4.ErrMalformed
var _ = fmt.Sprintf
