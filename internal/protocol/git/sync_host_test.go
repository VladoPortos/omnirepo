package git_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
	gitpkg "github.com/vladoportos/omnirepo/internal/protocol/git"
)

func TestGitSyncRejectsCredentialHostBeforeRequest(t *testing.T) {
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(401) }))
	defer srv.Close()
	db := sqlitetest.New(t)
	pid, rid := seedMirrorRepo(t, db, "p", "r", srv.URL)
	creds := metadata.NewUpstreamCredsRepo(db, newTestAEAD(t))
	id, err := creds.Create(context.Background(), pid, "trusted.example", metadata.CredKindDocker, "user", "secret", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	h := gitpkg.NewSyncHandler(gitpkg.SyncDeps{DB: db, Repos: metadata.NewReposRepo(db), Projects: metadata.NewProjectsRepo(db), Refs: metadata.NewGitRefsRepo(db), Creds: creds, HTTPClient: srv.Client(), DataRoot: t.TempDir()})
	payload, _ := json.Marshal(gitpkg.SyncPayload{UpstreamURL: srv.URL, CredID: &id})
	err = h.Handle(context.Background(), string(payload), pid, rid, 0)
	if err == nil || !strings.Contains(err.Error(), "cred_host_mismatch") {
		t.Errorf("expected host mismatch, got %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("sent %d requests to unrelated host", requests.Load())
	}
}
