package deb_test

import (
	"testing"

	"github.com/vladoportos/omnirepo/internal/protocol/deb"
)

func TestDEBSyncJobKindStable(t *testing.T) {
	if deb.SyncJobKind != "apt_sync" {
		t.Fatalf("SyncJobKind = %q, want apt_sync", deb.SyncJobKind)
	}
}
