package sigv4

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// xmlErr is a minimal mirror of the AWS error envelope for assertion.
type xmlErr struct {
	XMLName   xml.Name `xml:"Error"`
	Code      string   `xml:"Code"`
	Message   string   `xml:"Message"`
	RequestID string   `xml:"RequestId"`
}

// TestWriteError_ContentSHA256Mismatch verifies that calling WriteError with
// ErrContentSHA256Mismatch produces the AWS-shape XAmzContentSHA256Mismatch
// envelope at HTTP 400 with a non-empty RequestId.
func TestWriteError_ContentSHA256Mismatch(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/s3/b/k", nil)

	WriteError(rec, req, ErrContentSHA256Mismatch)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/xml" {
		t.Errorf("Content-Type = %q, want application/xml", got)
	}

	var body xmlErr
	if err := xml.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v\nraw=%s", err, rec.Body.String())
	}
	if body.Code != "XAmzContentSHA256Mismatch" {
		t.Errorf("Code = %q, want XAmzContentSHA256Mismatch", body.Code)
	}
	if strings.TrimSpace(body.Message) == "" {
		t.Errorf("Message is empty; want non-empty AWS-compatible text")
	}
	if body.RequestID == "" {
		t.Errorf("RequestId is empty; want non-empty hex (newRequestID)")
	}
	if got := rec.Header().Get("x-amz-request-id"); got == "" {
		t.Errorf("x-amz-request-id header empty; want non-empty hex")
	}
}

// TestMapError_ContentSHA256Mismatch_Wrapped verifies the dispatch is via
// errors.Is so wrapped sentinel errors still produce the 400 envelope.
func TestMapError_ContentSHA256Mismatch_Wrapped(t *testing.T) {
	wrapped := fmt.Errorf("backend: %w: streamed=abc declared=def", ErrContentSHA256Mismatch)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/s3/b/k", nil)
	WriteError(rec, req, wrapped)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	var body xmlErr
	if err := xml.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v\nraw=%s", err, rec.Body.String())
	}
	if body.Code != "XAmzContentSHA256Mismatch" {
		t.Errorf("Code = %q, want XAmzContentSHA256Mismatch (wrapped errors.Is path)", body.Code)
	}
}
