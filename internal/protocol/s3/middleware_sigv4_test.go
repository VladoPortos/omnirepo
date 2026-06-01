package s3_test

// SigV4 middleware end-to-end tests:
//   - Actor.S3KeyID is populated from the resolved s3_access_keys.id.
//   - PayloadSHAFromContext returns the declared SHA in hex / UNSIGNED /
//     STREAMING modes.
//   - On non-S3 routes (no SigV4 middleware in chain) the ctx helper
//     returns ("", false).

import (
	"context"
	"database/sql"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vladoportos/omnirepo/internal/auth"
	omrcrypto "github.com/vladoportos/omnirepo/internal/crypto"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
	s3 "github.com/vladoportos/omnirepo/internal/protocol/s3"
	s3keys "github.com/vladoportos/omnirepo/internal/protocol/s3/keys"
)

// mwTestEnv carries a fully-wired SigV4 middleware fixture with a real DB,
// AEAD, and a single seeded S3 access key.
type mwTestEnv struct {
	service   *s3keys.Service
	akid      string
	secret    string
	s3KeyID   int64
	projectID int64
}

func newMwEnv(t *testing.T) *mwTestEnv {
	t.Helper()
	db := sqlitetest.New(t)

	key, err := omrcrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	aead, err := omrcrypto.New(key)
	if err != nil {
		t.Fatal(err)
	}
	repo := metadata.NewS3KeysRepo(db)
	service := s3keys.NewService(repo, aead)

	// Seed project + user + S3 access key in one tx.
	akid, secret, err := s3keys.GenerateS3AccessKey()
	if err != nil {
		t.Fatal(err)
	}
	enc, err := aead.Encrypt([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}

	var projectID, s3KeyID int64
	ctx := context.Background()
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO users(login, email, password_hash) VALUES ('s3mw', 's3mw@test', 'x')`)
		if err != nil {
			return err
		}
		uid, _ := res.LastInsertId()
		res, err = tx.ExecContext(ctx,
			`INSERT INTO projects(name) VALUES ('mwproj')`)
		if err != nil {
			return err
		}
		projectID, _ = res.LastInsertId()
		s3KeyID, err = repo.Insert(ctx, tx, &metadata.S3AccessKey{
			ProjectID:       projectID,
			AccessKeyID:     akid,
			SecretEnc:       []byte(enc),
			Label:           "mw-test",
			CreatedByUserID: uid,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	return &mwTestEnv{
		service:   service,
		akid:      akid,
		secret:    secret,
		s3KeyID:   s3KeyID,
		projectID: projectID,
	}
}

// signSigV4Hex builds a SigV4-signed request whose payload-hash slot is
// hex(sha256(body)).
func signSigV4Hex(t *testing.T, method, target, host string, body []byte, akid, secret string, now time.Time) *http.Request {
	t.Helper()
	r := httptest.NewRequest(method, target, strings.NewReader(string(body)))
	bodyHash := sha256Hex(body)
	signSigV4(t, r, host, akid, secret, now, bodyHash)
	return r
}

// signSigV4Sentinel builds a SigV4-signed request whose payload-hash slot
// is the literal sentinel string (UNSIGNED-PAYLOAD or STREAMING-...).
func signSigV4Sentinel(t *testing.T, method, target, host string, akid, secret, sentinel string, now time.Time) *http.Request {
	t.Helper()
	r := httptest.NewRequest(method, target, nil)
	signSigV4(t, r, host, akid, secret, now, sentinel)
	return r
}

func signSigV4(t *testing.T, r *http.Request, host, akid, secret string, now time.Time, payloadHash string) {
	t.Helper()
	const region = "us-east-1"
	const svc = "s3"
	amzDate := now.UTC().Format("20060102T150405Z")
	date := amzDate[:8]
	r.Host = host
	r.Header.Set("Host", host)
	r.Header.Set("x-amz-date", amzDate)
	r.Header.Set("x-amz-content-sha256", payloadHash)
	signed := []string{"host", "x-amz-content-sha256", "x-amz-date"}
	canonHeaders := "host:" + host + "\n" +
		"x-amz-content-sha256:" + payloadHash + "\n" +
		"x-amz-date:" + amzDate + "\n"
	canonReq := strings.Join([]string{
		r.Method, r.URL.EscapedPath(), r.URL.RawQuery,
		canonHeaders, strings.Join(signed, ";"), payloadHash,
	}, "\n")
	scope := date + "/" + region + "/" + svc + "/aws4_request"
	sts := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + sha256Hex([]byte(canonReq))
	kDate := hmacSHA256([]byte("AWS4"+secret), []byte(date))
	kRegion := hmacSHA256(kDate, []byte(region))
	kSvc := hmacSHA256(kRegion, []byte(svc))
	kSigning := hmacSHA256(kSvc, []byte("aws4_request"))
	sig := hex.EncodeToString(hmacSHA256(kSigning, []byte(sts)))
	r.Header.Set("Authorization",
		"AWS4-HMAC-SHA256 "+
			"Credential="+akid+"/"+scope+", "+
			"SignedHeaders="+strings.Join(signed, ";")+", "+
			"Signature="+sig)
}

// (sha256Hex / hmacSHA256 helpers are declared in handler_test.go — same
// package s3_test, shared.)

// TestSigV4Middleware_PopulatesActorS3KeyID asserts the resolved
// s3_access_keys.id flows through to auth.Actor.S3KeyID after a successful
// verify (prerequisite for multipart attribution).
func TestSigV4Middleware_PopulatesActorS3KeyID(t *testing.T) {
	env := newMwEnv(t)
	const host = "bucket.s3.example.com"
	now := time.Now().UTC()

	var captured auth.Actor
	var captureOK bool
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, captureOK = auth.ActorFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	mw := s3.SigV4Middleware(env.service, 15*time.Minute)(inner)
	req := signSigV4Hex(t, http.MethodGet, "/s3/mybucket/file.txt", host, nil, env.akid, env.secret, now)
	w := httptest.NewRecorder()
	mw.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", w.Code, w.Body.String())
	}
	if !captureOK {
		t.Fatal("no actor in context")
	}
	if captured.Kind != auth.ActorKindS3Key {
		t.Errorf("Kind=%q, want %q", captured.Kind, auth.ActorKindS3Key)
	}
	if captured.S3KeyID == nil {
		t.Fatal("Actor.S3KeyID is nil; want pointer to seeded id")
	}
	if *captured.S3KeyID != env.s3KeyID {
		t.Errorf("Actor.S3KeyID=%d, want %d", *captured.S3KeyID, env.s3KeyID)
	}
	if captured.ProjectScope == nil || *captured.ProjectScope != env.projectID {
		t.Errorf("ProjectScope mismatch: got %v, want &%d", captured.ProjectScope, env.projectID)
	}
}

// TestSigV4Middleware_PopulatesPayloadSHAContext asserts the literal
// hex(sha256(body)) declared by the client is reachable downstream via
// PayloadSHAFromContext.
func TestSigV4Middleware_PopulatesPayloadSHAContext(t *testing.T) {
	env := newMwEnv(t)
	const host = "bucket.s3.example.com"
	now := time.Now().UTC()

	body := []byte("hello-payload")
	wantHash := sha256Hex(body)

	var got string
	var ok bool
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok = s3.PayloadSHAFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	mw := s3.SigV4Middleware(env.service, 15*time.Minute)(inner)
	req := signSigV4Hex(t, http.MethodPut, "/s3/mybucket/k", host, body, env.akid, env.secret, now)
	w := httptest.NewRecorder()
	mw.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", w.Code, w.Body.String())
	}
	if !ok {
		t.Fatal("PayloadSHAFromContext: ok=false; want true")
	}
	if got != wantHash {
		t.Errorf("PayloadSHA=%q, want %q", got, wantHash)
	}
}

// TestSigV4Middleware_PayloadSHAContextUnsigned asserts UNSIGNED-PAYLOAD
// sentinel propagates verbatim — PutObject reads this to skip the
// post-write SHA compare.
func TestSigV4Middleware_PayloadSHAContextUnsigned(t *testing.T) {
	env := newMwEnv(t)
	const host = "bucket.s3.example.com"
	now := time.Now().UTC()

	var got string
	var ok bool
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok = s3.PayloadSHAFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	mw := s3.SigV4Middleware(env.service, 15*time.Minute)(inner)
	req := signSigV4Sentinel(t, http.MethodGet, "/s3/mybucket/k", host,
		env.akid, env.secret, "UNSIGNED-PAYLOAD", now)
	w := httptest.NewRecorder()
	mw.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", w.Code, w.Body.String())
	}
	if !ok {
		t.Fatal("PayloadSHAFromContext: ok=false; want true")
	}
	if got != "UNSIGNED-PAYLOAD" {
		t.Errorf("PayloadSHA=%q, want UNSIGNED-PAYLOAD", got)
	}
}

// TestPayloadSHAFromContext_AbsentReturnsFalse asserts the helper returns
// ("", false) when SigV4 middleware never ran (non-S3 routes).
func TestPayloadSHAFromContext_AbsentReturnsFalse(t *testing.T) {
	got, ok := s3.PayloadSHAFromContext(context.Background())
	if ok {
		t.Fatalf("ok=true on bare context; want false")
	}
	if got != "" {
		t.Errorf("got=%q, want empty", got)
	}
}
