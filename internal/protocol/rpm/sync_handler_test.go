package rpm_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
	"github.com/vladoportos/omnirepo/internal/protocol/rpm"
)

// TestRPMSyncPayloadShape pins the SyncPayload JSON tags — the sync
// handler unmarshals the enqueued payload string into this exact shape.
func TestRPMSyncPayloadShape(t *testing.T) {
	body := []byte(`{"upstream_url":"https://repo.example/centos","cred_id":42,"filter":{"names":["foo"]}}`)
	var pl rpm.SyncPayload
	if err := json.Unmarshal(body, &pl); err != nil {
		t.Fatalf("unmarshal SyncPayload: %v", err)
	}
	if pl.UpstreamURL != "https://repo.example/centos" {
		t.Fatalf("upstream_url wrong: %+v", pl)
	}
	if pl.CredID == nil || *pl.CredID != 42 {
		t.Fatalf("cred_id wrong: %+v", pl.CredID)
	}
	if pl.Filter == nil || len(pl.Filter.Names) != 1 || pl.Filter.Names[0] != "foo" {
		t.Fatalf("filter wrong: %+v", pl.Filter)
	}
}

// TestRPMSyncJobKindStable pins the kind constant; if this test ever needs
// to change, the SyncPool registration in app.Run must change in lockstep.
func TestRPMSyncJobKindStable(t *testing.T) {
	if rpm.SyncJobKind != "rpm_sync" {
		t.Fatalf("SyncJobKind = %q, want rpm_sync", rpm.SyncJobKind)
	}
}

// TestRPMSyncRejectsBadPayload smoke-tests that Handle rejects malformed
// JSON without panicking.
func TestRPMSyncRejectsBadPayload(t *testing.T) {
	db := sqlitetest.New(t)
	h := rpm.NewSyncHandler(rpm.SyncDeps{DB: db})
	err := h.Handle(context.Background(), `{not json`, 0, 0, 0)
	if err == nil {
		t.Fatal("expected payload error")
	}
	if !strings.Contains(err.Error(), "payload") {
		t.Fatalf("expected payload error, got: %v", err)
	}
}

// TestRPMSyncRejectsEmptyURL smoke-tests that an empty upstream_url is
// rejected before any HTTP work happens.
func TestRPMSyncRejectsEmptyURL(t *testing.T) {
	db := sqlitetest.New(t)
	h := rpm.NewSyncHandler(rpm.SyncDeps{DB: db})
	err := h.Handle(context.Background(), `{}`, 0, 0, 0)
	if err == nil {
		t.Fatal("expected upstream_url required")
	}
	if !strings.Contains(err.Error(), "upstream_url required") {
		t.Fatalf("got: %v", err)
	}
}
