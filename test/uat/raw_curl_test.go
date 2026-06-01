//go:build uat

// RAW curl-level UAT (SC-4): content-negotiated directory listing
// and full PUT/GET/HEAD/DELETE round-trip against the /raw path.
//
// Uses net/http directly (equivalent to what curl would issue) so the
// assertions are deterministic and don't depend on a shell curl binary.
package uat

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestRAW_CurlRoundTrip exercises the full RAW surface: PUT a file,
// GET it back (byte-identical), list its parent directory with
// Accept: application/json and again with Accept: text/html, HEAD for
// size + ETag, DELETE, then re-GET → 404.
func TestRAW_CurlRoundTrip(t *testing.T) {
	// Bootstrap with the seeded repo as a raw repo.
	f := bootApp(t, bootOpts{Repo: "files", RepoType: "raw"})

	// The bootstrap seeds the raw repo with public_read=true, so
	// reads are anonymous. Writes (PUT/DELETE) require Basic auth —
	// the raw handler uses BasicOrAPIKey middleware, not session
	// cookies (cookie auth lives on /api/v1 only).
	authReq := func(method, url string, body io.Reader) *http.Request {
		req, _ := http.NewRequest(method, url, body)
		req.SetBasicAuth(f.adminLogin, f.adminPassword)
		return req
	}

	basePath := "/" + f.project + "/raw/" + f.repo + "/dir/file.txt"
	fileBody := []byte("hello from uat round-trip\n")

	// ---------- PUT ----------
	putReq := authReq("PUT", f.baseURL()+basePath, bytes.NewReader(fileBody))
	putReq.Header.Set("Content-Type", "text/plain")
	putResp, err := f.httpClient.Do(putReq)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	putBody, _ := io.ReadAll(putResp.Body)
	_ = putResp.Body.Close()
	if putResp.StatusCode != http.StatusOK && putResp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT: status=%d body=%s", putResp.StatusCode, putBody)
	}

	// ---------- GET file (anonymous; public_read=true) ----------
	getReq, _ := http.NewRequest("GET", f.baseURL()+basePath, nil)
	getResp, err := f.httpClient.Do(getReq)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	gotBody, _ := io.ReadAll(getResp.Body)
	_ = getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET: status=%d body=%s", getResp.StatusCode, gotBody)
	}
	if !bytes.Equal(gotBody, fileBody) {
		t.Fatalf("GET body mismatch: got=%q want=%q", gotBody, fileBody)
	}

	// ---------- GET dir json (anonymous) ----------
	dirPath := "/" + f.project + "/raw/" + f.repo + "/dir/"
	jsonReq, _ := http.NewRequest("GET", f.baseURL()+dirPath, nil)
	jsonReq.Header.Set("Accept", "application/json")
	jsonResp, err := f.httpClient.Do(jsonReq)
	if err != nil {
		t.Fatalf("GET dir json: %v", err)
	}
	jsonBody, _ := io.ReadAll(jsonResp.Body)
	_ = jsonResp.Body.Close()
	if jsonResp.StatusCode != http.StatusOK {
		t.Fatalf("GET dir json: status=%d body=%s", jsonResp.StatusCode, jsonBody)
	}
	ct := jsonResp.Header.Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Fatalf("GET dir json: Content-Type=%q want application/json", ct)
	}
	var listing []struct {
		Name  string `json:"name"`
		Size  int64  `json:"size"`
		IsDir bool   `json:"is_dir"`
	}
	if err := json.Unmarshal(jsonBody, &listing); err != nil {
		t.Fatalf("decode json listing %q: %v", jsonBody, err)
	}
	foundFile := false
	for _, it := range listing {
		if it.Name == "file.txt" {
			foundFile = true
			if it.Size != int64(len(fileBody)) {
				t.Errorf("json listing size: got=%d want=%d", it.Size, len(fileBody))
			}
			if it.IsDir {
				t.Errorf("json listing is_dir: got=true want=false")
			}
		}
	}
	if !foundFile {
		t.Errorf("json listing missing file.txt; got %+v", listing)
	}

	// ---------- GET dir html (anonymous) ----------
	htmlReq, _ := http.NewRequest("GET", f.baseURL()+dirPath, nil)
	htmlReq.Header.Set("Accept", "text/html")
	htmlResp, err := f.httpClient.Do(htmlReq)
	if err != nil {
		t.Fatalf("GET dir html: %v", err)
	}
	htmlBody, _ := io.ReadAll(htmlResp.Body)
	_ = htmlResp.Body.Close()
	if htmlResp.StatusCode != http.StatusOK {
		t.Fatalf("GET dir html: status=%d body=%s", htmlResp.StatusCode, htmlBody)
	}
	htmlCT := htmlResp.Header.Get("Content-Type")
	if !strings.Contains(htmlCT, "text/html") {
		t.Fatalf("GET dir html: Content-Type=%q want text/html", htmlCT)
	}
	if !strings.Contains(string(htmlBody), "file.txt") {
		t.Errorf("html listing missing file.txt; got=%s", htmlBody)
	}

	// ---------- HEAD file (anonymous) ----------
	headReq, _ := http.NewRequest("HEAD", f.baseURL()+basePath, nil)
	headResp, err := f.httpClient.Do(headReq)
	if err != nil {
		t.Fatalf("HEAD: %v", err)
	}
	_ = headResp.Body.Close()
	if headResp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD: status=%d", headResp.StatusCode)
	}
	if cl := headResp.Header.Get("Content-Length"); cl == "" {
		t.Errorf("HEAD missing Content-Length header")
	}
	// ETag is optional per the raw handler; log its presence but don't
	// require it to avoid coupling UAT to handler internals.
	if et := headResp.Header.Get("ETag"); et != "" {
		t.Logf("HEAD ETag present: %q", et)
	}

	// ---------- DELETE (Basic auth required) ----------
	delReq := authReq("DELETE", f.baseURL()+basePath, nil)
	delResp, err := f.httpClient.Do(delReq)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	delBody, _ := io.ReadAll(delResp.Body)
	_ = delResp.Body.Close()
	if delResp.StatusCode != http.StatusOK && delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE: status=%d body=%s", delResp.StatusCode, delBody)
	}

	// ---------- GET after delete → 404 ----------
	gone, err := http.Get(f.baseURL() + basePath) // anonymous; public_read=true
	if err != nil {
		t.Fatalf("re-GET: %v", err)
	}
	_ = gone.Body.Close()
	if gone.StatusCode != http.StatusNotFound {
		t.Fatalf("re-GET after DELETE: status=%d want 404", gone.StatusCode)
	}
}
