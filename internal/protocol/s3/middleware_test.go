package s3

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"testing"
)

// xmlError mirrors the AWS error XML shape for test assertions.
type xmlError struct {
	XMLName xml.Name `xml:"Error"`
	Code    string   `xml:"Code"`
	Message string   `xml:"Message"`
}

func TestRejectNonSigV4_NoHeader(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called")
	})
	handler := RejectNonSigV4(inner)

	req := httptest.NewRequest(http.MethodGet, "/s3/mybucket/file.txt", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusForbidden)
	}

	var errBody xmlError
	if err := xml.Unmarshal(w.Body.Bytes(), &errBody); err != nil {
		t.Fatalf("unmarshal XML error: %v", err)
	}
	if errBody.Code != "InvalidAccessKeyId" {
		t.Errorf("error code = %q, want InvalidAccessKeyId", errBody.Code)
	}
}

func TestRejectNonSigV4_BearerToken(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called for Bearer auth")
	})
	handler := RejectNonSigV4(inner)

	req := httptest.NewRequest(http.MethodGet, "/s3/mybucket/file.txt", nil)
	req.Header.Set("Authorization", "Bearer some-session-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusForbidden)
	}

	var errBody xmlError
	if err := xml.Unmarshal(w.Body.Bytes(), &errBody); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if errBody.Code != "InvalidAccessKeyId" {
		t.Errorf("code = %q, want InvalidAccessKeyId", errBody.Code)
	}
}

func TestRejectNonSigV4_ValidSigV4Passes(t *testing.T) {
	var called bool
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	handler := RejectNonSigV4(inner)

	req := httptest.NewRequest(http.MethodGet, "/s3/mybucket/file.txt", nil)
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20260416/us-east-1/s3/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc123")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !called {
		t.Fatal("inner handler was not called for valid SigV4 prefix")
	}
}

func TestBucketFromPath(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/s3/mybucket/file.txt", "mybucket"},
		{"/s3/mybucket", "mybucket"},
		{"/s3/", ""},
		{"/s3", ""},
		{"/other/path", ""},
	}
	for _, tt := range tests {
		got := bucketFromPath(tt.path)
		if got != tt.want {
			t.Errorf("bucketFromPath(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestRequireBucketAccess_NoBucket_PassesThrough(t *testing.T) {
	var called bool
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	lookup := BucketProjectLookup(func(ctx context.Context, name string) (int64, bool, error) {
		t.Fatal("lookup should not be called for root path")
		return 0, false, nil
	})
	handler := RequireBucketAccess(lookup)(inner)

	req := httptest.NewRequest(http.MethodGet, "/s3/", nil)
	req.URL.Path = "/s3/"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !called {
		t.Fatal("inner handler not called for root path")
	}
}

func TestActionFromMethod(t *testing.T) {
	tests := []struct {
		method string
		want   string
	}{
		{http.MethodGet, "s3:bucket:read"},
		{http.MethodHead, "s3:bucket:read"},
		{http.MethodPut, "s3:bucket:write"},
		{http.MethodPost, "s3:bucket:write"},
		{http.MethodDelete, "s3:bucket:write"},
	}
	for _, tt := range tests {
		got := actionFromMethod(tt.method)
		if string(got) != tt.want {
			t.Errorf("actionFromMethod(%q) = %q, want %q", tt.method, got, tt.want)
		}
	}
}
