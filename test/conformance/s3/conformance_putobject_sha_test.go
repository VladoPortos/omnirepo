//go:build conformance

package s3conf

// Plan 02-03 Task 3 — aws-sdk-go-v2 conformance smoke for the chi-side
// PutObject SHA enforcement (S3HARD-04).
//
// Strategy: pre-seed the SDK's payload-hash context value with hex(sha256(""))
// BEFORE the ComputePayloadHash middleware runs. ComputePayloadHash is a
// no-op when GetPayloadHash(ctx) is already non-empty (see vendor/.../signer/v4/
// middleware.go:161). The signer therefore signs over the empty-SHA, the
// canonical request and Authorization header agree with the wire-level
// x-amz-content-sha256 header (which will also be set to the empty-SHA by
// ContentSHA256Header middleware further down the stack), AND the body bytes
// are still non-empty.
//
// Result on the wire:
//   x-amz-content-sha256 = e3b0...b855  (empty-SHA — what client signed)
//   body                 = 23 non-empty bytes
//
// Server SigV4 verify PASSES (header + signature + canonical request all
// agree). Chi-side intercept then computes the body's actual sha256, finds
// it differs from the declared empty-SHA, and rejects with 400
// XAmzContentSHA256Mismatch. This is exactly the audit-finding-#2 attack
// shape (signed SHA-A but sent SHA-B with internal client-side coercion).

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	smithy "github.com/aws/smithy-go"
	smithymw "github.com/aws/smithy-go/middleware"
)

// emptyBodySHA256 is hex(sha256("")) — the AWS-canonical declared SHA for
// an empty payload. We sign for this but send non-empty bytes.
const emptyBodySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// TestS3PutObjectSHAMismatch_Conformance drives a real aws-sdk-go-v2 client
// against omnirepo and asserts the server-side enforcement reaches the
// client correctly.
func TestS3PutObjectSHAMismatch_Conformance(t *testing.T) {
	fx := bootAppWithS3Bucket(t)
	// retryMax=1 so the bare error is observable (RESEARCH A4 mitigation,
	// matches the negative-test pattern in conformance_test.go).
	client := NewClient(t, fx.s3Endpoint, fx.akid, fx.secret, true, 1)

	body := []byte("non-empty-payload-bytes")
	key := "sha-mismatch-test"

	_, err := client.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket: &fx.bucketName,
		Key:    aws.String(key),
		Body:   bytes.NewReader(body),
	}, func(o *s3.Options) {
		o.APIOptions = append(o.APIOptions, seedPayloadHash(emptyBodySHA256))
	})
	if err == nil {
		t.Fatalf("expected XAmzContentSHA256Mismatch error, got nil")
	}

	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected smithy.APIError, got %T: %v", err, err)
	}
	if apiErr.ErrorCode() != "XAmzContentSHA256Mismatch" {
		t.Fatalf("expected XAmzContentSHA256Mismatch, got %q (message: %s)",
			apiErr.ErrorCode(), apiErr.ErrorMessage())
	}

	// Object MUST NOT have committed.
	_, herr := client.HeadObject(context.Background(), &s3.HeadObjectInput{
		Bucket: &fx.bucketName,
		Key:    aws.String(key),
	})
	if herr == nil {
		t.Fatalf("expected HEAD to fail with NotFound after rejected PUT")
	}
	// Don't pin the exact code here — different SDK versions surface
	// NoSuchKey vs NotFound for HEAD; the property under test is "no
	// commit", which any error from HEAD demonstrates.
}

// seedPayloadHash pre-seeds v4.SetPayloadHash on the request stack ctx
// BEFORE ComputePayloadHash runs. ComputePayloadHash early-returns when
// GetPayloadHash(ctx) is already non-empty, so the signer signs over the
// forced hash, the canonical request agrees, and the wire-level
// x-amz-content-sha256 header is set to the same value by
// ContentSHA256Header further down the Finalize stack.
func seedPayloadHash(forcedSHA string) func(*smithymw.Stack) error {
	return func(stack *smithymw.Stack) error {
		return stack.Finalize.Insert(
			smithymw.FinalizeMiddlewareFunc("OmniRepoSeedPayloadHash",
				func(ctx context.Context, in smithymw.FinalizeInput, next smithymw.FinalizeHandler) (smithymw.FinalizeOutput, smithymw.Metadata, error) {
					return next.HandleFinalize(v4.SetPayloadHash(ctx, forcedSHA), in)
				}),
			"ComputePayloadHash",
			smithymw.Before,
		)
	}
}
