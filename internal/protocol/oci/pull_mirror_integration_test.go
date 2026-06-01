package oci_test

// Phase 8 Plan 06 / M6.5 — OCI pull-external + mirror-upload integration.
//
// Two orthogonal assertions covering the Docker/OCI pillar of Phase 8:
//
//   1. TestMirrorPull_OCI_ProgressAdvances
//      Confirms that a clone (pull-external) against the ggcr-style fake
//      registry from pull_external_test.go round-trips progress through the
//      real metadata.SyncJobsRepo: progress_bytes > 0, total_bytes > 0, and
//      the terminal current_step == "done" sentinel lands (after the
//      handler's final Flush). Reuses the existing mockUpstream + pullFixture
//      harness — NO fictitious env.* helpers.
//
//   2. TestMirrorPull_OCI_UploadToMirrorRepoReturns403
//      Complements the existing MirrorGuard coverage in blobs_test.go and
//      manifests_test.go by asserting from an integration angle that an
//      operator attempting to push a manifest to an is_mirror=true Docker
//      repo gets a 403 envelope with code=repo_is_mirror. Wires
//      SetMirrorConfigInTx onto the fixture repo, then drives a manifest
//      PUT through the real chi mux — the same path real Docker clients hit.

import (
	"context"
	"database/sql"
	"io"
	"strings"
	"testing"

	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/protocol/oci"
)

// TestMirrorPull_OCI_ProgressAdvances proves the end-to-end clone flow
// against the ggcr-based fake registry:
//   - pullFixture's mockUpstream serves a manifest + config + one layer
//   - runPullWithJob wires a real sync_jobs row and invokes pull-external
//     with jobID so the ProgressWriter persists throttled emits
//   - the sync_jobs row carries progress_bytes > 0, total_bytes > 0,
//     current_step == "done" after the handler's final Flush
func TestMirrorPull_OCI_ProgressAdvances(t *testing.T) {
	f := newPullFixture(t, false /* anonymous upstream */)
	jobID := f.runPullWithJob(t, oci.PullExternalJob{
		SrcImage: f.up.srcImageRef(),
		DstTag:   "mirror-progress",
	})

	var (
		progressBytes, totalBytes int64
		currentStep               string
	)
	if err := f.db.Reader.QueryRowContext(context.Background(),
		`SELECT progress_bytes, total_bytes, current_step FROM sync_jobs WHERE id=?`, jobID,
	).Scan(&progressBytes, &totalBytes, &currentStep); err != nil {
		t.Fatalf("scan sync_jobs row: %v", err)
	}
	if progressBytes <= 0 {
		t.Errorf("progress_bytes=%d; want >0 after clone", progressBytes)
	}
	if totalBytes <= 0 {
		t.Errorf("total_bytes=%d; want >0 (manifest layer+config sum)", totalBytes)
	}
	if currentStep != "done" {
		t.Errorf("current_step=%q; want 'done' (terminal sentinel)", currentStep)
	}

	// Manifest landed locally — proves the clone actually ran end-to-end
	// rather than failing silently with zero progress.
	m, err := f.manifests.GetByDigest(context.Background(), f.repoID, f.up.manifestDigest)
	if err != nil || m == nil {
		t.Fatalf("manifest not stored locally after clone: err=%v m=%v", err, m)
	}
}

// TestMirrorPull_OCI_UploadToMirrorRepoReturns403 complements the existing
// blob- and manifest-layer MirrorGuard tests by asserting from the
// integration vantage that an upload to an is_mirror Docker repo returns
// the 403 envelope code=repo_is_mirror. Exercises the MirrorGuard wiring
// on OCI's manifest PUT route (the only way to finalise a push).
func TestMirrorPull_OCI_UploadToMirrorRepoReturns403(t *testing.T) {
	f := newManifestFixture(t, false)
	if err := f.db.WriteTx(context.Background(), func(tx *sql.Tx) error {
		return f.repos.SetMirrorConfigInTx(context.Background(), tx, f.repoID, metadata.MirrorConfig{
			IsMirror:    true,
			UpstreamURL: "https://registry-1.docker.io",
			FilterJSON:  `{}`,
			CredID:      nil,
			ScanOnSync:  false,
		})
	}); err != nil {
		t.Fatalf("set mirror cfg: %v", err)
	}
	// Seed a config blob + build a minimal manifest referencing it, then
	// attempt a manifest PUT — the same path a real Docker client hits on
	// `docker push`. MirrorGuard short-circuits before the manifest is
	// parsed, so the config-blob seed is cosmetic but present to keep the
	// request shape honest.
	cfg := f.seedBlob([]byte("cfg"))
	body := buildManifest(cfg)
	resp := f.putManifest("mirror-upload-reject", body)
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 403 {
		t.Fatalf("status = %d, want 403; body=%s", resp.StatusCode, b)
	}
	if !strings.Contains(string(b), "repo_is_mirror") {
		t.Fatalf("body missing repo_is_mirror: %s", b)
	}
}
