package sigv4

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// BodyMode enumerates the three payload-hash shapes S3 clients send.
type BodyMode int

const (
	// BodyModeSHA256 — x-amz-content-sha256 holds hex(sha256(body)). The
	// verifier SHA256-hashes the streamed body and compares.
	BodyModeSHA256 BodyMode = iota
	// BodyModeUnsignedPayload — UNSIGNED-PAYLOAD literal. Body is NOT
	// signed; canonical-request payload-hash is the magic string.
	BodyModeUnsignedPayload
	// BodyModeStreamingSigned — STREAMING-AWS4-HMAC-SHA256-PAYLOAD.
	// Body is chunked, each chunk individually signed; the verifier
	// wraps r.Body in a chunk-verifying reader.
	BodyModeStreamingSigned
)

// SecretLookup resolves an access-key-id to a plaintext secret. Implementations
// MUST return ErrInvalidAccessKeyId for BOTH missing and revoked keys (D-12 —
// removes the timing oracle).
type SecretLookup func(akid string) (secret string, err error)

// VerifyResult carries metadata the caller needs post-verify: which actor
// authenticated (via AKID), what scope the request was signed under, and
// which body-hash mode we observed. After a successful verify, r.Body may
// have been wrapped with a streaming-chunk reader that verifies each chunk's
// signature as it's consumed — callers MUST read through the wrapped body.
type VerifyResult struct {
	AccessKeyID string
	Scope       string
	RequestTime time.Time
	BodyMode    BodyMode
}

// parsedAuthz is the decomposed Authorization header.
type parsedAuthz struct {
	AKID          string
	Date          string // YYYYMMDD
	Region        string
	Service       string
	Scope         string // "YYYYMMDD/region/service/aws4_request"
	SignedHeaders []string
	Signature     string
}

// authzRE relaxes the spec slightly to tolerate the trailing commas, extra
// whitespace, and comma-without-space variants real aws-cli / aws-sdk-go-v2
// emit in the wild.
var authzRE = regexp.MustCompile(`(?i)^AWS4-HMAC-SHA256\s+(.+)$`)

// parseAuthzHeader extracts Credential/SignedHeaders/Signature.
func parseAuthzHeader(h string) (*parsedAuthz, error) {
	m := authzRE.FindStringSubmatch(strings.TrimSpace(h))
	if m == nil {
		return nil, ErrMalformed
	}
	out := &parsedAuthz{}
	// Split on ",\s*" so trailing commas and single-comma forms both parse.
	for _, part := range regexp.MustCompile(`,\s*`).Split(m[1], -1) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			return nil, ErrMalformed
		}
		switch strings.ToLower(strings.TrimSpace(kv[0])) {
		case "credential":
			segs := strings.Split(kv[1], "/")
			if len(segs) < 5 {
				return nil, ErrMalformed
			}
			out.AKID = segs[0]
			out.Date = segs[1]
			out.Region = segs[2]
			out.Service = segs[3]
			out.Scope = strings.Join(segs[1:], "/")
		case "signedheaders":
			out.SignedHeaders = strings.Split(strings.ToLower(kv[1]), ";")
		case "signature":
			out.Signature = strings.TrimSpace(kv[1])
		}
	}
	if out.AKID == "" || out.Signature == "" || len(out.SignedHeaders) == 0 {
		return nil, ErrMalformed
	}
	return out, nil
}

// nowFn is overridable for tests so we can drive the clock-skew window.
var nowFn = func() time.Time { return time.Now().UTC() }

// Verify validates an incoming S3 request per AWS SigV4 (Authorization-header
// flavor).
//
// Dispatch:
//  1. Parse Authorization header → AKID / scope / signed-headers / signature.
//  2. Reject if Service != "s3" (D-13 region-agnostic but service-strict).
//  3. Parse x-amz-date; reject if |now - request| > skew.
//  4. Look up secret by AKID (missing|revoked collapse to ErrInvalidAccessKeyId).
//  5. Dispatch on x-amz-content-sha256:
//     - "UNSIGNED-PAYLOAD"                        → BodyModeUnsignedPayload
//     - "STREAMING-AWS4-HMAC-SHA256-PAYLOAD"      → wrap r.Body with chunk verifier
//     - <64-hex>                                  → BodyModeSHA256 (verified lazily by S3 handler)
//  6. Build canonical-request + string-to-sign + kSigning → HMAC → constant-time compare.
//  7. Return VerifyResult (or the best-matched sentinel error).
//
// Callers render errors with WriteError.
func Verify(r *http.Request, lookup SecretLookup, skew time.Duration) (*VerifyResult, error) {
	authz := r.Header.Get("Authorization")
	if authz == "" {
		return nil, ErrMalformed
	}
	parsed, err := parseAuthzHeader(authz)
	if err != nil {
		return nil, err
	}
	if parsed.Service != "s3" {
		return nil, ErrInvalidRequest
	}

	amzDate := r.Header.Get("x-amz-date")
	if amzDate == "" {
		return nil, ErrMalformed
	}
	reqTime, err := time.Parse(amzTimeFmt, amzDate)
	if err != nil {
		return nil, ErrMalformed
	}
	now := nowFn()
	if delta := reqTime.Sub(now); delta > skew || -delta > skew {
		return nil, &ErrSkew{
			RequestTime:    reqTime,
			ServerTime:     now,
			MaxAllowedSkew: skew,
		}
	}

	secret, err := lookup(parsed.AKID)
	if err != nil {
		return nil, ErrInvalidAccessKeyId
	}

	// ---- body-hash dispatch ----
	cs := r.Header.Get("x-amz-content-sha256")
	mode, bodyHash, err := dispatchBody(r, cs, secret, parsed, amzDate)
	if err != nil {
		return nil, err
	}

	// ---- canonical request + sts + signing ----
	// Go's HTTP server strips the Host header from r.Header and stores it in
	// r.Host. Inject it so canonicalHeaders can find "host" in the signed-
	// headers set. This is safe even when r.Header already has "Host" (the
	// sigv4 unit tests set it explicitly) — Set overwrites.
	if r.Host != "" && r.Header.Get("Host") == "" {
		r.Header.Set("Host", r.Host)
	}
	canonReq := canonicalRequest(r.Method, r.URL.EscapedPath(), r.URL.RawQuery,
		r.Header, parsed.SignedHeaders, bodyHash)
	sts := stringToSign(amzDate, parsed.Scope, canonReq)
	kSigning := deriveKey(secret, parsed.Date, parsed.Region, parsed.Service)
	expected := hmacSHA256Hex(kSigning, []byte(sts))

	if subtle.ConstantTimeCompare([]byte(expected), []byte(parsed.Signature)) != 1 {
		return nil, ErrSignatureMismatch
	}

	return &VerifyResult{
		AccessKeyID: parsed.AKID,
		Scope:       parsed.Scope,
		RequestTime: reqTime,
		BodyMode:    mode,
	}, nil
}

// dispatchBody decides how the payload-hash string appears in the canonical
// request and wraps r.Body when STREAMING is in use.
func dispatchBody(r *http.Request, cs, secret string, parsed *parsedAuthz, amzDate string) (BodyMode, string, error) {
	switch {
	case cs == "UNSIGNED-PAYLOAD":
		return BodyModeUnsignedPayload, "UNSIGNED-PAYLOAD", nil
	case cs == "STREAMING-AWS4-HMAC-SHA256-PAYLOAD":
		// Swap r.Body with a chunk-verifying reader. The seed signature is
		// the header signature; subsequent chunk signatures chain off the
		// previous chunk's signature.
		kSigning := deriveKey(secret, parsed.Date, parsed.Region, parsed.Service)
		reader := NewChunkedReader(r.Body, parsed.Signature, parsed.Scope, amzDate, kSigning)
		r.Body = reader
		return BodyModeStreamingSigned, "STREAMING-AWS4-HMAC-SHA256-PAYLOAD", nil
	case cs == "":
		// No content-sha256 header — treat as if client signed hex(sha256("")).
		empty := sha256.Sum256(nil)
		return BodyModeSHA256, hex.EncodeToString(empty[:]), nil
	default:
		// Client claims hex(sha256(body)). Trust the declared header for
		// canonical-request construction; the actual body is verified by
		// the S3 handler hashing as it streams (Plan 07).
		if len(cs) != 64 {
			return 0, "", ErrMalformed
		}
		return BodyModeSHA256, cs, nil
	}
}

// readerNoop is a trivial NopCloser for Request bodies that arrived as
// io.NopCloser already. Exposed for tests.
func readerNoop(r io.Reader) io.ReadCloser { return io.NopCloser(r) }
