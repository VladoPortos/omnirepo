package oci_test

import (
	"bytes"
	"context"
	"database/sql"
	"net/http"
	"testing"
)

func TestBlobOwnershipRejectsUnrelatedRepo(t *testing.T) {
	f := newManifestFixture(t, false)
	ctx := context.Background()
	digest, _, err := f.cas.Put(ctx, bytes.NewBufferString("private bytes"))
	if err != nil {
		t.Fatal(err)
	}
	privateProject, err := f.projects.Create(ctx, "private", "")
	if err != nil {
		t.Fatal(err)
	}
	privateRepo, err := f.repos.Create(ctx, privateProject, "docker", "secret", "", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Writer.ExecContext(ctx, `UPDATE repos SET public_read=1 WHERE id=?`, f.repoID); err != nil {
		t.Fatal(err)
	}
	if err := f.db.WriteTx(ctx, func(tx *sql.Tx) error {
		if err := f.blobs.UpsertZeroRef(ctx, tx, digest, 13); err != nil {
			return err
		}
		return f.blobs.Link(ctx, tx, privateRepo, digest)
	}); err != nil {
		t.Fatal(err)
	}
	// A global CAS row conveys no ownership to this authenticated repository.
	for _, method := range []string{"GET", "HEAD", "DELETE"} {
		req, _ := http.NewRequest(method, f.srv.URL+"/v2/"+f.repoPath+"/blobs/"+digest, nil)
		req.Header.Set("Authorization", "Bearer "+f.token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Errorf("%s unrelated blob = %d, want 404", method, resp.StatusCode)
		}
	}
	for _, method := range []string{"GET", "HEAD"} {
		req, _ := http.NewRequest(method, f.srv.URL+"/v2/"+f.repoPath+"/blobs/"+digest, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Errorf("anonymous %s unrelated blob = %d", method, resp.StatusCode)
		}
	}
	ownedConfig := f.seedBlob([]byte("owned config"))
	layerResp := f.putManifest("stolen-layer", buildManifest(ownedConfig, digest))
	layerResp.Body.Close()
	if layerResp.StatusCode != 404 {
		t.Errorf("manifest using private layer = %d", layerResp.StatusCode)
	}
	resp := f.putManifest("stolen", buildManifest(digest))
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("manifest using unrelated blob = %d, want 404", resp.StatusCode)
	}
	req, _ := http.NewRequest("POST", f.srv.URL+"/v2/"+f.repoPath+"/blobs/uploads/?mount="+digest+"&from="+f.repoPath, nil)
	req.Header.Set("Authorization", "Bearer "+f.token)
	mounted, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	mounted.Body.Close()
	if mounted.StatusCode != 202 {
		t.Errorf("mount unrelated blob = %d, want 202", mounted.StatusCode)
	}
}
