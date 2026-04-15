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
)
