package helm_test

import (
	"testing"

	"github.com/vladoportos/omnirepo/internal/protocol/helm"
)

func TestHelmSyncJobKindStable(t *testing.T) {
	if helm.SyncJobKind != "helm_sync" {
		t.Fatalf("SyncJobKind = %q, want helm_sync", helm.SyncJobKind)
	}
}
