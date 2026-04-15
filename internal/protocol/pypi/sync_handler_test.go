package pypi_test

import (
	"testing"

	"github.com/dxc-internal/omnirepo/internal/protocol/pypi"
)

func TestPyPISyncJobKindStable(t *testing.T) {
	if pypi.SyncJobKind != "pypi_sync" {
		t.Fatalf("SyncJobKind = %q, want pypi_sync", pypi.SyncJobKind)
	}
}
