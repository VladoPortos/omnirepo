//go:build conformance

// Package s3conf drives aws-sdk-go-v2 against an in-process omnirepo to
// verify SigV4 + S3 protocol conformance. Build-tag gated so the default
// `make test` never requires the AWS SDK.
package s3conf

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

// NewClient builds an aws-sdk-go-v2 S3 client pre-configured for the
// conformance test environment:
//   - static credentials (AKID + secret)
//   - TLS skip-verify (omnirepo uses self-signed certs)
//   - configurable path-style vs v-host
//   - RetryMaxAttempts controls auto-retry; set to 1 for negative tests
//     so the bare error is observable (RESEARCH A4 mitigation)
func NewClient(t *testing.T, endpoint, akid, secret string, pathStyle bool, retryMax int) *s3.Client {
	t.Helper()

	cfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion("auto"),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(akid, secret, ""),
		),
		awsconfig.WithHTTPClient(&http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // self-signed cert in test
			},
		}),
		awsconfig.WithRetryMaxAttempts(retryMax),
	)
	if err != nil {
		t.Fatalf("aws config: %v", err)
	}

	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = pathStyle
	})
}

// NewClockSkewTransport returns an http.RoundTripper that wraps the default
// TLS-skip-verify transport. It is NOT used for clock-skew injection itself
// (we use the signer NowFunc instead) but provides the TLS config needed to
// talk to omnirepo's self-signed cert.
func NewClockSkewTransport() http.RoundTripper {
	return &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
	}
}
