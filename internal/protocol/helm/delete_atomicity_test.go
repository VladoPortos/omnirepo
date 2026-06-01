package helm_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// TestHelmDelete_TxFailure_FileAndRowIntact is the ATOMICDEL-06 fault-injection
// proof for the Helm protocol. Same shape as raw/rpm/deb plus an extra
// invariant: when a matching .prov sidecar is present, it MUST also survive
// the rolled-back tx (helm/delete.go orders the .prov move AFTER the chart
// move which itself runs only after tx commit — so a failed tx never touches
// either file).
//
// Storage path layout: <repoRoot>/<project>/helm/<repo>/charts/<filename>.tgz
// and the matching .prov at <chartAbs>.prov.
func TestHelmDelete_TxFailure_FileAndRowIntact(t *testing.T) {
	f := newFixture(t)
	_, repoID := f.seedRepo("proj1", "charts1", false, false)

	tgz := makeChartTGZ(t, "mychart", "1.0.0", "", "", nil)
	resp := f.put(t, "/proj1/helm/charts1/charts/mychart-1.0.0.tgz", tgz, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("baseline PUT: %d", resp.StatusCode)
	}
	resp.Body.Close()
	f.waitForKick(t, repoID, 1)

	chartPath := filepath.Join(f.repoRoot, "proj1", "helm", "charts1", "charts", "mychart-1.0.0.tgz")
	provPath := chartPath + ".prov"

	// Drop a fake .prov sidecar so the test exercises the helm-specific
	// "chart + .prov both untouched on rolled-back tx" invariant. Production
	// .prov is uploaded via PUT, but a raw write here is sufficient: the
	// delete handler only does os.Stat(provAbs) → trash.Move, both of which
	// see the file regardless of how it landed on disk.
	if err := os.WriteFile(provPath, []byte("fake-prov-signature"), 0o640); err != nil {
		t.Fatalf("seed .prov: %v", err)
	}

	// Sanity baseline.
	if _, err := os.Stat(chartPath); err != nil {
		t.Fatalf("baseline chart missing: %v", err)
	}
	if _, err := os.Stat(provPath); err != nil {
		t.Fatalf("baseline prov missing: %v", err)
	}
	row, _ := f.charts.FindByFilename(context.Background(), repoID, "mychart-1.0.0.tgz")
	if row == nil {
		t.Fatalf("baseline row missing")
	}

	// Arm the one-shot failpoint.
	sentinel := errors.New("synthetic-tx-fail")
	f.db.SetWriteTxFailpointForTest(sentinel)

	resp = f.del(t, "/proj1/helm/charts1/charts/mychart-1.0.0.tgz", true)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("DELETE under tx failure: status=%d (want 500)", resp.StatusCode)
	}
	resp.Body.Close()

	// THE ATOMICITY INVARIANT — chart .tgz, .prov sidecar, and DB row all
	// untouched. A future regression that re-inverts the ordering or moves
	// .prov before tx commit will fail one of these.
	if _, err := os.Stat(chartPath); err != nil {
		t.Fatalf("chart .tgz gone after rolled-back DELETE: %v", err)
	}
	if _, err := os.Stat(provPath); err != nil {
		t.Fatalf(".prov gone after rolled-back DELETE: %v", err)
	}
	row, _ = f.charts.FindByFilename(context.Background(), repoID, "mychart-1.0.0.tgz")
	if row == nil {
		t.Fatal("helm_charts row gone after rolled-back DELETE (should be intact)")
	}

	// Recovery DELETE: chart in trash; row removed; .prov also moved
	// (best-effort but happy-path here).
	resp = f.del(t, "/proj1/helm/charts1/charts/mychart-1.0.0.tgz", true)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("recovery DELETE: status=%d (want 204)", resp.StatusCode)
	}
	resp.Body.Close()

	if _, err := os.Stat(chartPath); !os.IsNotExist(err) {
		t.Fatalf("chart still on disk after recovery DELETE: %v", err)
	}
	if _, err := os.Stat(provPath); !os.IsNotExist(err) {
		t.Fatalf(".prov still on disk after recovery DELETE: %v", err)
	}
	row, _ = f.charts.FindByFilename(context.Background(), repoID, "mychart-1.0.0.tgz")
	if row != nil {
		t.Fatal("row still present after recovery DELETE")
	}
}
