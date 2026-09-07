package s3_test

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/xml"
	"github.com/johannesboyne/gofakes3"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func repairRequest(t *testing.T, f *testFixture, method, path, host string, headers map[string]string, body []byte) *http.Response {
	t.Helper()
	r, err := http.NewRequest(method, f.server.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if host != "" {
		r.Host = host
	} else {
		host = r.URL.Host
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	signRequest(r, f.akid, f.secret, host, time.Now(), body)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestRepairCopySourceAuthorization(t *testing.T) {
	f := setupTestFixture(t)
	res, err := f.db.Writer.Exec(`INSERT INTO projects(name) VALUES ('other')`)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := res.LastInsertId()
	f.backend.DefaultProjectID = pid
	if err := f.backend.CreateBucket("private"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.backend.PutObject("private", "secret", nil, strings.NewReader("secret"), 6, nil); err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"", "mybucket.omnirepo.example.com"} {
		for _, source := range []string{"/private/secret", "%2Fprivate%2Fsecret"} {
			path := "/s3/mybucket/copied"
			if host != "" {
				path = "/copied"
			}
			resp := repairRequest(t, f, "PUT", path, host, map[string]string{"X-Amz-Copy-Source": source}, nil)
			if resp.StatusCode != 403 {
				t.Fatalf("host=%s source=%s status=%d", host, source, resp.StatusCode)
			}
			if _, err := f.backend.HeadObject("mybucket", "copied"); err == nil {
				t.Fatal("source exfiltrated")
			}
		}
	}
	resp := repairRequest(t, f, "PUT", "/s3/mybucket/copied", "", map[string]string{"X-Amz-Copy-Source": "/mybucket/file.txt"}, nil)
	if resp.StatusCode != 200 {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("authorized copy: %d %s", resp.StatusCode, data)
	}
}

func TestRepairMultipartOpaqueETagHTTP(t *testing.T) {
	f := setupTestFixture(t)
	var keyID int64
	if err := f.db.Reader.QueryRow(`SELECT id FROM s3_access_keys LIMIT 1`).Scan(&keyID); err != nil {
		t.Fatal(err)
	}
	id, err := f.backend.CreateMultipartUploadCtx(context.Background(), "mybucket", "multi", nil, &keyID)
	if err != nil {
		t.Fatal(err)
	}
	part, err := f.backend.UploadPart("mybucket", "multi", id, 1, 3, strings.NewReader("new"))
	if err != nil {
		t.Fatal(err)
	}
	_, etag, err := f.backend.CompleteMultipartUpload("mybucket", "multi", id, &gofakes3.CompleteMultipartUploadRequest{Parts: []gofakes3.CompletedPart{{PartNumber: 1, ETag: part}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "HEAD"} {
		for _, condition := range []string{"", "If-None-Match", "If-Match"} {
			headers := map[string]string{}
			if condition != "" {
				headers[condition] = etag
			}
			resp := repairRequest(t, f, method, "/s3/mybucket/multi", "", headers, nil)
			want := 200
			if condition == "If-None-Match" {
				want = 304
			}
			if resp.StatusCode != want || resp.Header.Get("ETag") != etag {
				t.Fatalf("%s %s: status=%d etag=%s want=%s", method, condition, resp.StatusCode, resp.Header.Get("ETag"), etag)
			}
			if resp.Header.Get("X-Omnirepo-Object-Etag") != "" {
				t.Fatal("internal bridge header leaked")
			}
		}
		resp := repairRequest(t, f, method, "/s3/mybucket/multi", "", map[string]string{"If-Match": `"wrong"`}, nil)
		if resp.StatusCode != http.StatusPreconditionFailed {
			t.Fatalf("mismatched validator: %d", resp.StatusCode)
		}
	}
	resp := repairRequest(t, f, "PUT", "/s3/mybucket/copied", "", map[string]string{"X-Amz-Copy-Source": "/mybucket/multi"}, nil)
	var result struct {
		ETag string `xml:"ETag"`
	}
	if err := xml.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	sum := md5.Sum([]byte("new"))
	want := `"` + hex.EncodeToString(sum[:]) + `"`
	if result.ETag != want {
		t.Fatalf("copy etag=%s want %s", result.ETag, want)
	}
	resp = repairRequest(t, f, "PUT", "/s3/mybucket/multi", "", map[string]string{"If-Match": etag}, []byte("updated"))
	if resp.StatusCode != 200 {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("conditional overwrite: %d %s", resp.StatusCode, data)
	}
}

func TestRepairHTTPAliasAndFailedOverwrite(t *testing.T) {
	f := setupTestFixture(t)
	if _, err := f.backend.PutObject("mybucket", "a/b", nil, strings.NewReader("old"), 3, nil); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/s3/mybucket/a//b", "/s3/mybucket/a/b/"} {
		resp := repairRequest(t, f, "DELETE", path, "", nil, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("alias %s: %d", path, resp.StatusCode)
		}
	}
	if _, err := f.db.Writer.Exec(`CREATE TRIGGER fail_http_s3 BEFORE UPDATE ON s3_objects BEGIN SELECT RAISE(ABORT, 'failpoint'); END`); err != nil {
		t.Fatal(err)
	}
	resp := repairRequest(t, f, "PUT", "/s3/mybucket/a/b", "", nil, []byte("new"))
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("failed overwrite: %d", resp.StatusCode)
	}
	resp = repairRequest(t, f, "GET", "/s3/mybucket/a/b", "", nil, nil)
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || string(data) != "old" {
		t.Fatalf("previous object lost: %d %q", resp.StatusCode, data)
	}
}
