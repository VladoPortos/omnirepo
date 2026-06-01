//go:build conformance

package s3conf

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithy "github.com/aws/smithy-go"
)

// TestS3Conformance is the top-level conformance suite. Each subtest
// exercises a distinct S3 behaviour against the in-process omnirepo.
func TestS3Conformance(t *testing.T) {
	fx := bootAppWithS3Bucket(t)

	// Positive tests use RetryMaxAttempts=3 (normal SDK retry).
	// S3 routes are mounted at /s3/* so the SDK BaseEndpoint includes /s3.
	client := NewClient(t, fx.s3Endpoint, fx.akid, fx.secret, true, 3)

	t.Run("PutGetRoundTrip", func(t *testing.T) {
		body := []byte("hello conformance")
		key := "test/roundtrip.txt"
		_, err := client.PutObject(context.Background(), &s3.PutObjectInput{
			Bucket: &fx.bucketName,
			Key:    &key,
			Body:   bytes.NewReader(body),
		})
		if err != nil {
			t.Fatalf("PutObject: %v", err)
		}

		got, err := client.GetObject(context.Background(), &s3.GetObjectInput{
			Bucket: &fx.bucketName,
			Key:    &key,
		})
		if err != nil {
			t.Fatalf("GetObject: %v", err)
		}
		defer got.Body.Close()
		data, _ := io.ReadAll(got.Body)
		if !bytes.Equal(data, body) {
			t.Fatalf("round-trip mismatch: got %q, want %q", data, body)
		}
	})

	t.Run("MultipartUpload", func(t *testing.T) {
		// 10 MiB body triggers multipart behaviour. We use the low-level
		// multipart API to control part boundaries and verify ETag format.
		const size = 10 * 1024 * 1024
		body := make([]byte, size)
		if _, err := rand.Read(body); err != nil {
			t.Fatalf("rand: %v", err)
		}
		key := "test/multipart-10mb.bin"

		createOut, err := client.CreateMultipartUpload(context.Background(), &s3.CreateMultipartUploadInput{
			Bucket: &fx.bucketName,
			Key:    &key,
		})
		if err != nil {
			t.Fatalf("CreateMultipartUpload: %v", err)
		}
		uploadID := createOut.UploadId

		partSize := 5 * 1024 * 1024
		var parts []s3types.CompletedPart
		for i := 0; i*partSize < len(body); i++ {
			start := i * partSize
			end := start + partSize
			if end > len(body) {
				end = len(body)
			}
			partNum := int32(i + 1)
			up, uerr := client.UploadPart(context.Background(), &s3.UploadPartInput{
				Bucket:     &fx.bucketName,
				Key:        &key,
				UploadId:   uploadID,
				PartNumber: &partNum,
				Body:       bytes.NewReader(body[start:end]),
			})
			if uerr != nil {
				t.Fatalf("UploadPart %d: %v", partNum, uerr)
			}
			parts = append(parts, s3types.CompletedPart{
				ETag:       up.ETag,
				PartNumber: &partNum,
			})
		}

		completeOut, err := client.CompleteMultipartUpload(context.Background(), &s3.CompleteMultipartUploadInput{
			Bucket:   &fx.bucketName,
			Key:      &key,
			UploadId: uploadID,
			MultipartUpload: &s3types.CompletedMultipartUpload{
				Parts: parts,
			},
		})
		if err != nil {
			t.Fatalf("CompleteMultipartUpload: %v", err)
		}

		// Verify the CompleteMultipartUpload response ETag has the -N
		// suffix (multipart convention). Note: gofakes3's Object.Hash
		// field is []byte so HeadObject renders ETag without the -N
		// suffix — the correct multipart ETag is only in the Complete
		// response. This is a gofakes3 limitation, not a bug.
		completeETag := aws.ToString(completeOut.ETag)
		if !strings.Contains(completeETag, "-") {
			t.Fatalf("multipart ETag should contain '-N' suffix, got %q", completeETag)
		}

		// HeadObject still works (verifies object is accessible).
		_, err = client.HeadObject(context.Background(), &s3.HeadObjectInput{
			Bucket: &fx.bucketName,
			Key:    &key,
		})
		if err != nil {
			t.Fatalf("HeadObject: %v", err)
		}

		// Verify content round-trips.
		got, err := client.GetObject(context.Background(), &s3.GetObjectInput{
			Bucket: &fx.bucketName,
			Key:    &key,
		})
		if err != nil {
			t.Fatalf("GetObject multipart: %v", err)
		}
		defer got.Body.Close()
		data, _ := io.ReadAll(got.Body)
		if !bytes.Equal(data, body) {
			t.Fatalf("multipart round-trip: size got=%d want=%d", len(data), len(body))
		}
	})

	t.Run("VHostStyle", func(t *testing.T) {
		// v-host client (UsePathStyle=false). The VHostRewrite middleware
		// inspects r.Host and rewrites the path.
		vhostClient := NewClient(t, fx.s3Endpoint, fx.akid, fx.secret, false, 3)
		body := []byte("vhost-test-body")
		key := "vhost/key.txt"
		_, err := vhostClient.PutObject(context.Background(), &s3.PutObjectInput{
			Bucket: &fx.bucketName,
			Key:    &key,
			Body:   bytes.NewReader(body),
		})
		if err != nil {
			t.Fatalf("PutObject v-host: %v", err)
		}

		got, err := vhostClient.GetObject(context.Background(), &s3.GetObjectInput{
			Bucket: &fx.bucketName,
			Key:    &key,
		})
		if err != nil {
			t.Fatalf("GetObject v-host: %v", err)
		}
		defer got.Body.Close()
		data, _ := io.ReadAll(got.Body)
		if !bytes.Equal(data, body) {
			t.Fatalf("v-host round-trip mismatch: got %q, want %q", data, body)
		}
	})

	t.Run("ListObjectsV2Pagination", func(t *testing.T) {
		prefix := "list-test/"
		for i := 0; i < 20; i++ {
			key := fmt.Sprintf("%sitem-%02d.txt", prefix, i)
			_, err := client.PutObject(context.Background(), &s3.PutObjectInput{
				Bucket: &fx.bucketName,
				Key:    &key,
				Body:   bytes.NewReader([]byte(fmt.Sprintf("item-%02d", i))),
			})
			if err != nil {
				t.Fatalf("PutObject %s: %v", key, err)
			}
		}

		var allKeys []string
		var contToken *string
		maxKeys := int32(5)
		for {
			out, err := client.ListObjectsV2(context.Background(), &s3.ListObjectsV2Input{
				Bucket:            &fx.bucketName,
				Prefix:            &prefix,
				MaxKeys:           &maxKeys,
				ContinuationToken: contToken,
			})
			if err != nil {
				t.Fatalf("ListObjectsV2: %v", err)
			}
			for _, obj := range out.Contents {
				allKeys = append(allKeys, aws.ToString(obj.Key))
			}
			if !aws.ToBool(out.IsTruncated) {
				break
			}
			contToken = out.NextContinuationToken
			if contToken == nil {
				t.Fatal("IsTruncated=true but NextContinuationToken is nil")
			}
		}
		if len(allKeys) != 20 {
			t.Fatalf("expected 20 keys, got %d: %v", len(allKeys), allKeys)
		}
	})

	// --- Negative tests ---
	// Use RetryMaxAttempts=1 so we observe the bare error without SDK
	// auto-correction (RESEARCH A4 mitigation).

	t.Run("WrongSecret_SignatureDoesNotMatch", func(t *testing.T) {
		badClient := NewClient(t, fx.s3Endpoint, fx.akid, "wrong-secret-xxxxxxxxxxxxxxxxxxxxxxxxx", true, 1)
		key := "neg/wrong-secret.txt"
		_, err := badClient.PutObject(context.Background(), &s3.PutObjectInput{
			Bucket: &fx.bucketName,
			Key:    &key,
			Body:   bytes.NewReader([]byte("should fail")),
		})
		if err == nil {
			t.Fatal("expected SignatureDoesNotMatch, got nil")
		}
		assertAWSErrorCode(t, err, "SignatureDoesNotMatch")
	})

	t.Run("BogusAKID_InvalidAccessKeyId", func(t *testing.T) {
		badClient := NewClient(t, fx.s3Endpoint, "AKIAXXXXXXXXXXXXXXXX", "secret-doesnt-matter-xxxxxxxxxxxx", true, 1)
		key := "neg/bogus-akid.txt"
		_, err := badClient.PutObject(context.Background(), &s3.PutObjectInput{
			Bucket: &fx.bucketName,
			Key:    &key,
			Body:   bytes.NewReader([]byte("should fail")),
		})
		if err == nil {
			t.Fatal("expected InvalidAccessKeyId, got nil")
		}
		assertAWSErrorCode(t, err, "InvalidAccessKeyId")
	})

	t.Run("ClockSkew_RequestTimeTooSkewed", func(t *testing.T) {
		// To produce a genuine RequestTimeTooSkewed error, we must send a
		// request with a VALID SigV4 signature computed at a skewed time.
		// aws-sdk-go-v2 does not expose NowFunc, so we hand-craft a
		// minimal SigV4-signed GET request with x-amz-date 16 min in the
		// future. The server sees a valid signature (over the future time)
		// and returns RequestTimeTooSkewed because the timestamp exceeds
		// the +-15 min window.
		skewedTime := time.Now().UTC().Add(16 * time.Minute)
		url := fmt.Sprintf("%s/s3/%s", fx.httpEndpoint, fx.bucketName)
		req := buildSkewedSigV4Request(t, "GET", url, fx.akid, fx.secret, skewedTime)

		httpClient := &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
			},
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			t.Fatalf("skewed request: %v", err)
		}
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(resp.Body)

		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected 403, got %d: %s", resp.StatusCode, respBody)
		}
		bodyStr := string(respBody)
		if !strings.Contains(bodyStr, "RequestTimeTooSkewed") {
			t.Fatalf("expected RequestTimeTooSkewed in response, got: %s", bodyStr)
		}
		if !strings.Contains(bodyStr, "ServerTime") {
			t.Fatalf("expected ServerTime echoed in response, got: %s", bodyStr)
		}
		t.Logf("RequestTimeTooSkewed response verified with ServerTime echoed")
	})

	t.Run("DeleteNonEmptyBucket_BucketNotEmpty", func(t *testing.T) {
		extraBucket := "nonempty-bucket"
		createBucketDirect(t, fx.dataRoot, fx.project, extraBucket)
		key := "keep-me.txt"
		_, err := client.PutObject(context.Background(), &s3.PutObjectInput{
			Bucket: &extraBucket,
			Key:    &key,
			Body:   bytes.NewReader([]byte("data")),
		})
		if err != nil {
			t.Fatalf("PutObject to non-empty bucket: %v", err)
		}

		_, err = client.DeleteBucket(context.Background(), &s3.DeleteBucketInput{
			Bucket: &extraBucket,
		})
		if err == nil {
			t.Fatal("expected BucketNotEmpty, got nil")
		}
		assertAWSErrorCode(t, err, "BucketNotEmpty")
	})

	t.Run("DeleteObject", func(t *testing.T) {
		key := "to-delete.txt"
		_, err := client.PutObject(context.Background(), &s3.PutObjectInput{
			Bucket: &fx.bucketName,
			Key:    &key,
			Body:   bytes.NewReader([]byte("delete me")),
		})
		if err != nil {
			t.Fatalf("PutObject: %v", err)
		}

		_, err = client.DeleteObject(context.Background(), &s3.DeleteObjectInput{
			Bucket: &fx.bucketName,
			Key:    &key,
		})
		if err != nil {
			t.Fatalf("DeleteObject: %v", err)
		}

		_, err = client.HeadObject(context.Background(), &s3.HeadObjectInput{
			Bucket: &fx.bucketName,
			Key:    &key,
		})
		if err == nil {
			t.Fatal("expected NotFound after delete, got nil")
		}
	})

	t.Run("DinD_AWSCLICopy", func(t *testing.T) {
		image := resolveImage(t, "aws-cli")

		key := "cli-test/hello.txt"
		_, err := client.PutObject(context.Background(), &s3.PutObjectInput{
			Bucket: &fx.bucketName,
			Key:    &key,
			Body:   bytes.NewReader([]byte("hello from omnirepo")),
		})
		if err != nil {
			t.Fatalf("PutObject for CLI test: %v", err)
		}

		script := fmt.Sprintf(`set -e
export AWS_ACCESS_KEY_ID=%s
export AWS_SECRET_ACCESS_KEY=%s
export AWS_DEFAULT_REGION=auto
aws --endpoint-url http://host.docker.internal:%d/s3 --no-verify-ssl s3 cp s3://%s/%s /tmp/downloaded.txt
cat /tmp/downloaded.txt
`, fx.akid, fx.secret, fx.port, fx.bucketName, key)

		out, derr := dockerRun(t, image, script)
		if derr != nil {
			t.Fatalf("aws s3 cp via DinD failed: %v\n--- output ---\n%s", derr, out)
		}
		if !strings.Contains(out, "hello from omnirepo") {
			t.Fatalf("expected 'hello from omnirepo' in output:\n%s", out)
		}
	})
}

// buildSkewedSigV4Request constructs a minimal SigV4-signed HTTP request at
// the given (skewed) time. This is used to exercise the server's
// RequestTimeTooSkewed error path: the signature is valid (computed over the
// skewed timestamp) but the server rejects the request because the x-amz-date
// is outside the allowed +-15 min window.
//
// Only supports empty-body GET requests (sufficient for ListBucket/HeadBucket).
func buildSkewedSigV4Request(t *testing.T, method, rawURL, akid, secret string, signTime time.Time) *http.Request {
	t.Helper()

	req, err := http.NewRequest(method, rawURL, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	amzDate := signTime.Format("20060102T150405Z")
	dateStamp := signTime.Format("20060102")
	region := "auto"
	service := "s3"
	scope := dateStamp + "/" + region + "/" + service + "/aws4_request"

	// Set required headers before building canonical request.
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855") // sha256("")
	req.Header.Set("Host", req.Host)

	// Canonical request.
	canonicalURI := req.URL.Path
	if canonicalURI == "" {
		canonicalURI = "/"
	}
	canonicalQueryString := req.URL.RawQuery
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	canonicalHeaders := "host:" + req.Host + "\n" +
		"x-amz-content-sha256:" + req.Header.Get("x-amz-content-sha256") + "\n" +
		"x-amz-date:" + amzDate + "\n"
	payloadHash := req.Header.Get("x-amz-content-sha256")

	canonicalRequest := method + "\n" +
		canonicalURI + "\n" +
		canonicalQueryString + "\n" +
		canonicalHeaders + "\n" +
		signedHeaders + "\n" +
		payloadHash

	// String to sign.
	stringToSign := "AWS4-HMAC-SHA256\n" +
		amzDate + "\n" +
		scope + "\n" +
		hashSHA256([]byte(canonicalRequest))

	// Derive signing key.
	kDate := hmacSHA256([]byte("AWS4"+secret), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(service))
	kSigning := hmacSHA256(kService, []byte("aws4_request"))

	signature := hex.EncodeToString(hmacSHA256(kSigning, []byte(stringToSign)))

	authHeader := fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		akid, scope, signedHeaders, signature)
	req.Header.Set("Authorization", authHeader)

	return req
}

func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

func hashSHA256(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// assertAWSErrorCode checks that err wraps a smithy.APIError with the given code.
func assertAWSErrorCode(t *testing.T, err error, wantCode string) {
	t.Helper()
	var ae smithy.APIError
	if !errors.As(err, &ae) {
		t.Fatalf("expected AWS API error with code %q, got %T: %v", wantCode, err, err)
	}
	if ae.ErrorCode() != wantCode {
		t.Fatalf("expected error code %q, got %q (message: %s)", wantCode, ae.ErrorCode(), ae.ErrorMessage())
	}
}
