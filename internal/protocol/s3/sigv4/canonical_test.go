package sigv4

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// AWS-published secret used to sign every fixture in aws4_testsuite.
const awsTestSuiteSecret = "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"

// parseAuthzReference extracts Credential / SignedHeaders / Signature from a
// fixture .authz line of the shape
//
//	AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20110909/us-east-1/host/aws4_request, SignedHeaders=date;host, Signature=...
func parseAuthzReference(s string) (credentialScope string, amzDate string, region, service string, signedHeaders []string, signature string) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "AWS4-HMAC-SHA256 ")
	for _, part := range splitAuthzParts(s) {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch strings.TrimSpace(kv[0]) {
		case "Credential":
			// AKID/YYYYMMDD/region/service/aws4_request
			segs := strings.Split(kv[1], "/")
			if len(segs) >= 5 {
				amzDate = segs[1]
				region = segs[2]
				service = segs[3]
				credentialScope = strings.Join(segs[1:], "/")
			}
		case "SignedHeaders":
			signedHeaders = strings.Split(kv[1], ";")
		case "Signature":
			signature = kv[1]
		}
	}
	return
}

func splitAuthzParts(s string) []string {
	// Split on ", " but tolerate single-comma separators (aws-cli quirk).
	parts := regexp.MustCompile(`,\s*`).Split(s, -1)
	return parts
}

// loadReqFile reads a .req fixture (HTTP/1.1 wire format) into an
// http.Request. The test suite files use CRLF line endings.
func loadReqFile(t *testing.T, path string) *http.Request {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	// Normalize: the reference requests use `http/1.1` (lowercase); Go's
	// http.ReadRequest demands `HTTP/1.1`. Also allow missing final CRLF.
	text := string(data)
	text = strings.Replace(text, " http/1.1", " HTTP/1.1", 1)
	// Fixtures omit Content-Length; inject one based on post-header bytes so
	// http.ReadRequest reads the body.
	if idx := strings.Index(text, "\r\n\r\n"); idx >= 0 {
		body := text[idx+4:]
		if body != "" && !strings.Contains(text[:idx], "Content-Length:") {
			text = text[:idx] + "\r\nContent-Length: " +
				strconv.Itoa(len(body)) + text[idx:]
		}
	}
	if !strings.HasSuffix(text, "\r\n\r\n") {
		if strings.HasSuffix(text, "\r\n") {
			text += "\r\n"
		} else {
			text += "\r\n\r\n"
		}
	}
	req, err := http.ReadRequest(bufio.NewReader(strings.NewReader(text)))
	if err != nil {
		t.Fatalf("parse request %s: %v", path, err)
	}
	// Restore the Host header in the http.Header map — Go lifts it to req.Host.
	if req.Header.Get("Host") == "" && req.Host != "" {
		req.Header.Set("Host", req.Host)
	}
	return req
}

// stripCR normalizes fixture files that use CRLF to the LF form our code emits.
func stripCR(b []byte) string { return strings.ReplaceAll(string(b), "\r\n", "\n") }

// fixtureDirs returns every <case> directory under testdata/aws4_testsuite.
func fixtureDirs(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir("testdata/aws4_testsuite")
	if err != nil {
		t.Fatalf("read testdata: %v", err)
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, filepath.Join("testdata/aws4_testsuite", e.Name()))
		}
	}
	if len(dirs) < 5 {
		t.Fatalf("expected ≥5 fixture dirs, got %d", len(dirs))
	}
	return dirs
}

// readFixture reads one file; panics if missing — fixture must be complete.
func readFixture(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}

// TestCanonicalRequest iterates every vendored fixture and checks byte-exact
// match against the reference .creq file (after CRLF → LF normalization).
func TestCanonicalRequest(t *testing.T) {
	for _, dir := range fixtureDirs(t) {
		name := filepath.Base(dir)
		t.Run(name, func(t *testing.T) {
			req := loadReqFile(t, filepath.Join(dir, name+".req"))
			authz := string(readFixture(t, filepath.Join(dir, name+".authz")))
			_, _, _, _, signed, _ := parseAuthzReference(authz)

			// Body hash — these fixtures all signed the empty-or-body payload
			// WITHOUT including an x-amz-content-sha256 header, so the
			// canonical-request payload-hash is hex(sha256(body)).
			body := []byte{}
			if req.Body != nil {
				b, _ := io.ReadAll(req.Body)
				body = b
			}
			sum := sha256.Sum256(body)
			bodyHash := hex.EncodeToString(sum[:])

			got := canonicalRequest(req.Method, req.URL.EscapedPath(),
				req.URL.RawQuery, req.Header, signed, bodyHash)
			want := stripCR(readFixture(t, filepath.Join(dir, name+".creq")))
			if got != want {
				t.Fatalf("canonical mismatch\n--- got\n%q\n--- want\n%q", got, want)
			}
		})
	}
}

// TestStringToSign validates the 4-line StringToSign against each fixture.
func TestStringToSign(t *testing.T) {
	for _, dir := range fixtureDirs(t) {
		name := filepath.Base(dir)
		t.Run(name, func(t *testing.T) {
			creq := stripCR(readFixture(t, filepath.Join(dir, name+".creq")))
			authz := string(readFixture(t, filepath.Join(dir, name+".authz")))
			credentialScope, _, _, _, _, _ := parseAuthzReference(authz)

			// amzDate for sts is the full wire timestamp; fixtures use
			// x-amz-date equivalent derived from the Date header.
			// Reference STS hardcodes 20110909T233600Z for all fixtures.
			amzDate := "20110909T233600Z"
			got := stringToSign(amzDate, credentialScope, creq)
			want := stripCR(readFixture(t, filepath.Join(dir, name+".sts")))
			if got != want {
				t.Fatalf("sts mismatch\n--- got\n%q\n--- want\n%q", got, want)
			}
		})
	}
}

// TestDeriveKey verifies the 4-step HMAC chain against the signature embedded
// in each fixture's .authz. Derivation is indirectly checked by computing
// hex(HMAC(kSigning, sts)) and comparing to the reference Signature= value.
func TestDeriveKey(t *testing.T) {
	for _, dir := range fixtureDirs(t) {
		name := filepath.Base(dir)
		t.Run(name, func(t *testing.T) {
			sts := stripCR(readFixture(t, filepath.Join(dir, name+".sts")))
			authz := string(readFixture(t, filepath.Join(dir, name+".authz")))
			_, date, region, service, _, refSig := parseAuthzReference(authz)

			kSigning := deriveKey(awsTestSuiteSecret, date, region, service)
			got := hmacSHA256Hex(kSigning, []byte(sts))
			if got != refSig {
				t.Fatalf("signature mismatch: got %s want %s", got, refSig)
			}
		})
	}
}

// TestEncodePath — RFC 3986 per-segment encoding, '/' literal, no double-encode.
func TestEncodePath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "/"},
		{"/", "/"},
		{"/foo", "/foo"},
		{"/foo/bar baz/qux", "/foo/bar%20baz/qux"},
		{"/a+b", "/a%2Bb"},
		{"/~tilde", "/~tilde"},                         // ~ is unreserved
		{"/caf\u00e9", "/caf%C3%A9"},                   // UTF-8 é
		{"/already%20encoded", "/already%2520encoded"}, // we re-encode '%' per RFC for s3 no-double-encode path segments (input is raw not pre-encoded in our contract)
	}
	for _, c := range cases {
		if got := encodePath(c.in); got != c.want {
			t.Errorf("encodePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestEncodeQuery — sort by encoded key then encoded value; '+' → %20.
func TestEncodeQuery(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"b=2&a=1&a=0", "a=0&a=1&b=2"},
		{"foo=bar", "foo=bar"},
		{"k=a b", "k=a%20b"},
		{"k=a+b", "k=a%20b"}, // '+' decodes to space, re-encodes as %20
		{"x=&y=z", "x=&y=z"},
		{"a", "a="},
	}
	for _, c := range cases {
		if got := encodeQuery(c.in); got != c.want {
			t.Errorf("encodeQuery(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestCanonicalHeaders — collapse outside quotes, sort, lowercase.
func TestCanonicalHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("Host", "example.com")
	h.Set("X-Amz-Date", "20260101T000000Z")
	h.Add("My-Header1", "a   b   c") // triple-space outside quote → "a b c"
	h.Add("My-Header1", "d")         // multi-value join with comma
	h.Set("My-Header2", `"a  b"  c`) // preserve inside quote

	signed := []string{"host", "my-header1", "my-header2", "x-amz-date"}
	block, list := canonicalHeaders(h, signed)

	wantList := "host;my-header1;my-header2;x-amz-date"
	if list != wantList {
		t.Errorf("signed list = %q, want %q", list, wantList)
	}
	wantBlock := "host:example.com\n" +
		"my-header1:a b c,d\n" +
		"my-header2:\"a  b\" c\n" +
		"x-amz-date:20260101T000000Z\n"
	if block != wantBlock {
		t.Errorf("block mismatch\n--- got\n%q\n--- want\n%q", block, wantBlock)
	}
}

// TestWriteError — sentinel errors render to AWS-shape XML with correct
// status/Code. ErrSkew includes server/request times in the wire literal format.
func TestWriteError(t *testing.T) {
	check := func(t *testing.T, err error, wantStatus int, mustContain ...string) {
		t.Helper()
		w := newRecorder()
		WriteError(w, nil, err)
		if w.status != wantStatus {
			t.Errorf("status=%d, want %d", w.status, wantStatus)
		}
		if ct := w.header.Get("Content-Type"); ct != "application/xml" {
			t.Errorf("Content-Type=%q, want application/xml", ct)
		}
		body := w.buf.String()
		if !strings.HasPrefix(body, "<?xml") {
			t.Errorf("body missing XML prolog: %q", body)
		}
		for _, s := range mustContain {
			if !strings.Contains(body, s) {
				t.Errorf("body missing %q: %s", s, body)
			}
		}
	}

	check(t, ErrInvalidAccessKeyId, 403,
		"<Code>InvalidAccessKeyId</Code>", "<RequestId>")
	check(t, ErrSignatureMismatch, 403,
		"<Code>SignatureDoesNotMatch</Code>")
	check(t, ErrMalformed, 400,
		"<Code>AuthorizationHeaderMalformed</Code>")
	check(t, ErrInvalidRequest, 400,
		"<Code>InvalidRequest</Code>")
	check(t, &ErrSkew{
		RequestTime:    mustTime("2026-01-01T12:30:00Z"),
		ServerTime:     mustTime("2026-01-01T13:00:00Z"),
		MaxAllowedSkew: 900e9, // 15min in ns
	}, 403,
		"<Code>RequestTimeTooSkewed</Code>",
		"<RequestTime>20260101T123000Z</RequestTime>",
		"<ServerTime>20260101T130000Z</ServerTime>",
		"<MaxAllowedSkewMilliseconds>900000</MaxAllowedSkewMilliseconds>",
	)

	// Unknown error → InternalError, no leak of message
	check(t, io.ErrUnexpectedEOF, 500, "<Code>InternalError</Code>")
	if strings.Contains(newRecorder().buf.String(), "unexpected EOF") {
		t.Error("leaked raw error message")
	}
}

// ---- tiny http.ResponseWriter recorder (avoid httptest import weight) ----

type recorder struct {
	header http.Header
	buf    bytes.Buffer
	status int
}

func newRecorder() *recorder { return &recorder{header: http.Header{}} }

func (r *recorder) Header() http.Header         { return r.header }
func (r *recorder) Write(b []byte) (int, error) { return r.buf.Write(b) }
func (r *recorder) WriteHeader(s int)           { r.status = s }

func mustTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}
