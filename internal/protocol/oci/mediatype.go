// Package oci implements OmniRepo's OCI Distribution v1.1 surface at /v2.
//
// Plan 02-05 ships the skeleton: router, ping, token issue, Bearer verify
// middleware, and WWW-Authenticate challenge. Plans 02-06 (blobs) and 02-07
// (manifests/tags/catalog) plug real routes into the subrouter exposed by
// Handler.Mount.
package oci

// OCI + Docker manifest media-type constants. Downstream plans 02-06/02-07
// reach for these when negotiating Accept / Content-Type on manifest read
// and write paths. Keeping the literals in one file prevents subtle drift
// between the v2 and v3 media types (the ".list" / ".index" pair).
const (
	MediaTypeOCIManifest        = "application/vnd.oci.image.manifest.v1+json"
	MediaTypeDockerManifestV2   = "application/vnd.docker.distribution.manifest.v2+json"
	MediaTypeOCIIndex           = "application/vnd.oci.image.index.v1+json"
	MediaTypeDockerManifestList = "application/vnd.docker.distribution.manifest.list.v2+json"

	// Helm OCI chart config — the canonical marker for a manifest that
	// wraps a chart pushed via `helm push oci://…`. Identifies WHICH
	// manifests are Helm; the chart bytes themselves live in a layer (see
	// MediaTypeHelmChartContentV1 below). Plan 07-04 S-03b: the OCI
	// manifestPut post-commit hook sniffs for this exact string on
	// helm-type repos to trigger the forward mirror into the traditional
	// /<project>/helm/<repo>/charts/ tree.
	MediaTypeHelmChartConfigV1 = "application/vnd.cncf.helm.config.v1+json"

	// Helm OCI chart content — the canonical mediaType for the layer that
	// carries the chart .tgz bytes. Helm v3 charts put the chart here and
	// may ALSO ship a separate provenance-file layer (mediaType
	// "application/vnd.cncf.helm.chart.provenance.v1.prov") alongside it.
	// The mirror hook MUST pick the CHART layer by mediaType, never by
	// layer index or by assuming exactly one layer (breaking charts with
	// provenance is an S-03b regression waiting to happen).
	MediaTypeHelmChartContentV1 = "application/vnd.cncf.helm.chart.content.v1.tar+gzip"
)
