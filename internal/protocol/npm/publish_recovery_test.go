package npm_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPublishMetadataFailureRemovesUnpublishedTarball(t *testing.T) {
	f := newFixture(t)
	f.seedRepo("acme", "js", false)
	if _, err := f.db.Writer.Exec(`CREATE TRIGGER fail_npm BEFORE INSERT ON npm_dist_tags BEGIN SELECT RAISE(ABORT, 'failpoint'); END`); err != nil {
		t.Fatal(err)
	}
	body := publishJSON(t, "pkg", "1.0.0", []byte("payload"))
	resp := f.do(t, "PUT", "/acme/npm/js/pkg", body, true)
	if resp.StatusCode != 500 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, mustBody(t, resp))
	}
	_ = resp.Body.Close()
	var count int
	if err := f.db.Reader.QueryRow(`SELECT COUNT(*) FROM npm_packages`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rows=%d err=%v", count, err)
	}
	if err := filepath.Walk(f.repoRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && filepath.Ext(path) == ".tgz" {
			return errors.New("unpublished tarball remains")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Writer.Exec(`DROP TRIGGER fail_npm`); err != nil {
		t.Fatal(err)
	}
	resp = f.do(t, "PUT", "/acme/npm/js/pkg", body, true)
	if resp.StatusCode != 201 {
		t.Fatalf("retry=%d body=%s", resp.StatusCode, mustBody(t, resp))
	}
	_ = resp.Body.Close()
	resp = f.do(t, "GET", "/acme/npm/js/pkg/-/pkg-1.0.0.tgz", nil, true)
	if got := mustBody(t, resp); resp.StatusCode != 200 || got != "payload" {
		t.Fatalf("download=%d %q", resp.StatusCode, got)
	}
}

func TestConcurrentDuplicatePublishPreservesWinningBytes(t *testing.T) {
	f := newFixture(t)
	f.seedRepo("acme", "js", false)
	type outcome struct {
		status  int
		payload string
	}
	results := make(chan outcome, 2)
	start := make(chan struct{})
	for _, payload := range []string{"first-content", "other-content"} {
		body := publishJSON(t, "pkg", "1.0.0", []byte(payload))
		go func() {
			<-start
			resp := f.do(t, "PUT", "/acme/npm/js/pkg", body, true)
			_ = resp.Body.Close()
			results <- outcome{resp.StatusCode, payload}
		}()
	}
	close(start)
	a, b := <-results, <-results
	if a.status != 201 {
		a, b = b, a
	}
	if a.status != 201 || b.status != 403 {
		t.Fatalf("outcomes=%+v %+v", a, b)
	}
	resp := f.do(t, "GET", "/acme/npm/js/pkg/-/pkg-1.0.0.tgz", nil, true)
	if got := mustBody(t, resp); resp.StatusCode != 200 || got != a.payload {
		t.Fatalf("winning payload=%q, download=%d %q", a.payload, resp.StatusCode, got)
	}
}
