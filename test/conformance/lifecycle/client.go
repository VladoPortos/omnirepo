//go:build conformance

// Package lifecycleconf is the LIFECYCLE-11 cross-protocol denial conformance
// suite. It boots omnirepo in-process, provisions a project + 4 repos +
// S3 access key + S3 bucket + project-owned API key + 4 indexed packages,
// then asserts every protocol surface (S3 SigV4, REST API key, search-as-
// member, search-as-super-admin) denies access after the project is
// soft-deleted and works again after Restore. Build-tag gated so the
// default `make test` never requires the AWS SDK.
package lifecycleconf

import (
	"context"
	"crypto/tls"
	"net/http"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// NewS3Client builds an aws-sdk-go-v2 S3 client pre-configured for the
// lifecycle conformance test environment. Mirrors test/conformance/s3/client.go
// (NewClient) — single-purpose conformance helper, no production callers.
//
//   - Static credentials (AKID + secret).
//   - TLS skip-verify (omnirepo serves a self-signed cert by default; the
//     fixture uses plain HTTP listener but keep this defense in depth).
//   - Path-style addressing (UsePathStyle=true) — bucket-in-path, the
//     omnirepo S3 mount serves /s3/<bucket>/<key>.
//   - retryMax=1 keeps the negative sub-tests fast on auth-fail (no SDK
//     auto-retry mask of the bare error).
func NewS3Client(t *testing.T, endpoint, akid, secret string, retryMax int) *s3.Client {
	t.Helper()

	cfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion("auto"),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(akid, secret, ""),
		),
		awsconfig.WithHTTPClient(&http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // fixture-only
			},
		}),
		awsconfig.WithRetryMaxAttempts(retryMax),
	)
	if err != nil {
		t.Fatalf("aws config: %v", err)
	}

	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})
}
