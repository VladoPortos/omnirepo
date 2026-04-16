---
phase: 04-s3-git
plan: 04
subsystem: sigv4-verifier
tags: [sigv4, crypto, s3, security, streaming]
requires:
  - 04-01  # aws4_testsuite fixtures + probe vendoring
provides:
  - api:sigv4.Verify
  - api:sigv4.NewChunkedReader
  - api:sigv4.WriteError
  - api:sigv4.VerifyResult
  - api:sigv4.BodyMode
  - api:sigv4.SecretLookup
  - sentinel:sigv4.ErrMalformed
  - sentinel:sigv4.ErrInvalidAccessKeyId
  - sentinel:sigv4.ErrSignatureMismatch
  - sentinel:sigv4.ErrInvalidRequest
  - sentinel:sigv4.ErrSkew
affects:
  - internal/protocol/s3/sigv4/
tech_stack_added: []
tech_stack_patterns:
  - hand-rolled SigV4 verifier (no aws-sdk-go dep)
  - per-chunk HMAC signature chain parser (io.Reader wrapper)
  - AWS-shape XML error renderer with request-ID correlation
  - nowFn injection for deterministic skew tests
key_files_created:
  - internal/protocol/s3/sigv4/canonical.go
  - internal/protocol/s3/sigv4/canonical_test.go
  - internal/protocol/s3/sigv4/errors.go
  - internal/protocol/s3/sigv4/verify.go
  - internal/protocol/s3/sigv4/verify_test.go
  - internal/protocol/s3/sigv4/chunked.go
key_files_modified: []
decisions:
  - "Constant-time compare collapses both the final signature check and the per-chunk chunk-signature check (subtle.ConstantTimeCompare)"
  - "64 MiB per-chunk size cap — rejects DoS attempts (T-04-04-06); chosen as 4x gofakes3's default streaming-buffer ceiling so real aws-cli chunks (~64 KiB default) aren't near the cap"
  - "SecretLookup contract collapses missing+revoked into ErrInvalidAccessKeyId at the caller boundary — no oracle (D-12)"
  - "WriteError always writes XML prolog + 8-byte hex RequestId; x-amz-request-id echoed in response header for log correlation"
  - "nowFn is package-level (test override); production callers never see it"
  - "rfc3986Escape builds the percent-encoder by hand rather than calling url.QueryEscape (canonical rule is '%20' for space, not '+')"
  - "trimCollapseWS preserves whitespace inside double-quoted substrings — matches AWS canonical-headers rule"
metrics:
  duration_minutes: 20
  tasks: 2
  files_created: 6
  files_modified: 0
  commits: 2
completed_date: "2026-04-16"
---

# Phase 04 Plan 04: SigV4 Verifier Summary

One-liner: hand-rolled AWS SigV4 verifier (authorization-header parsing,
canonical-request / string-to-sign / HMAC-SHA256 key-chain, constant-time
signature compare, three body-hash modes including a STREAMING chunked-body
parser) with AWS-shape XML error rendering, all green against the five
vendored AWS4 test vectors + 14 additional behavioral tests.

## Objective (restated)

Land the D-09 SigV4 verifier as a self-contained `internal/protocol/s3/sigv4/`
package so Plan 07 can mount it as middleware in front of gofakes3. The
verifier must pass every AWS SigV4 test vector byte-for-byte, handle all
three body-hash modes aws-cli / aws-sdk-go-v2 emit, and render AWS-shape
XML errors (including the exact RequestTimeTooSkewed envelope in D-11 format).

## What landed

### Task 1 — canonical.go + errors.go (commit d4ecf89)

- **canonical.go** (~180 LOC): six pure functions — `canonicalRequest`,
  `stringToSign`, `deriveKey`, `encodePath`, `encodeQuery`, `canonicalHeaders`
  plus the `hmacSHA256` / `hmacSHA256Hex` helpers and the internal
  `rfc3986Escape` / `trimCollapseWS` primitives.
- **errors.go** (~110 LOC): sentinel errors (`ErrMalformed`,
  `ErrInvalidAccessKeyId`, `ErrSignatureMismatch`, `ErrInvalidRequest`) +
  `ErrSkew` struct carrying request/server times and max-allowed skew; the
  `awsError` XML struct + `WriteError(w, r, err)` renderer that dispatches
  on error type to the correct AWS Code + status.
- **canonical_test.go** (~330 LOC): table-driven fixture iteration over every
  `testdata/aws4_testsuite/<case>/` directory (`get-vanilla`, `post-vanilla`,
  `post-vanilla-query`, `get-header-value-trim`,
  `post-x-www-form-urlencoded-parameters`) — each case validates
  canonical-request, string-to-sign, AND derived signature byte-for-byte
  against the vendored `.creq` / `.sts` / `.authz` fixtures. Plus unit tests
  for encodePath, encodeQuery, canonicalHeaders, and WriteError (including
  the D-11 RequestTimeTooSkewed envelope).

### Task 2 — verify.go + chunked.go (commit 005c1e0)

- **verify.go** (~200 LOC): `Verify(r, lookup, skew) (*VerifyResult, error)`
  — parses Authorization (tolerant of trailing commas + odd whitespace real
  aws-cli emits), enforces `service="s3"` (D-13) — any other service →
  `ErrInvalidRequest`. Parses x-amz-date, checks ±skew window (both future
  and past directions), dispatches on `x-amz-content-sha256` to set
  `BodyMode`, wraps the body with the chunked reader for STREAMING mode,
  and finishes with a `subtle.ConstantTimeCompare` on the final signature.
- **chunked.go** (~140 LOC): `NewChunkedReader(body, seedSig, scope, amzDate,
  kSigning) io.ReadCloser` — a bufio-backed reader that lazily parses each
  chunk header line, verifies the per-chunk signature against the chain
  (seeded by the header signature, then each chunk's sig chains to the
  next), enforces a 64 MiB per-chunk cap, and requires the terminal
  zero-size chunk to carry a valid signature over the empty body. Uses
  `crypto/subtle.ConstantTimeCompare` for each chunk compare.
- **verify_test.go** (~310 LOC): 14 scenarios mapping 1:1 to the plan's
  behavior list — happy path (Unsigned + SHA256 body modes), wrong secret,
  missing Authorization, Authorization lacking `Signature=`, unknown AKID,
  skew future / past, tamper detection (post-sign
  `x-amz-content-sha256` mutation), wrong-service rejection, STREAMING
  3-chunk happy path, tampered middle-chunk signature, missing terminal
  chunk, and a single-byte reader that forces chunk-header parsing across
  bufio boundaries.

## Fixtures used

All five AWS `aws4_testsuite` cases vendored in Plan 01:

| Case | Validates |
|------|-----------|
| `get-vanilla` | GET with `Date`+`Host`, empty body |
| `post-vanilla` | POST with same two headers, empty body |
| `post-vanilla-query` | POST with `?foo=bar` (canonical query string) |
| `get-header-value-trim` | Header value inner-whitespace collapse (`p: phfft ` → `phfft`) |
| `post-x-www-form-urlencoded-parameters` | Body of `foo=bar` + `Content-Type` header inclusion in SignedHeaders |

All five cases pass byte-exact for `.creq` and `.sts` files (after CRLF → LF
normalization — test vectors use CRLF line endings on disk, AWS canonical
spec uses LF), and the derived signature matches the reference `.authz`
`Signature=` token exactly.

## Encoder edge cases discovered

1. **Query `+` → `%20`**: `url.QueryEscape` emits `+` for space, which AWS
   canonicalization rejects. Our `rfc3986Escape` hand-walks the byte
   sequence and percent-encodes anything outside `[A-Za-z0-9-._~]`. A
   dedicated `encodeQuery` test round-trips `k=a+b` → `k=a%20b` to pin
   this.
2. **Path `%` literal is re-encoded**: our contract is that the caller
   passes the RAW path (post-URL-decode); if the URL arrives with
   `%20` pre-encoded, `encodePath` treats it as the literal bytes `% 2 0`
   and produces `%2520`. The `Verify()` caller passes `r.URL.EscapedPath()`
   so this matches real-world S3 client behavior (they send
   `/foo%20bar` which arrives as escaped).
3. **Header CRLF vs LF**: the vendored fixture `.creq` / `.sts` files use
   CRLF; our canonicalRequest builds LF-only output (per AWS spec); test
   harness normalizes fixtures via `strings.ReplaceAll(s, "\r\n", "\n")`
   before the byte-exact compare.
4. **Inner-whitespace collapse respects quotes**: `"a  b"  c` (two spaces
   inside quotes, two outside) canonicalizes to `"a  b" c` — preserves
   quoted-substring whitespace exactly per AWS rule. Unit-tested.
5. **Empty x-amz-content-sha256**: some minimal clients omit the header
   entirely. `dispatchBody` treats missing as `hex(sha256(""))` (empty
   body) — same outcome as if the client explicitly signed an empty
   payload.

## Per-chunk size cap

**64 MiB (`maxChunkSize`)** — documented in `chunked.go`. Rationale: aws-cli
and aws-sdk-go-v2 emit STREAMING chunks at ~64 KiB by default (3 orders of
magnitude under the cap); 64 MiB gives headroom for any future tuning but
immediately rejects a malicious client declaring a multi-GiB chunk size
before we've allocated a buffer (T-04-04-06 DoS mitigation).

## Auth gates

None. Plan was fully autonomous.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 — Bug] test case for content-sha256 tamper self-signed the wrong hash**

- **Found during:** Task 2 test execution.
- **Issue:** My first attempt at `TestVerify_SHA256BodyMode`'s tamper
  scenario called `signRequest(t, r2, now, body, strings.Repeat("f", 64))`
  — which signs canonical-request using the tampered hash as the bodyHash.
  The signer and verifier agreed, so `Verify` correctly returned no error,
  failing the test.
- **Fix:** Sign normally with the correct hash, THEN mutate
  `x-amz-content-sha256` on the request object post-sign. Because
  `SignedHeaders` includes `x-amz-content-sha256`, the canonical-request
  the verifier reconstructs differs from what the signer saw → signature
  mismatch. This now matches the real threat model (attacker flips the
  header in transit).
- **Files:** `internal/protocol/s3/sigv4/verify_test.go`
- **Commit:** 005c1e0

**2. [Rule 3 — Blocking] fixtures omit Content-Length; http.ReadRequest yields empty body**

- **Found during:** Task 1 canonical-request fixture test —
  `post-x-www-form-urlencoded-parameters` fixture failed because Go's
  `http.ReadRequest` can't auto-read a body without a `Content-Length`
  header (the AWS fixture .req files predate the convention).
- **Fix:** `loadReqFile` synthesizes a `Content-Length: <len>` header from
  the post-header bytes of the fixture before feeding to
  `http.ReadRequest`. All five fixtures now parse and the body is
  available for sha256 hashing.
- **Files:** `internal/protocol/s3/sigv4/canonical_test.go`
- **Commit:** d4ecf89

### Out-of-scope discoveries

None — the sigv4 package is brand-new; no adjacent code needed adjustment.

## Known Stubs

None. The package is entirely production-ready:

- `Verify()` is pure (no side effects beyond wrapping r.Body for STREAMING).
- `WriteError()` writes to the caller-provided ResponseWriter.
- `NewChunkedReader` is a real io.ReadCloser; reads stream through, no
  accumulation beyond the single-chunk buffer.

No TODO/FIXME markers; no placeholder returns.

## Verification

- `go test ./internal/protocol/s3/sigv4/... -count=1` → PASS (all fixture
  sub-tests + 14 Verify/Chunked scenarios + Task-1 unit tests)
- `go build ./...` → clean (no regressions elsewhere in the module)
- All acceptance-criteria greps from the plan satisfied:
  - `^func canonicalRequest` = 1, `^func stringToSign` = 1,
    `^func deriveKey` = 1 in canonical.go
  - `RequestTimeTooSkewed` × 3, `MaxAllowedSkewMilliseconds` × 2,
    `application/xml` × 2 in errors.go
  - `^func Verify` = 1, `subtle.ConstantTimeCompare` = 1,
    `UNSIGNED-PAYLOAD` × 4, `STREAMING-AWS4-HMAC-SHA256-PAYLOAD` × 4 in
    verify.go
  - `^func NewChunkedReader` = 1, `chunk-signature=` × 3 in chunked.go

## Self-Check: PASSED

- `internal/protocol/s3/sigv4/canonical.go` — FOUND
- `internal/protocol/s3/sigv4/canonical_test.go` — FOUND
- `internal/protocol/s3/sigv4/errors.go` — FOUND
- `internal/protocol/s3/sigv4/verify.go` — FOUND
- `internal/protocol/s3/sigv4/verify_test.go` — FOUND
- `internal/protocol/s3/sigv4/chunked.go` — FOUND
- Commit d4ecf89 — FOUND
- Commit 005c1e0 — FOUND

## What Plan 07+ can assume

- `sigv4.Verify(r, lookup, 15*time.Minute)` is the single entry point to
  mount as chi middleware on the `/s3/*` mount point.
- For STREAMING requests the caller MUST read `r.Body` through completion
  — truncating early returns the plaintext bytes already verified but
  bypasses the terminal-chunk check. Plan 07's handler will wrap with
  `io.Copy` into the storage backend (which reads to EOF).
- `WriteError(w, r, err)` handles the full error → HTTP-response mapping.
  Plan 07 need only `switch { case err := sigv4.Verify(...); err != nil:
  sigv4.WriteError(w, r, err); return }`.
- Secret lookup is the caller's responsibility. Phase 4 Plan 05 will
  provide the `S3KeysRepo`-backed implementation (`FindByAKID` →
  AEAD-decrypt → return plaintext); that repo was already landed in Plan
  02 with `ErrS3AccessKeyNotFound` collapsing missing+revoked per D-12.
