//go:build conformance

package lifecycleconf

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	smithy "github.com/aws/smithy-go"
)

// TestLifecycleConformance is the lifecycle end-to-end gate. One in-process
// app boot, one project + 4 repos + S3 access key + bucket + project-owned API
// key + 4 indexed packages, then SOFT-DELETE → assert every protocol surface
// denies, then RESTORE → assert every surface works again (restore regression
// guard).
//
// The test is structured into three phases:
//
//   - Phase A (BeforeSoftDelete_*): every surface MUST work. The Search
//     subtest performs a positive sanity assertion (≥1 result whose Name
//     contains the indexed package name) so a JSON envelope mismatch in
//     SearchResult / searchResponse fails HERE, before the post-delete
//     zero-result assertion can mask the bug.
//   - Phase B (AfterSoftDelete_*): every surface MUST deny. S3 ops fail
//     with AWS-shape error envelopes (denial signal: any of NoSuchBucket,
//     InvalidAccessKeyId, SignatureDoesNotMatch, AccessDenied, Forbidden);
//     REST API key returns 401 with the existing auth.unauthenticated
//     envelope; search returns zero results.
//   - Phase C (AfterRestore_*): every surface MUST work again. This is the
//     restore regression guard — proves Restore actually un-cascades, not
//     just that SoftDelete cascades.
func TestLifecycleConformance(t *testing.T) {
	fx := bootAppWithLifecycleFixture(t)

	// retryMax=1 keeps negative-case sub-tests fast on auth-fail (no SDK
	// auto-retry mask of the bare error). Phase A uses the same client; positive
	// ops succeed on first try so the retry knob doesn't hide success.
	client := NewS3Client(t, fx.s3Endpoint, fx.s3AKID, fx.s3Secret, 1)

	// =========================================================================
	// PHASE A — before soft-delete: every surface MUST work.
	// =========================================================================
	t.Run("BeforeSoftDelete_S3GetWorks", func(t *testing.T) {
		out, err := client.GetObject(context.Background(), &s3.GetObjectInput{
			Bucket: ptr(fx.s3Bucket),
			Key:    ptr("test/object.bin"),
		})
		if err != nil {
			t.Fatalf("pre-delete GetObject: %v", err)
		}
		defer out.Body.Close()
		body, _ := io.ReadAll(out.Body)
		if !bytes.Equal(body, []byte("hello")) {
			t.Fatalf("pre-delete body: got %q want %q", body, []byte("hello"))
		}
	})

	t.Run("BeforeSoftDelete_RESTAPIKeyAuths", func(t *testing.T) {
		status := getProjectReposWithBearer(t, fx, fx.projectAPIKey)
		if status != http.StatusOK {
			t.Fatalf("pre-delete REST status=%d, want 200", status)
		}
	})

	t.Run("BeforeSoftDelete_SearchFindsAllPackages", func(t *testing.T) {
		// POSITIVE SANITY ASSERTION: require ≥1 result whose Name
		// contains the indexed package name. A struct-tag mismatch in
		// SearchResult / searchResponse would silently decode to zero
		// results and then make the post-delete zero-result test pass even
		// when the search filter is broken. Forcing a positive match here
		// proves the envelope decode bug surfaces in PHASE A, not later.
		for _, q := range []string{
			"lifecyclepkg-rpm",
			"lifecyclepkg-deb",
			"lifecyclepkg-pypi",
			"lifecyclepkg-helm",
		} {
			results := searchAsAdmin(t, fx, q)
			if len(results) == 0 {
				t.Fatalf("pre-delete search %q: 0 results, want ≥1 (positive sanity)", q)
			}
			found := false
			for _, r := range results {
				if strings.Contains(r.Name, q) {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("pre-delete search %q: %d results but none had Name containing %q (envelope decode bug?)",
					q, len(results), q)
			}
		}
	})

	// =========================================================================
	// SOFT-DELETE the project — DELETE /api/v1/admin/projects/{name}.
	// =========================================================================
	softDeleteProject(t, fx, fx.project)

	// =========================================================================
	// PHASE B — after soft-delete: every surface MUST deny.
	// =========================================================================
	t.Run("AfterSoftDelete_S3GetDenied", func(t *testing.T) {
		_, err := client.GetObject(context.Background(), &s3.GetObjectInput{
			Bucket: ptr(fx.s3Bucket),
			Key:    ptr("test/object.bin"),
		})
		if err == nil {
			t.Fatal("expected error after soft-delete GetObject; got nil")
		}
		assertS3DenialError(t, err, "GetObject")
	})

	t.Run("AfterSoftDelete_S3PutDenied", func(t *testing.T) {
		_, err := client.PutObject(context.Background(), &s3.PutObjectInput{
			Bucket: ptr(fx.s3Bucket),
			Key:    ptr("test/postdelete.bin"),
			Body:   bytes.NewReader([]byte("should fail")),
		})
		if err == nil {
			t.Fatal("expected error after soft-delete PutObject; got nil")
		}
		assertS3DenialError(t, err, "PutObject")

		// Defense-in-depth: no s3_objects row should have been written
		// for the rejected key — proves the denial fired before the
		// upload pipeline created any row.
		if cnt := countS3ObjectsByKey(t, fx.dataRoot, "test/postdelete.bin"); cnt != 0 {
			t.Fatalf("post-delete PutObject created %d s3_objects rows; want 0", cnt)
		}
	})

	t.Run("AfterSoftDelete_S3ListBucketsHidesDeleted", func(t *testing.T) {
		// gofakes3 ListBuckets is global (not bucket-name-routed). After
		// soft-delete, EITHER the entire ListBuckets call fails at SigV4
		// (key resolves to nothing) OR the bucket simply does not appear.
		// Both are acceptable denial signals.
		out, err := client.ListBuckets(context.Background(), &s3.ListBucketsInput{})
		if err != nil {
			// auth fails entirely — acceptable. Confirm it's an AWS-shape error.
			assertS3DenialError(t, err, "ListBuckets")
			return
		}
		for _, b := range out.Buckets {
			if b.Name != nil && *b.Name == fx.s3Bucket {
				t.Fatalf("post-delete ListBuckets returned the soft-deleted bucket %q", fx.s3Bucket)
			}
		}
	})

	t.Run("AfterSoftDelete_S3CreateMultipartUploadDenied", func(t *testing.T) {
		_, err := client.CreateMultipartUpload(context.Background(), &s3.CreateMultipartUploadInput{
			Bucket: ptr(fx.s3Bucket),
			Key:    ptr("test/multi.bin"),
		})
		if err == nil {
			t.Fatal("expected error after soft-delete CreateMultipartUpload; got nil")
		}
		assertS3DenialError(t, err, "CreateMultipartUpload")
	})

	t.Run("AfterSoftDelete_RESTAPIKey401", func(t *testing.T) {
		status := getProjectReposWithBearer(t, fx, fx.projectAPIKey)
		if status != http.StatusUnauthorized {
			t.Fatalf("post-delete REST status=%d, want 401 (cascade)", status)
		}
	})

	t.Run("AfterSoftDelete_SearchAsAdminFindsZero", func(t *testing.T) {
		for _, q := range []string{
			"lifecyclepkg-rpm",
			"lifecyclepkg-deb",
			"lifecyclepkg-pypi",
			"lifecyclepkg-helm",
		} {
			results := searchAsAdmin(t, fx, q)
			if len(results) != 0 {
				t.Fatalf("post-delete search %q: got %d results, want 0",
					q, len(results))
			}
		}
	})

	// =========================================================================
	// RESTORE the project — POST /api/v1/admin/trash/project-<id>/restore.
	// =========================================================================
	restoreProject(t, fx)

	// =========================================================================
	// PHASE C — after restore: every surface MUST work again (restore symmetry).
	// =========================================================================
	t.Run("AfterRestore_S3GetWorks", func(t *testing.T) {
		// Use a fresh client with normal retries so the SDK doesn't observe
		// any stale state from the previous denied-path attempts.
		retryClient := NewS3Client(t, fx.s3Endpoint, fx.s3AKID, fx.s3Secret, 3)
		out, err := retryClient.GetObject(context.Background(), &s3.GetObjectInput{
			Bucket: ptr(fx.s3Bucket),
			Key:    ptr("test/object.bin"),
		})
		if err != nil {
			t.Fatalf("post-restore GetObject: %v", err)
		}
		defer out.Body.Close()
		body, _ := io.ReadAll(out.Body)
		if !bytes.Equal(body, []byte("hello")) {
			t.Fatalf("post-restore body: got %q want %q", body, []byte("hello"))
		}
	})

	t.Run("AfterRestore_RESTAPIKeyAuths", func(t *testing.T) {
		status := getProjectReposWithBearer(t, fx, fx.projectAPIKey)
		if status != http.StatusOK {
			t.Fatalf("post-restore REST status=%d, want 200 (api_keys.revoked_at restored)", status)
		}
	})

	t.Run("AfterRestore_SearchFindsAllPackages", func(t *testing.T) {
		for _, q := range []string{
			"lifecyclepkg-rpm",
			"lifecyclepkg-deb",
			"lifecyclepkg-pypi",
			"lifecyclepkg-helm",
		} {
			results := searchAsAdmin(t, fx, q)
			if len(results) == 0 {
				t.Fatalf("post-restore search %q: 0 results, want ≥1 (reindex)", q)
			}
		}
	})
}

// ptr returns a pointer to the supplied string. Convenience for the
// aws-sdk-go-v2 input structs (Bucket/Key/etc. are *string).
func ptr(s string) *string { return &s }

// assertS3DenialError verifies err is a smithy.APIError whose code is in
// the accepted denial set. The exact code depends on which gate fired first:
//
//   - InvalidAccessKeyId — the SigV4 verifier saw FindByAKID return
//     ErrS3AccessKeyNotFound (the access-key lookup hardening OR the
//     soft-delete cascade — either way, the key collapses to "missing").
//   - NoSuchBucket — the bucket lookup saw the project-soft-delete filter
//     and returned no row (the bucket-lookup hardening).
//   - SignatureDoesNotMatch — gofakes3's catch-all when SigV4 verification
//     can't proceed.
//   - AccessDenied / Forbidden — generic denial envelope.
//
// All five are acceptable denial signals; the test does NOT pin one
// because the exact code is an implementation detail of which gate fires
// first.
func assertS3DenialError(t *testing.T, err error, op string) {
	t.Helper()
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("%s: error not a smithy APIError: %T %v", op, err, err)
	}
	denialCodes := []string{
		"NoSuchBucket",
		"InvalidAccessKeyId",
		"SignatureDoesNotMatch",
		"AccessDenied",
		"Forbidden",
	}
	for _, code := range denialCodes {
		if apiErr.ErrorCode() == code {
			return
		}
	}
	t.Fatalf("%s: AWS error code %q is not a denial signal (want one of %v); message=%q",
		op, apiErr.ErrorCode(), denialCodes, apiErr.ErrorMessage())
}
