//go:build live_oci

// Live E2E: pull a real Bitnami chart from Docker Hub.
//
// Gated behind the `live_oci` build tag and runs only under
// `make test-live-oci`. Requires env:
//   - DOCKERHUB_USER   — Docker Hub username
//   - DOCKERHUB_TOKEN  — Docker Hub PAT (Read:Public_Repos scope suffices)
//
// Skips cleanly when either is absent so CI without secrets stays green.
//
// Scope guard:
// This file ships only three smoke checks against the real Docker Hub
// endpoint — a Resolve, a PullChart, and a Resolve re-run for digest
// stability. The full SyncHandler.Handle round-trip is NOT exercised
// here because the hermetic integration tests in
// sync_oci_integration_test.go already cover the dedup, rebound,
// mixed-index, and regen-HTTP-urls behavior. What the live test uniquely
// proves is that:
//
//   • The real ociclient + Helm SDK can talk OCI to Docker Hub.
//   • Basic credentials flow through the wrapper into the SDK.
//   • Canonical AND legacy Bitnami media types both parse without the
//     wrapper emitting noise (enforced by ClientOptWriter(io.Discard)
//     in client.go — if a warning ever leaks, test-live-oci output
//     surfaces it).
//
// NOT designed for frequent CI — intended for local/pre-release sanity.
// Bitnami publishes nginx chart versions at
// oci://registry-1.docker.io/bitnamicharts/nginx; the test resolves the
// tag via a ListTags probe rather than hard-coding a version that might
// be yanked. redis is the documented fallback if nginx availability
// drifts.

package helm_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/vladoportos/omnirepo/internal/protocol/helm"
	"github.com/vladoportos/omnirepo/internal/protocol/helm/ociclient"
)

const (
	// liveOCIUpstream — Bitnami nginx chart. Smallest stable, actively
	// maintained primary target. Fallback (if nginx drifts):
	// registry-1.docker.io/bitnamicharts/redis.
	liveOCIUpstream = "oci://registry-1.docker.io/bitnamicharts/nginx"
)

func TestLiveOCIBitnamiSync(t *testing.T) {
	user := os.Getenv("DOCKERHUB_USER")
	token := os.Getenv("DOCKERHUB_TOKEN")
	if user == "" || token == "" {
		t.Skip("DOCKERHUB_USER / DOCKERHUB_TOKEN unset; skipping live OCI test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Default http.Client is fine at this depth — transport-level timeout
	// comes from the ctx deadline above (though the Helm SDK does not
	// propagate ctx; see the NOTE in ociclient/client.go PullChart).
	// Production wiring in phase3_sync.go uses a shared, timeout-configured
	// *http.Client; here we accept the default since this test is opt-in
	// and developer-driven.
	cli := ociclient.New(nil)
	creds := ociclient.AuthCreds{User: user, Password: token}

	// Smoke 1: list tags on the base ref, pick the first one. Avoids
	// hard-coding a version that Bitnami might yank. ListTags applies an
	// oras-side semver filter — any parseable semver tag is acceptable.
	tags, err := cli.ListTags(ctx, liveOCIUpstream, creds)
	if err != nil {
		t.Fatalf("list tags %s: %v", liveOCIUpstream, err)
	}
	if len(tags) == 0 {
		t.Fatalf("list tags %s: upstream returned 0 tags (expected >=1)", liveOCIUpstream)
	}
	tag := tags[0]
	ref := liveOCIUpstream + ":" + tag

	// Smoke 2: resolve the chosen tag to a manifest digest.
	digest, err := cli.Resolve(ctx, ref, creds)
	if err != nil {
		t.Fatalf("resolve %s: %v", ref, err)
	}
	if !strings.HasPrefix(digest, "sha256:") || len(digest) < len("sha256:")+8 {
		t.Fatalf("resolve: unexpected digest shape %q (want sha256:<hex>)", digest)
	}

	// Smoke 3: pull the chart. Asserts bytes land, chart metadata parses,
	// and the returned chart name matches the ref's last segment.
	res, err := cli.PullChart(ctx, ref, creds)
	if err != nil {
		t.Fatalf("pull %s: %v", ref, err)
	}
	if res == nil {
		t.Fatalf("pull %s: nil result", ref)
	}
	if len(res.Data) == 0 {
		t.Fatalf("pull %s: empty chart data", ref)
	}
	if res.Meta.Name != "nginx" {
		t.Errorf("pull %s: chart meta name = %q; want %q", ref, res.Meta.Name, "nginx")
	}
	if res.Meta.Version == "" {
		t.Errorf("pull %s: chart meta version is empty", ref)
	}
	// Chart-layer digest parity check with the manifest digest via a
	// second resolve — both must be shaped sha256:<hex>. They may differ
	// (manifest digest ≠ layer digest is normal), so we only assert
	// shape + stability.
	if !strings.HasPrefix(res.Digest, "sha256:") || len(res.Digest) < len("sha256:")+8 {
		t.Errorf("pull %s: chart-layer digest shape = %q; want sha256:<hex>", ref, res.Digest)
	}

	// Smoke 4: a second Resolve returns the same manifest digest.
	// Upstream-stability check — proves the sync handler's dedup path
	// (which pre-flights Resolve before PullChart) has a stable input
	// at this moment in time. Not a tag-rebound canary — Bitnami re-tags
	// rarely, and this assertion only spans the test run.
	digest2, err := cli.Resolve(ctx, ref, creds)
	if err != nil {
		t.Fatalf("resolve (re-run) %s: %v", ref, err)
	}
	if digest != digest2 {
		t.Fatalf("digest drift between two resolves: %q vs %q", digest, digest2)
	}

	// Touch the EntrySourceOCI constant to assert it is importable in the
	// live file. This keeps the live test wired to the classifier symbol
	// the non-live integration tests exercise — a refactor that renames
	// EntrySourceOCI will break the live test compile alongside the
	// hermetic ones.
	_ = helm.EntrySourceOCI

	t.Logf("live OCI smoke passed: ref=%s manifest=%s layer=%s size=%d",
		ref, digest, res.Digest, len(res.Data))
}
