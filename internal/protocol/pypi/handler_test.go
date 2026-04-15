package pypi_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dxc-internal/omnirepo/internal/auth"
	"github.com/dxc-internal/omnirepo/internal/metadata"
)

func TestSimpleIndexEmpty(t *testing.T) {
	f := newHandlerFixture(t)
	f.seedRepo("proj1", "internal", true, false)

	resp, err := http.Get(f.srv.URL + "/proj1/pypi/internal/simple/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Simple index") {
		t.Fatalf("body: %s", body)
	}
}

func TestSimpleContentNegotiation(t *testing.T) {
	f := newHandlerFixture(t)
	f.seedRepo("proj1", "internal", true, false)

	// Default Accept → HTML.
	respHTML, err := http.Get(f.srv.URL + "/proj1/pypi/internal/simple/")
	if err != nil {
		t.Fatal(err)
	}
	defer respHTML.Body.Close()
	bodyHTML, _ := io.ReadAll(respHTML.Body)
	if !strings.Contains(string(bodyHTML), "<html>") {
		t.Fatalf("html body missing <html>: %s", bodyHTML)
	}

	// Accept: application/vnd.pypi.simple.v1+json → JSON.
	req, _ := http.NewRequest(http.MethodGet, f.srv.URL+"/proj1/pypi/internal/simple/", nil)
	req.Header.Set("Accept", "application/vnd.pypi.simple.v1+json")
	respJSON, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer respJSON.Body.Close()
	if ct := respJSON.Header.Get("Content-Type"); !strings.Contains(ct, "json") {
		t.Fatalf("Content-Type=%q want json", ct)
	}
	bodyJSON, _ := io.ReadAll(respJSON.Body)
	var doc struct {
		Meta struct {
			APIVersion string `json:"api-version"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(bodyJSON, &doc); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, bodyJSON)
	}
	if doc.Meta.APIVersion != "1.0" {
		t.Fatalf("api-version=%q", doc.Meta.APIVersion)
	}
}

func TestSimpleRedirectNonNormalized(t *testing.T) {
	f := newHandlerFixture(t)
	f.seedRepo("proj1", "internal", true, false)

	c := http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := c.Get(f.srv.URL + "/proj1/pypi/internal/simple/Flask/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMovedPermanently {
		t.Fatalf("status=%d want 301", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.HasSuffix(loc, "/proj1/pypi/internal/simple/flask/") {
		t.Fatalf("Location=%q", loc)
	}
}

func TestSimpleRepoMissing401(t *testing.T) {
	f := newHandlerFixture(t)
	// Anonymous + missing repo: AnonymousReadOK falls through (repo not
	// public_read because it does not exist), and BasicOrAPIKey 401s.
	resp, err := http.Get(f.srv.URL + "/no-such-proj/pypi/x/simple/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", resp.StatusCode)
	}
}

func TestSimplePrivateForbiddenForAnon(t *testing.T) {
	f := newHandlerFixture(t)
	f.seedRepo("proj1", "internal", false, false) // private

	resp, err := http.Get(f.srv.URL + "/proj1/pypi/internal/simple/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anon status=%d want 401", resp.StatusCode)
	}
}

func TestLegacyUploadRoundTripWheel(t *testing.T) {
	f := newHandlerFixture(t)
	_, rid := f.seedRepo("proj1", "internal", false, false)

	wheel := makeWheelBytes(t, "Flask", "2.3.0")
	resp := twineUpload(t, f.srv.URL, "proj1", "internal",
		"Flask-2.3.0-py3-none-any.whl", wheel, "bdist_wheel", f.basicAuth())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}

	// File on disk.
	abs := filepath.Join(f.repoRoot, "proj1", "pypi", "internal", "packages", "Flask-2.3.0-py3-none-any.whl")
	got, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, wheel) {
		t.Fatalf("on-disk mismatch")
	}

	// pypi_files row inserted with normalized project.
	row, err := f.pypiRepo.FindByFilename(context.Background(), rid, "Flask-2.3.0-py3-none-any.whl")
	if err != nil || row == nil {
		t.Fatalf("row: %v", err)
	}
	if row.ProjectNormalized != "flask" {
		t.Fatalf("ProjectNormalized=%q want flask", row.ProjectNormalized)
	}
	if row.Version != "2.3.0" {
		t.Fatalf("Version=%q", row.Version)
	}
	if row.Kind != "wheel" {
		t.Fatalf("Kind=%q", row.Kind)
	}
	// FTS row present.
	var n int
	if err := f.db.Reader.QueryRow(
		`SELECT COUNT(*) FROM pypi_fts WHERE repo_id=? AND name=? AND version=?`,
		rid, "flask", "2.3.0",
	).Scan(&n); err != nil {
		t.Fatalf("fts count: %v", err)
	}
	if n != 1 {
		t.Fatalf("pypi_fts rows=%d want 1", n)
	}
	// state dirty + coalescer kicked.
	state, _, err := f.repos.GetMetadataState(context.Background(), rid)
	if err != nil {
		t.Fatalf("GetMetadataState: %v", err)
	}
	if state != metadata.MetadataStateDirty {
		t.Fatalf("state=%q want dirty", state)
	}
	f.waitForKick(t, rid, 1)
}

func TestLegacyUploadRejectedInvalid(t *testing.T) {
	f := newHandlerFixture(t)
	f.seedRepo("proj1", "internal", false, false)
	resp := twineUpload(t, f.srv.URL, "proj1", "internal",
		"bogus-1.0-py3-none-any.whl", []byte("garbage"), "bdist_wheel", f.basicAuth())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", resp.StatusCode)
	}
}

func TestLegacyUploadForbiddenForOutsider(t *testing.T) {
	f := newHandlerFixture(t)
	f.seedRepo("proj1", "internal", false, false)

	// Second project, user is NOT a member.
	pid, err := f.projects.Create(context.Background(), "proj2", "")
	if err != nil {
		t.Fatal(err)
	}
	autoScan := false
	publicRead := false
	if _, err := f.repos.Create(context.Background(), pid, "pypi", "internal", "", &autoScan, nil, &publicRead); err != nil {
		t.Fatal(err)
	}
	wheel := makeWheelBytes(t, "Flask", "2.3.0")
	resp := twineUpload(t, f.srv.URL, "proj2", "internal",
		"Flask-2.3.0-py3-none-any.whl", wheel, "bdist_wheel", f.basicAuth())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status=%d want 403", resp.StatusCode)
	}
}

func TestLegacyUploadSdist(t *testing.T) {
	f := newHandlerFixture(t)
	_, rid := f.seedRepo("proj1", "internal", false, false)

	sdist := makeSdistBytes(t, "Flask", "2.3.0")
	resp := twineUpload(t, f.srv.URL, "proj1", "internal",
		"Flask-2.3.0.tar.gz", sdist, "sdist", f.basicAuth())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	row, err := f.pypiRepo.FindByFilename(context.Background(), rid, "Flask-2.3.0.tar.gz")
	if err != nil || row == nil {
		t.Fatalf("row: %v", err)
	}
	if row.Kind != "sdist" {
		t.Fatalf("Kind=%q", row.Kind)
	}
}

// PEP 694 helpers + tests.

func pep694CreateSession(t *testing.T, srvURL, proj, repo, name, version, auth string) (string, int) {
	t.Helper()
	body := fmt.Sprintf(`{"meta":{"api-version":"1.0"},"name":%q,"version":%q}`, name, version)
	req, _ := http.NewRequest(http.MethodPost, srvURL+"/"+proj+"/pypi/"+repo+"/+upload/",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return "", resp.StatusCode
	}
	var got struct {
		SessionID string `json:"session-id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&got)
	return got.SessionID, resp.StatusCode
}

// pep694Upload PUTs file content to the staging URL.
func pep694Upload(t *testing.T, srvURL, proj, repo, sid, filename string, body []byte, auth string) int {
	t.Helper()
	url := fmt.Sprintf("%s/%s/pypi/%s/+upload/%s/%s", srvURL, proj, repo, sid, filename)
	req, _ := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func pep694Commit(t *testing.T, srvURL, proj, repo, sid, auth string) int {
	t.Helper()
	url := fmt.Sprintf("%s/%s/pypi/%s/+upload/%s/commit", srvURL, proj, repo, sid)
	req, _ := http.NewRequest(http.MethodPost, url, nil)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestPEP694FullFlow(t *testing.T) {
	f := newHandlerFixture(t)
	_, rid := f.seedRepo("proj1", "internal", false, false)

	sid, status := pep694CreateSession(t, f.srv.URL, "proj1", "internal", "Flask", "2.3.0", f.basicAuth())
	if status != http.StatusCreated || sid == "" {
		t.Fatalf("create session status=%d sid=%q", status, sid)
	}

	wheel := makeWheelBytes(t, "Flask", "2.3.0")
	if s := pep694Upload(t, f.srv.URL, "proj1", "internal", sid,
		"Flask-2.3.0-py3-none-any.whl", wheel, f.basicAuth()); s != http.StatusAccepted {
		t.Fatalf("upload status=%d", s)
	}

	if s := pep694Commit(t, f.srv.URL, "proj1", "internal", sid, f.basicAuth()); s != http.StatusOK {
		t.Fatalf("commit status=%d", s)
	}

	// Row inserted.
	row, err := f.pypiRepo.FindByFilename(context.Background(), rid, "Flask-2.3.0-py3-none-any.whl")
	if err != nil || row == nil {
		t.Fatalf("row missing: %v", err)
	}
	if row.ProjectNormalized != "flask" {
		t.Fatalf("ProjectNormalized=%q", row.ProjectNormalized)
	}
	// Coalescer kicked once at end of commit.
	f.waitForKick(t, rid, 1)
}

func TestPEP694WrongActorRejected(t *testing.T) {
	f := newHandlerFixture(t)
	pid, _ := f.seedRepo("proj1", "internal", false, false)

	sid, status := pep694CreateSession(t, f.srv.URL, "proj1", "internal", "Flask", "2.3.0", f.basicAuth())
	if status != http.StatusCreated {
		t.Fatalf("create status=%d", status)
	}

	// Make a second user that's also a member of the project, but uploads
	// against the first user's session-id.
	otherLogin := "other-user"
	otherPass := "other-test-password-1234567"
	otherHash, err := auth.HashPassword(otherPass)
	if err != nil {
		t.Fatal(err)
	}
	otherID, err := f.users.Create(context.Background(), otherLogin, "o@example.com", otherHash, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Writer.Exec(`INSERT INTO project_members(project_id, user_id) VALUES (?, ?)`, pid, otherID); err != nil {
		t.Fatal(err)
	}

	otherAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte(otherLogin+":"+otherPass))
	if s := pep694Upload(t, f.srv.URL, "proj1", "internal", sid,
		"Flask-2.3.0-py3-none-any.whl", makeWheelBytes(t, "Flask", "2.3.0"), otherAuth); s != http.StatusForbidden {
		t.Fatalf("status=%d want 403", s)
	}
}

func TestPEP694SessionExpired(t *testing.T) {
	f := newHandlerFixture(t)
	f.seedRepo("proj1", "internal", false, false)

	// PEP694 fixture TTL is 1s; sleep 1.2s.
	sid, status := pep694CreateSession(t, f.srv.URL, "proj1", "internal", "Flask", "2.3.0", f.basicAuth())
	if status != http.StatusCreated {
		t.Fatalf("create status=%d", status)
	}
	time.Sleep(1100 * time.Millisecond)
	if s := pep694Upload(t, f.srv.URL, "proj1", "internal", sid,
		"Flask-2.3.0-py3-none-any.whl", makeWheelBytes(t, "Flask", "2.3.0"), f.basicAuth()); s != http.StatusGone {
		t.Fatalf("status=%d want 410", s)
	}
}
