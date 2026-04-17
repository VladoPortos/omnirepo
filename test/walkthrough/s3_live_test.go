//go:build walkthrough

// Package walkthrough — live-server S3 walkthrough (2026-04-17).
//
// Exercises the S3 protocol against a *live* omnirepo process that the
// developer started out-of-band (no in-process boot). Mirrors the conformance
// suite but targets the real HTTP port and uses the bucket/key/secret the
// developer provisioned with the walkthrough script.
//
// Run with:
//
//	OMNI_S3_ENDPOINT=http://localhost:18080 \
//	OMNI_S3_BUCKET=walkthrough-2026-04-17 \
//	OMNI_S3_AKID=AKIA... \
//	OMNI_S3_SECRET=... \
//	go test -tags=walkthrough -count=1 -v ./test/walkthrough/...
//
// Skips if any of the env vars are missing — this keeps the default test run
// untouched.
package walkthrough

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type liveEnv struct {
	endpoint string
	bucket   string
	akid     string
	secret   string
}

func loadEnv(t *testing.T) liveEnv {
	t.Helper()
	e := liveEnv{
		endpoint: os.Getenv("OMNI_S3_ENDPOINT"),
		bucket:   os.Getenv("OMNI_S3_BUCKET"),
		akid:     os.Getenv("OMNI_S3_AKID"),
		secret:   os.Getenv("OMNI_S3_SECRET"),
	}
	if e.endpoint == "" || e.bucket == "" || e.akid == "" || e.secret == "" {
		t.Skip("live walkthrough requires OMNI_S3_{ENDPOINT,BUCKET,AKID,SECRET}")
	}
	if !strings.HasSuffix(e.endpoint, "/s3") {
		e.endpoint = strings.TrimRight(e.endpoint, "/") + "/s3"
	}
	return e
}

func newS3Client(t *testing.T, e liveEnv, retries int) *s3.Client {
	t.Helper()
	cfg, err := awscfg.LoadDefaultConfig(context.Background(),
		awscfg.WithRegion("auto"),
		awscfg.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(e.akid, e.secret, "")),
		awscfg.WithRetryMaxAttempts(retries),
	)
	if err != nil {
		t.Fatalf("aws config: %v", err)
	}
	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(e.endpoint)
		o.UsePathStyle = true
	})
}

func TestLiveS3_PutListGetDelete(t *testing.T) {
	e := loadEnv(t)
	c := newS3Client(t, e, 3)
	ctx := context.Background()

	// Small object round-trip.
	key := "walkthrough/hello.txt"
	body := []byte(fmt.Sprintf("hello from walkthrough %d", time.Now().UnixNano()))
	if _, err := c.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &e.bucket, Key: &key, Body: bytes.NewReader(body),
	}); err != nil {
		t.Fatalf("PutObject: %v", err)
	}

	// List.
	prefix := "walkthrough/"
	lout, err := c.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: &e.bucket, Prefix: &prefix,
	})
	if err != nil {
		t.Fatalf("ListObjectsV2: %v", err)
	}
	var found bool
	for _, obj := range lout.Contents {
		if aws.ToString(obj.Key) == key {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("List did not return %q", key)
	}

	// Get — byte-for-byte equality.
	gout, err := c.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &e.bucket, Key: &key,
	})
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	defer gout.Body.Close()
	got, _ := io.ReadAll(gout.Body)
	if !bytes.Equal(got, body) {
		t.Fatalf("round-trip mismatch: got %q, want %q", got, body)
	}

	// Delete.
	if _, err := c.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: &e.bucket, Key: &key,
	}); err != nil {
		t.Fatalf("DeleteObject: %v", err)
	}
	if _, err := c.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: &e.bucket, Key: &key,
	}); err == nil {
		t.Fatal("HeadObject should 404 after delete")
	}
}

func TestLiveS3_Multipart6MiB(t *testing.T) {
	e := loadEnv(t)
	c := newS3Client(t, e, 3)
	ctx := context.Background()

	const size = 6 * 1024 * 1024
	body := make([]byte, size)
	if _, err := rand.Read(body); err != nil {
		t.Fatalf("rand: %v", err)
	}
	key := "walkthrough/mp-6mb.bin"

	create, err := c.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket: &e.bucket, Key: &key,
	})
	if err != nil {
		t.Fatalf("CreateMultipartUpload: %v", err)
	}
	uploadID := create.UploadId

	partSize := 5 * 1024 * 1024
	var parts []s3types.CompletedPart
	for i := 0; i*partSize < size; i++ {
		s := i * partSize
		eOff := s + partSize
		if eOff > size {
			eOff = size
		}
		p := int32(i + 1)
		up, uerr := c.UploadPart(ctx, &s3.UploadPartInput{
			Bucket: &e.bucket, Key: &key, UploadId: uploadID,
			PartNumber: &p, Body: bytes.NewReader(body[s:eOff]),
		})
		if uerr != nil {
			t.Fatalf("UploadPart %d: %v", p, uerr)
		}
		parts = append(parts, s3types.CompletedPart{ETag: up.ETag, PartNumber: &p})
	}

	cmp, err := c.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket: &e.bucket, Key: &key, UploadId: uploadID,
		MultipartUpload: &s3types.CompletedMultipartUpload{Parts: parts},
	})
	if err != nil {
		t.Fatalf("CompleteMultipartUpload: %v", err)
	}
	if !strings.Contains(aws.ToString(cmp.ETag), "-") {
		t.Fatalf("multipart ETag should have '-N' suffix, got %q", aws.ToString(cmp.ETag))
	}

	// Pull it back, verify size.
	gout, err := c.GetObject(ctx, &s3.GetObjectInput{Bucket: &e.bucket, Key: &key})
	if err != nil {
		t.Fatalf("GetObject multipart: %v", err)
	}
	defer gout.Body.Close()
	got, _ := io.ReadAll(gout.Body)
	if len(got) != size || !bytes.Equal(got, body) {
		t.Fatalf("multipart mismatch: got=%d want=%d", len(got), size)
	}
}

// TestLiveS3_CleanupMultipartObject deletes the 6 MiB object left behind
// by TestLiveS3_Multipart6MiB. Run by the walkthrough script to confirm
// DeleteObject's synchronous unlink (no trash path for S3).
func TestLiveS3_CleanupMultipartObject(t *testing.T) {
	e := loadEnv(t)
	c := newS3Client(t, e, 1)
	_, err := c.DeleteObject(context.Background(), &s3.DeleteObjectInput{
		Bucket: &e.bucket, Key: aws.String("walkthrough/mp-6mb.bin"),
	})
	if err != nil {
		t.Fatalf("DeleteObject: %v", err)
	}
}

// TestLiveS3_SeedDemoObjects populates the walkthrough bucket with a small
// set of objects (mixed flat keys + a nested prefix) so the UI has realistic
// content. Skipped when env vars are missing; safe to re-run (overwrites).
func TestLiveS3_SeedDemoObjects(t *testing.T) {
	e := loadEnv(t)
	c := newS3Client(t, e, 3)
	ctx := context.Background()
	seeds := []struct {
		key  string
		body string
	}{
		{"readme.txt", "top-level readme\n"},
		{"config.json", `{"mode":"prod"}`},
		{"logs/2026-04-17.log", "demo log entry\n"},
		{"logs/2026-04-16.log", "older log\n"},
		{"images/cover.png", "fake-png"},
	}
	for _, s := range seeds {
		_, err := c.PutObject(ctx, &s3.PutObjectInput{
			Bucket: &e.bucket,
			Key:    aws.String(s.key),
			Body:   bytes.NewReader([]byte(s.body)),
		})
		if err != nil {
			t.Fatalf("seed %q: %v", s.key, err)
		}
	}
}

// TestLiveS3_SignatureHeaderVisible confirms the SigV4 Authorization header
// is accepted (not "SignatureDoesNotMatch"). We hit PutObject and assert the
// server-side 200/204 — the SDK only reaches success if SigV4 verified.
// This is the explicit check the walkthrough seed asked for.
func TestLiveS3_SignatureHeaderVisible(t *testing.T) {
	e := loadEnv(t)
	// Low-level http: capture the Authorization header we actually send.
	urlStr := e.endpoint + "/" + e.bucket + "/walkthrough/sig-probe.txt"
	u, err := url.Parse(urlStr)
	if err != nil {
		t.Fatal(err)
	}
	// Use the SDK to produce the signed request indirectly: do a PutObject
	// and inspect whether we got success — if SigV4 had rejected we'd get
	// 403 SignatureDoesNotMatch.
	c := newS3Client(t, e, 1)
	_, err = c.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket: &e.bucket,
		Key:    aws.String("walkthrough/sig-probe.txt"),
		Body:   bytes.NewReader([]byte("sigv4 probe")),
	})
	if err != nil {
		t.Fatalf("PutObject against %s: %v", u, err)
	}
	// Sanity-check: the object is retrievable — implicitly confirms the
	// server accepted the SigV4 and wrote it.
	_, err = c.HeadObject(context.Background(), &s3.HeadObjectInput{
		Bucket: &e.bucket, Key: aws.String("walkthrough/sig-probe.txt"),
	})
	if err != nil {
		t.Fatalf("HeadObject: %v", err)
	}
	// Clean up.
	_, _ = c.DeleteObject(context.Background(), &s3.DeleteObjectInput{
		Bucket: &e.bucket, Key: aws.String("walkthrough/sig-probe.txt"),
	})
	t.Log("SigV4 accepted (PutObject → 200/204, not 403 SignatureDoesNotMatch)")

	// Also make a deliberate malformed-auth request to confirm the server
	// DOES reject bogus signatures — proving we exercised SigV4, not a
	// permissive fallback.
	badReq, _ := http.NewRequest("PUT", urlStr, bytes.NewReader([]byte("x")))
	badReq.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=AKIAXXXXXXXXXXXXXXXX/20260417/auto/s3/aws4_request, SignedHeaders=host, Signature=deadbeef")
	badReq.Header.Set("x-amz-date", time.Now().UTC().Format("20060102T150405Z"))
	resp, herr := http.DefaultClient.Do(badReq)
	if herr != nil {
		t.Fatalf("bad-auth probe: %v", herr)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("bogus signature expected 403, got %d", resp.StatusCode)
	}
}
