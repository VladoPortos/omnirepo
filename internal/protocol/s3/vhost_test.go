package s3

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVHostRewrite(t *testing.T) {
	hostnames := []string{"omnirepo.corp.example", "s3.local"}

	// Capture the rewritten path.
	var gotPath string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	})
	handler := VHostRewrite(hostnames)(inner)

	tests := []struct {
		name     string
		host     string
		path     string
		wantPath string
	}{
		{
			name:     "vhost rewrite with suffix match",
			host:     "mybucket.omnirepo.corp.example",
			path:     "/file.txt",
			wantPath: "/s3/mybucket/file.txt",
		},
		{
			name:     "vhost rewrite with port",
			host:     "mybucket.omnirepo.corp.example:8443",
			path:     "/file.txt",
			wantPath: "/s3/mybucket/file.txt",
		},
		{
			name:     "vhost rewrite second hostname",
			host:     "testbucket.s3.local",
			path:     "/obj.bin",
			wantPath: "/s3/testbucket/obj.bin",
		},
		{
			name:     "IPv4 host — no rewrite",
			host:     "127.0.0.1:8443",
			path:     "/file.txt",
			wantPath: "/file.txt",
		},
		{
			name:     "path-style already has /s3/ — no rewrite",
			host:     "omnirepo.corp.example",
			path:     "/s3/mybucket/file.txt",
			wantPath: "/s3/mybucket/file.txt",
		},
		{
			name:     "bare hostname — no rewrite (no bucket prefix)",
			host:     "omnirepo.corp.example",
			path:     "/file.txt",
			wantPath: "/file.txt",
		},
		{
			name:     "case insensitive host — lowercased and rewritten",
			host:     "MyBucket.Omnirepo.Corp.Example",
			path:     "/key",
			wantPath: "/s3/mybucket/key", // host lowercased → mybucket is valid
		},
		{
			name:     "root path vhost rewrite",
			host:     "mybucket.omnirepo.corp.example",
			path:     "/",
			wantPath: "/s3/mybucket/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotPath = ""
			req := httptest.NewRequest(http.MethodGet, "https://"+tt.host+tt.path, nil)
			req.Host = tt.host
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if gotPath != tt.wantPath {
				t.Errorf("path = %q, want %q", gotPath, tt.wantPath)
			}
		})
	}
}

func TestIsValidBucketName(t *testing.T) {
	valid := []string{"mybucket", "my-bucket", "my.bucket", "a123", "abc"}
	for _, name := range valid {
		if !isValidBucketName(name) {
			t.Errorf("isValidBucketName(%q) = false, want true", name)
		}
	}

	invalid := []string{"", "ab", "-bucket", "UPPER", "a..b", "127.0.0.1"}
	for _, name := range invalid {
		if isValidBucketName(name) {
			t.Errorf("isValidBucketName(%q) = true, want false", name)
		}
	}
}
