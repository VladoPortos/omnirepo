package sigv4

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Test vectors for a live, self-signed request against our own math. We
// construct a request, sign it with AWS SigV4 rules ourselves, then Verify.
// This exercises the full pipeline (parse Authorization → skew → lookup →
// canonical → sts → key → compare).

const (
	testAKID    = "AKIAIOSFODNN7EXAMPLE"
	testSecret  = "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"
	testRegion  = "us-east-1"
	testService = "s3"
	testHost    = "bucket.s3.example.com"
)

// signRequest attaches a valid x-amz-date, x-amz-content-sha256, and
// Authorization header to r using testSecret.
func signRequest(t *testing.T, r *http.Request, now time.Time, body []byte, contentSHA string) {
	t.Helper()
	amzDate := now.UTC().Format(amzTimeFmt)
	date := amzDate[:8]
	r.Header.Set("Host", testHost)
	r.Host = testHost
	r.Header.Set("x-amz-date", amzDate)
	if contentSHA == "" {
		h := sha256.Sum256(body)
		contentSHA = hex.EncodeToString(h[:])
	}
	r.Header.Set("x-amz-content-sha256", contentSHA)

	signed := []string{"host", "x-amz-content-sha256", "x-amz-date"}

	bodyHash := contentSHA
	canonReq := canonicalRequest(r.Method, r.URL.EscapedPath(), r.URL.RawQuery,
		r.Header, signed, bodyHash)
	scope := date + "/" + testRegion + "/" + testService + "/aws4_request"
	sts := stringToSign(amzDate, scope, canonReq)
	kSigning := deriveKey(testSecret, date, testRegion, testService)
	sig := hmacSHA256Hex(kSigning, []byte(sts))

	r.Header.Set("Authorization",
		"AWS4-HMAC-SHA256 "+
			"Credential="+testAKID+"/"+scope+", "+
			"SignedHeaders="+strings.Join(signed, ";")+", "+
			"Signature="+sig)
}

// testLookup is a simple SecretLookup that returns testSecret for testAKID.
func testLookup(akid string) (string, error) {
	if akid == testAKID {
		return testSecret, nil
	}
	return "", errors.New("not found")
}

// withFrozenTime overrides nowFn for the duration of fn.
func withFrozenTime(t *testing.T, now time.Time, fn func()) {
	t.Helper()
	orig := nowFn
	nowFn = func() time.Time { return now }
	defer func() { nowFn = orig }()
	fn()
}

func makeReq(method, target string, body []byte) *http.Request {
	r, _ := http.NewRequest(method, "https://"+testHost+target, bytes.NewReader(body))
	r.Body = io.NopCloser(bytes.NewReader(body))
	return r
}

func TestVerify_HappyPathUnsigned(t *testing.T) {
	now := time.Date(2026, 4, 16, 12, 0, 0, 0, time.UTC)
	r := makeReq("GET", "/object.txt", nil)
	signRequest(t, r, now, nil, "UNSIGNED-PAYLOAD")

	withFrozenTime(t, now, func() {
		res, err := Verify(r, testLookup, 15*time.Minute)
		if err != nil {
			t.Fatalf("Verify: %v", err)
		}
		if res.AccessKeyID != testAKID {
			t.Errorf("AKID=%q", res.AccessKeyID)
		}
		if res.BodyMode != BodyModeUnsignedPayload {
			t.Errorf("BodyMode=%d, want Unsigned", res.BodyMode)
		}
	})
}

func TestVerify_WrongSecret(t *testing.T) {
	now := time.Date(2026, 4, 16, 12, 0, 0, 0, time.UTC)
	r := makeReq("GET", "/x", nil)
	signRequest(t, r, now, nil, "UNSIGNED-PAYLOAD")

	bogus := func(akid string) (string, error) { return "not-the-right-secret", nil }
	withFrozenTime(t, now, func() {
		_, err := Verify(r, bogus, 15*time.Minute)
		if !errors.Is(err, ErrSignatureMismatch) {
			t.Fatalf("err=%v, want ErrSignatureMismatch", err)
		}
	})
}

func TestVerify_MissingAuthorization(t *testing.T) {
	r := makeReq("GET", "/x", nil)
	_, err := Verify(r, testLookup, 15*time.Minute)
	if !errors.Is(err, ErrMalformed) {
		t.Fatalf("err=%v, want ErrMalformed", err)
	}
}

func TestVerify_AuthorizationNoSignature(t *testing.T) {
	r := makeReq("GET", "/x", nil)
	r.Header.Set("Authorization",
		"AWS4-HMAC-SHA256 Credential=AKIA/20260101/us-east-1/s3/aws4_request, SignedHeaders=host")
	r.Header.Set("x-amz-date", "20260101T000000Z")
	_, err := Verify(r, testLookup, 15*time.Minute)
	if !errors.Is(err, ErrMalformed) {
		t.Fatalf("err=%v, want ErrMalformed", err)
	}
}

func TestVerify_UnknownAKID(t *testing.T) {
	now := time.Date(2026, 4, 16, 12, 0, 0, 0, time.UTC)
	r := makeReq("GET", "/x", nil)
	signRequest(t, r, now, nil, "UNSIGNED-PAYLOAD")

	lookup := func(akid string) (string, error) { return "", errors.New("revoked") }
	withFrozenTime(t, now, func() {
		_, err := Verify(r, lookup, 15*time.Minute)
		if !errors.Is(err, ErrInvalidAccessKeyId) {
			t.Fatalf("err=%v, want ErrInvalidAccessKeyId", err)
		}
	})
}

func TestVerify_SkewFuture(t *testing.T) {
	now := time.Date(2026, 4, 16, 12, 0, 0, 0, time.UTC)
	future := now.Add(16 * time.Minute)
	r := makeReq("GET", "/x", nil)
	signRequest(t, r, future, nil, "UNSIGNED-PAYLOAD")

	withFrozenTime(t, now, func() {
		_, err := Verify(r, testLookup, 15*time.Minute)
		var se *ErrSkew
		if !errors.As(err, &se) {
			t.Fatalf("err=%v, want *ErrSkew", err)
		}
		if !se.ServerTime.Equal(now) {
			t.Errorf("ServerTime=%v, want %v", se.ServerTime, now)
		}
	})
}

func TestVerify_SkewPast(t *testing.T) {
	now := time.Date(2026, 4, 16, 12, 0, 0, 0, time.UTC)
	past := now.Add(-20 * time.Minute)
	r := makeReq("GET", "/x", nil)
	signRequest(t, r, past, nil, "UNSIGNED-PAYLOAD")

	withFrozenTime(t, now, func() {
		_, err := Verify(r, testLookup, 15*time.Minute)
		var se *ErrSkew
		if !errors.As(err, &se) {
			t.Fatalf("err=%v, want *ErrSkew", err)
		}
	})
}

func TestVerify_UnsignedPayloadSkipsBodyCheck(t *testing.T) {
	now := time.Date(2026, 4, 16, 12, 0, 0, 0, time.UTC)
	body := []byte("the body")
	r := makeReq("PUT", "/k", body)
	signRequest(t, r, now, body, "UNSIGNED-PAYLOAD")

	withFrozenTime(t, now, func() {
		res, err := Verify(r, testLookup, 15*time.Minute)
		if err != nil {
			t.Fatalf("Verify: %v", err)
		}
		if res.BodyMode != BodyModeUnsignedPayload {
			t.Errorf("BodyMode=%d", res.BodyMode)
		}
	})
}

func TestVerify_SHA256BodyMode(t *testing.T) {
	now := time.Date(2026, 4, 16, 12, 0, 0, 0, time.UTC)
	body := []byte("payload")
	r := makeReq("PUT", "/k", body)
	signRequest(t, r, now, body, "")

	withFrozenTime(t, now, func() {
		res, err := Verify(r, testLookup, 15*time.Minute)
		if err != nil {
			t.Fatalf("Verify: %v", err)
		}
		if res.BodyMode != BodyModeSHA256 {
			t.Errorf("BodyMode=%d, want SHA256", res.BodyMode)
		}
	})

	// Tamper: attacker rewrites x-amz-content-sha256 after signing. The
	// header is part of canonicalRequest (via SignedHeaders=host;
	// x-amz-content-sha256; x-amz-date), so mutating it post-sign breaks
	// the signature.
	r2 := makeReq("PUT", "/k", body)
	signRequest(t, r2, now, body, "")
	r2.Header.Set("x-amz-content-sha256", strings.Repeat("f", 64))
	withFrozenTime(t, now, func() {
		_, err := Verify(r2, testLookup, 15*time.Minute)
		if !errors.Is(err, ErrSignatureMismatch) {
			t.Fatalf("expected signature mismatch after tampered content-sha256, got %v", err)
		}
	})
}

// ---- PayloadSHA256 plumbing tests ----

// TestVerify_PayloadSHA256_HexMode asserts that VerifyResult.PayloadSHA256
// carries the literal hex(sha256(body)) header value the client signed when
// the request used standard SigV4 SHA mode.
func TestVerify_PayloadSHA256_HexMode(t *testing.T) {
	now := time.Date(2026, 4, 16, 12, 0, 0, 0, time.UTC)
	body := []byte("payload-bytes")
	want := func() string {
		h := sha256.Sum256(body)
		return hex.EncodeToString(h[:])
	}()
	r := makeReq("PUT", "/k", body)
	signRequest(t, r, now, body, "")

	withFrozenTime(t, now, func() {
		res, err := Verify(r, testLookup, 15*time.Minute)
		if err != nil {
			t.Fatalf("Verify: %v", err)
		}
		if res.BodyMode != BodyModeSHA256 {
			t.Errorf("BodyMode=%d, want SHA256", res.BodyMode)
		}
		if res.PayloadSHA256 != want {
			t.Errorf("PayloadSHA256=%q, want %q", res.PayloadSHA256, want)
		}
	})
}

// TestVerify_PayloadSHA256_Unsigned asserts the literal "UNSIGNED-PAYLOAD"
// sentinel propagates verbatim into VerifyResult.PayloadSHA256 — downstream
// PutObject reads this to skip the post-write SHA compare.
func TestVerify_PayloadSHA256_Unsigned(t *testing.T) {
	now := time.Date(2026, 4, 16, 12, 0, 0, 0, time.UTC)
	body := []byte("the body")
	r := makeReq("PUT", "/k", body)
	signRequest(t, r, now, body, "UNSIGNED-PAYLOAD")

	withFrozenTime(t, now, func() {
		res, err := Verify(r, testLookup, 15*time.Minute)
		if err != nil {
			t.Fatalf("Verify: %v", err)
		}
		if res.BodyMode != BodyModeUnsignedPayload {
			t.Errorf("BodyMode=%d, want Unsigned", res.BodyMode)
		}
		if res.PayloadSHA256 != "UNSIGNED-PAYLOAD" {
			t.Errorf("PayloadSHA256=%q, want UNSIGNED-PAYLOAD", res.PayloadSHA256)
		}
	})
}

// TestVerify_PayloadSHA256_Streaming asserts the
// "STREAMING-AWS4-HMAC-SHA256-PAYLOAD" sentinel propagates verbatim — the
// chunk verifier already runs inline; PutObject must skip the post-write
// compare.
func TestVerify_PayloadSHA256_Streaming(t *testing.T) {
	now := time.Date(2026, 4, 16, 12, 0, 0, 0, time.UTC)
	r := makeReq("PUT", "/k", nil)
	// Build a SigV4 signature for a STREAMING request. The canonical-request
	// payload-hash slot is the literal sentinel; the actual chunked body is
	// handed off to NewChunkedReader inside dispatchBody.
	amzDate := now.UTC().Format(amzTimeFmt)
	date := amzDate[:8]
	r.Header.Set("Host", testHost)
	r.Host = testHost
	r.Header.Set("x-amz-date", amzDate)
	r.Header.Set("x-amz-content-sha256", "STREAMING-AWS4-HMAC-SHA256-PAYLOAD")
	signed := []string{"host", "x-amz-content-sha256", "x-amz-date"}
	canonReq := canonicalRequest(r.Method, r.URL.EscapedPath(), r.URL.RawQuery,
		r.Header, signed, "STREAMING-AWS4-HMAC-SHA256-PAYLOAD")
	scope := date + "/" + testRegion + "/" + testService + "/aws4_request"
	sts := stringToSign(amzDate, scope, canonReq)
	kSigning := deriveKey(testSecret, date, testRegion, testService)
	sig := hmacSHA256Hex(kSigning, []byte(sts))
	r.Header.Set("Authorization",
		"AWS4-HMAC-SHA256 Credential="+testAKID+"/"+scope+
			", SignedHeaders="+strings.Join(signed, ";")+", Signature="+sig)

	withFrozenTime(t, now, func() {
		res, err := Verify(r, testLookup, 15*time.Minute)
		if err != nil {
			t.Fatalf("Verify: %v", err)
		}
		if res.BodyMode != BodyModeStreamingSigned {
			t.Errorf("BodyMode=%d, want Streaming", res.BodyMode)
		}
		if res.PayloadSHA256 != "STREAMING-AWS4-HMAC-SHA256-PAYLOAD" {
			t.Errorf("PayloadSHA256=%q, want STREAMING-AWS4-HMAC-SHA256-PAYLOAD",
				res.PayloadSHA256)
		}
	})
}

// TestVerify_PayloadSHA256_EmptyHeader asserts that when the client omits
// x-amz-content-sha256, the verifier reports PayloadSHA256 = hex(sha256(""))
// — matching the implicit value the client signed.
func TestVerify_PayloadSHA256_EmptyHeader(t *testing.T) {
	now := time.Date(2026, 4, 16, 12, 0, 0, 0, time.UTC)
	wantEmpty := func() string {
		h := sha256.Sum256(nil)
		return hex.EncodeToString(h[:])
	}()

	// Build a request that signs with hex(sha256("")) but DOES NOT set the
	// x-amz-content-sha256 header on the wire — the verifier infers the
	// implicit empty-body hash. Note: signedHeaders MUST omit
	// x-amz-content-sha256 in this branch (the client never sent it).
	r := makeReq("GET", "/k", nil)
	amzDate := now.UTC().Format(amzTimeFmt)
	date := amzDate[:8]
	r.Header.Set("Host", testHost)
	r.Host = testHost
	r.Header.Set("x-amz-date", amzDate)
	signed := []string{"host", "x-amz-date"}
	canonReq := canonicalRequest(r.Method, r.URL.EscapedPath(), r.URL.RawQuery,
		r.Header, signed, wantEmpty)
	scope := date + "/" + testRegion + "/" + testService + "/aws4_request"
	sts := stringToSign(amzDate, scope, canonReq)
	kSigning := deriveKey(testSecret, date, testRegion, testService)
	sig := hmacSHA256Hex(kSigning, []byte(sts))
	r.Header.Set("Authorization",
		"AWS4-HMAC-SHA256 Credential="+testAKID+"/"+scope+
			", SignedHeaders="+strings.Join(signed, ";")+", Signature="+sig)

	withFrozenTime(t, now, func() {
		res, err := Verify(r, testLookup, 15*time.Minute)
		if err != nil {
			t.Fatalf("Verify: %v", err)
		}
		if res.BodyMode != BodyModeSHA256 {
			t.Errorf("BodyMode=%d, want SHA256", res.BodyMode)
		}
		if res.PayloadSHA256 != wantEmpty {
			t.Errorf("PayloadSHA256=%q, want %q", res.PayloadSHA256, wantEmpty)
		}
	})
}

func TestVerify_WrongService(t *testing.T) {
	now := time.Date(2026, 4, 16, 12, 0, 0, 0, time.UTC)
	r := makeReq("GET", "/x", nil)
	// Sign under service=dynamodb
	amzDate := now.UTC().Format(amzTimeFmt)
	date := amzDate[:8]
	r.Header.Set("Host", testHost)
	r.Host = testHost
	r.Header.Set("x-amz-date", amzDate)
	r.Header.Set("x-amz-content-sha256", "UNSIGNED-PAYLOAD")
	signed := []string{"host", "x-amz-content-sha256", "x-amz-date"}
	canonReq := canonicalRequest(r.Method, "/x", "", r.Header, signed, "UNSIGNED-PAYLOAD")
	scope := date + "/" + testRegion + "/dynamodb/aws4_request"
	sts := stringToSign(amzDate, scope, canonReq)
	kSigning := deriveKey(testSecret, date, testRegion, "dynamodb")
	sig := hmacSHA256Hex(kSigning, []byte(sts))
	r.Header.Set("Authorization",
		"AWS4-HMAC-SHA256 Credential="+testAKID+"/"+scope+
			", SignedHeaders="+strings.Join(signed, ";")+", Signature="+sig)

	withFrozenTime(t, now, func() {
		_, err := Verify(r, testLookup, 15*time.Minute)
		if !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("err=%v, want ErrInvalidRequest", err)
		}
	})
}

// ---- STREAMING tests (chunked reader) ----

// buildStreamingBody constructs the on-wire body for a 3-chunk STREAMING
// request: two data chunks + terminal zero-size chunk. Returns wire bytes,
// concatenated plaintext, and the seed signature (for Authorization header).
// The function also returns the x-amz-date we baked in so callers sign with
// the same timestamp.
func buildStreamingBody(t *testing.T, data [][]byte, seedSig, scope, amzDate string, kSigning []byte) ([]byte, []byte) {
	t.Helper()
	var wire bytes.Buffer
	var plain bytes.Buffer
	prev := seedSig
	for _, d := range data {
		plain.Write(d)
		h := sha256.Sum256(d)
		sts := "AWS4-HMAC-SHA256-PAYLOAD\n" + amzDate + "\n" + scope + "\n" + prev +
			"\n" + emptySHA256Hex + "\n" + hex.EncodeToString(h[:])
		sig := hmacSHA256Hex(kSigning, []byte(sts))
		wire.WriteString(strconvHex(int64(len(d))) + ";chunk-signature=" + sig + "\r\n")
		wire.Write(d)
		wire.WriteString("\r\n")
		prev = sig
	}
	// Terminal zero-size chunk.
	h := sha256.Sum256(nil)
	sts := "AWS4-HMAC-SHA256-PAYLOAD\n" + amzDate + "\n" + scope + "\n" + prev +
		"\n" + emptySHA256Hex + "\n" + hex.EncodeToString(h[:])
	sig := hmacSHA256Hex(kSigning, []byte(sts))
	wire.WriteString("0;chunk-signature=" + sig + "\r\n\r\n")
	return wire.Bytes(), plain.Bytes()
}

func strconvHex(n int64) string {
	const hexDigits = "0123456789abcdef"
	if n == 0 {
		return "0"
	}
	var b [16]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = hexDigits[n&0xF]
		n >>= 4
	}
	return string(b[i:])
}

// TestChunked_Happy3Chunks verifies the STREAMING reader emits the original
// bytes concatenated across chunks.
func TestChunked_Happy3Chunks(t *testing.T) {
	amzDate := "20260416T120000Z"
	scope := "20260416/us-east-1/s3/aws4_request"
	kSigning := deriveKey(testSecret, "20260416", "us-east-1", "s3")
	seed := "seedsigseedsigseedsigseedsigseedsigseedsigseedsigseedsigseedsig0"
	data := [][]byte{
		bytes.Repeat([]byte("A"), 1024),
		bytes.Repeat([]byte("B"), 2048),
	}
	wire, plain := buildStreamingBody(t, data, seed, scope, amzDate, kSigning)

	r := NewChunkedReader(io.NopCloser(bytes.NewReader(wire)), seed, scope, amzDate, kSigning)
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("bytes mismatch: got %d bytes want %d", len(got), len(plain))
	}
	if err := r.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
}

func TestChunked_TamperedMiddleSig(t *testing.T) {
	amzDate := "20260416T120000Z"
	scope := "20260416/us-east-1/s3/aws4_request"
	kSigning := deriveKey(testSecret, "20260416", "us-east-1", "s3")
	seed := strings.Repeat("a", 64)
	data := [][]byte{[]byte("first"), []byte("second")}
	wire, _ := buildStreamingBody(t, data, seed, scope, amzDate, kSigning)
	// Corrupt the second chunk's signature byte.
	// Find second "chunk-signature=" occurrence and flip one hex digit.
	idx1 := bytes.Index(wire, []byte("chunk-signature="))
	idx2 := bytes.Index(wire[idx1+1:], []byte("chunk-signature=")) + idx1 + 1
	flipPos := idx2 + len("chunk-signature=")
	if wire[flipPos] == 'a' {
		wire[flipPos] = 'b'
	} else {
		wire[flipPos] = 'a'
	}

	r := NewChunkedReader(io.NopCloser(bytes.NewReader(wire)), seed, scope, amzDate, kSigning)
	_, err := io.ReadAll(r)
	if !errors.Is(err, ErrSignatureMismatch) {
		t.Fatalf("err=%v, want ErrSignatureMismatch", err)
	}
}

func TestChunked_MissingTerminalChunk(t *testing.T) {
	amzDate := "20260416T120000Z"
	scope := "20260416/us-east-1/s3/aws4_request"
	kSigning := deriveKey(testSecret, "20260416", "us-east-1", "s3")
	seed := strings.Repeat("b", 64)
	data := [][]byte{[]byte("only-chunk")}
	wire, _ := buildStreamingBody(t, data, seed, scope, amzDate, kSigning)
	// Strip the terminal zero chunk (everything from last "0;chunk-signature=").
	cut := bytes.LastIndex(wire, []byte("0;chunk-signature="))
	wire = wire[:cut]

	r := NewChunkedReader(io.NopCloser(bytes.NewReader(wire)), seed, scope, amzDate, kSigning)
	_, err := io.ReadAll(r)
	if err == nil {
		t.Fatalf("want error on missing terminal chunk, got nil")
	}
}

// TestChunked_BufferBoundary forces the chunk header to span a bufio boundary
// by feeding bytes one at a time through a reader that yields single bytes.
func TestChunked_BufferBoundary(t *testing.T) {
	amzDate := "20260416T120000Z"
	scope := "20260416/us-east-1/s3/aws4_request"
	kSigning := deriveKey(testSecret, "20260416", "us-east-1", "s3")
	seed := strings.Repeat("c", 64)
	data := [][]byte{bytes.Repeat([]byte("x"), 1)}
	wire, plain := buildStreamingBody(t, data, seed, scope, amzDate, kSigning)

	slow := &byteByByteReader{data: wire}
	r := NewChunkedReader(io.NopCloser(slow), seed, scope, amzDate, kSigning)
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("mismatch: %q vs %q", got, plain)
	}
}

type byteByByteReader struct {
	data []byte
	i    int
}

func (b *byteByByteReader) Read(p []byte) (int, error) {
	if b.i >= len(b.data) {
		return 0, io.EOF
	}
	p[0] = b.data[b.i]
	b.i++
	return 1, nil
}
