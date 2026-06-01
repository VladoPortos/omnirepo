package s3_test

// H-10 regression: aws-chunked (STREAMING-AWS4-HMAC-SHA256-PAYLOAD) uploads.
//
// SigV4Middleware must hand gofakes3 an ALREADY de-framed + signature-verified
// body and rewrite x-amz-content-sha256 to UNSIGNED-PAYLOAD, so gofakes3 does
// not wrap the body in a SECOND chunked reader (which would Fscanf on the
// de-framed bytes — the path-style corruption bug) and so the verified reader
// reaches the request that is actually forwarded (the vhost gap). It must also
// rewrite Content-Length to the decoded payload length, since gofakes3 reads
// object size from Content-Length on the non-streaming path.

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	s3 "github.com/vladoportos/omnirepo/internal/protocol/s3"
)

const emptySHA256HexTest = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

func TestSigV4Middleware_StreamingDeframesAndRewrites(t *testing.T) {
	env := newMwEnv(t)
	now := time.Now().UTC()
	const region, svc = "us-east-1", "s3"
	amzDate := now.Format("20060102T150405Z")
	date := amzDate[:8]
	scope := date + "/" + region + "/" + svc + "/aws4_request"
	host := "mwproj.s3.local"
	target := "/s3/buck/key.txt" // path-style: verifyReq == r (no vhost clone)

	kDate := hmacSHA256([]byte("AWS4"+env.secret), []byte(date))
	kRegion := hmacSHA256(kDate, []byte(region))
	kSvc := hmacSHA256(kRegion, []byte(svc))
	kSigning := hmacSHA256(kSvc, []byte("aws4_request"))

	const streaming = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD"
	payload := []byte("hello streaming world — this is the de-framed object body")

	// Seed signature = the request signature with the STREAMING sentinel as
	// the canonical payload hash.
	signedHdrs := "host;x-amz-content-sha256;x-amz-date"
	canonHeaders := "host:" + host + "\n" +
		"x-amz-content-sha256:" + streaming + "\n" +
		"x-amz-date:" + amzDate + "\n"
	canonReq := "PUT\n" + target + "\n\n" + canonHeaders + "\n" + signedHdrs + "\n" + streaming
	sts := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + sha256Hex([]byte(canonReq))
	seedSig := hex.EncodeToString(hmacSHA256(kSigning, []byte(sts)))

	// Per-chunk string-to-sign (mirrors internal/protocol/s3/sigv4/chunked.go).
	chunkSig := func(prevSig string, data []byte) string {
		cts := "AWS4-HMAC-SHA256-PAYLOAD\n" +
			amzDate + "\n" + scope + "\n" + prevSig + "\n" +
			emptySHA256HexTest + "\n" + sha256Hex(data)
		return hex.EncodeToString(hmacSHA256(kSigning, []byte(cts)))
	}
	sig1 := chunkSig(seedSig, payload)
	sigTerm := chunkSig(sig1, nil)

	var wire bytes.Buffer
	fmt.Fprintf(&wire, "%x;chunk-signature=%s\r\n", len(payload), sig1)
	wire.Write(payload)
	wire.WriteString("\r\n")
	fmt.Fprintf(&wire, "0;chunk-signature=%s\r\n\r\n", sigTerm)

	r := httptest.NewRequest(http.MethodPut, target, bytes.NewReader(wire.Bytes()))
	r.Host = host
	r.Header.Set("Host", host)
	r.Header.Set("x-amz-date", amzDate)
	r.Header.Set("x-amz-content-sha256", streaming)
	r.Header.Set("X-Amz-Decoded-Content-Length", strconv.Itoa(len(payload)))
	r.Header.Set("Content-Length", strconv.Itoa(wire.Len()))
	r.Header.Set("Authorization",
		"AWS4-HMAC-SHA256 Credential="+env.akid+"/"+scope+
			", SignedHeaders="+signedHdrs+", Signature="+seedSig)

	var (
		gotSHA, gotCL string
		gotBody       []byte
	)
	inner := http.HandlerFunc(func(w http.ResponseWriter, rr *http.Request) {
		gotSHA = rr.Header.Get("X-Amz-Content-Sha256")
		gotCL = rr.Header.Get("Content-Length")
		gotBody, _ = io.ReadAll(rr.Body)
		w.WriteHeader(http.StatusOK)
	})

	w := httptest.NewRecorder()
	s3.SigV4Middleware(env.service, 15*time.Minute)(inner).ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s (signature chain should verify)", w.Code, w.Body.String())
	}
	if gotSHA != "UNSIGNED-PAYLOAD" {
		t.Errorf("X-Amz-Content-Sha256=%q want UNSIGNED-PAYLOAD (so gofakes3 does not double-parse)", gotSHA)
	}
	if gotCL != strconv.Itoa(len(payload)) {
		t.Errorf("Content-Length=%q want %d (decoded payload length)", gotCL, len(payload))
	}
	if !bytes.Equal(gotBody, payload) {
		t.Errorf("forwarded body = %q, want de-framed payload %q", gotBody, payload)
	}
}
