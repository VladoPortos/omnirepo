package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLimitTrivyDBUploadRejectsDeclaredOversize(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/trivy/db", strings.NewReader("tiny"))
	req.ContentLength = maxTrivyDBUploadBytes + 1
	w := httptest.NewRecorder()

	if limitTrivyDBUpload(w, req) {
		t.Fatal("limitTrivyDBUpload allowed an oversized request")
	}
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d want 413", w.Code)
	}
}

func TestLimitTrivyDBUploadWrapsUnknownLengthBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/trivy/db", strings.NewReader("tiny"))
	req.ContentLength = -1
	originalBody := req.Body
	w := httptest.NewRecorder()

	if !limitTrivyDBUpload(w, req) {
		t.Fatal("limitTrivyDBUpload rejected an unknown-length request before reading")
	}
	if req.Body == originalBody {
		t.Fatal("request body was not wrapped with a limiting reader")
	}
}
