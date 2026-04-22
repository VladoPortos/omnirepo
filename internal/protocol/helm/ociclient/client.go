// Package ociclient isolates the Helm SDK registry.Client behind a narrow
// Client interface so internal/protocol/helm/sync_handler.go can remain
// testable with an in-memory fake (see fake.go) and so the dependency surface
// on helm.sh/helm/v3/pkg/registry stays pinned to this one file.
//
// Production impl wraps helm.sh/helm/v3/pkg/registry.Client. Two MANDATORY
// options are applied on every SDK-client construction:
//
//   - ClientOptEnableCache(false) — Phase 11 decision D-01, OCIHELM invariant.
//     The default cache directory is ~/.cache/helm/registry, which in a
//     FROM scratch container falls back to CWD and leaks credentials into
//     the mounted data volume. Disabling the cache preserves the air-gap /
//     single-volume invariant.
//   - ClientOptWriter(io.Discard) — Phase 11 decision D-03. Helm's Pull
//     prints a stderr warning line when it accepts the legacy
//     application/tar+gzip media type (see vendor/.../client.go:583).
//     We ACCEPT this media type silently per D-03; redirecting the writer
//     to io.Discard prevents the warning from polluting operator logs.
//     It also suppresses the "Pulled:/Digest:" informational lines.
//
// Per-call registry.Client construction (not cached across invocations):
// the Helm SDK's Login path writes credentials to a disk credentials file,
// which we never want. Passing ClientOptBasicAuth on every NewClient call
// avoids that disk round-trip and keeps credentials in-process only.
//
// Pitfall §8 (Phase 11 research): the Helm SDK's Pull uses
// context.Background() internally at vendor/.../client.go:538 — caller ctx
// is NOT propagated into the SDK call. Cancellation must come from
// transport-level timeouts on the injected *http.Client. The Client
// interface still accepts ctx to honor Go conventions for caller-side
// cancellation and to future-proof the signature if/when the SDK adds
// ctx propagation.
package ociclient

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"helm.sh/helm/v3/pkg/registry"
)

// AuthCreds carries optional Basic auth for an OCI pull. Empty User/Password
// fields mean anonymous pull. Password is the full credential string (PAT /
// password / registry token) — the Helm SDK handles whatever form of basic
// auth the registry accepts.
type AuthCreds struct {
	User, Password string
}

// ChartMeta is the subset of helm.sh/helm/v3/pkg/chart.Metadata the OCI pull
// flow surfaces to callers. The full chart metadata is re-derived from the
// downloaded .tgz bytes by helm.Parse in the commit-tail; this struct just
// carries enough for the caller to log / sanity-check / pre-flight compare
// against the upstream index entry before writing to disk.
type ChartMeta struct {
	Name        string
	Version     string
	AppVersion  string
	Description string
	Keywords    []string
}

// PullResult is what PullChart returns. Data is the raw .tgz bytes (never
// extracted — feeds straight into pathStore.Put). Digest is the OCI
// chart-layer digest in the form "sha256:<hex>" (this is the content digest,
// which is distinct from the manifest digest returned by Resolve — use this
// one for the helm_charts.digest column to match OCI chart-content
// semantics). Size is the chart-layer byte count.
type PullResult struct {
	Data   []byte
	Digest string
	Size   int64
	Meta   ChartMeta
}

// Client is the narrow OCI Helm client interface consumed by the Helm sync
// handler. All three methods accept ctx but the current Helm SDK does not
// propagate it; cancellation must be wired via the injected *http.Client.
type Client interface {
	// Resolve returns the manifest digest ("sha256:<hex>") for ref. Used
	// pre-flight by the sync handler to check dedup on
	// (repo_id, name, version, digest) BEFORE fetching chart bytes, which
	// avoids wasting rate-limit quota on already-cached digests.
	Resolve(ctx context.Context, ref string, creds AuthCreds) (digest string, err error)

	// ListTags enumerates available tags for a base chart ref (no tag
	// suffix). Reserved for future "mirror all versions" UX; in v1.4 the
	// sync handler only pulls tags the upstream index.yaml explicitly
	// lists, but including ListTags in the interface keeps FakeClient
	// usable for higher-level tests that need it.
	ListTags(ctx context.Context, ref string, creds AuthCreds) (tags []string, err error)

	// PullChart pulls the chart .tgz at ref. ref MUST include a tag
	// (e.g. "registry-1.docker.io/bitnamicharts/nginx:15.14.0"). The
	// legacy application/tar+gzip media type is accepted silently (D-03).
	PullChart(ctx context.Context, ref string, creds AuthCreds) (*PullResult, error)
}

// httpHelmClient is the production Client impl.
type httpHelmClient struct {
	httpClient *http.Client // caller-supplied shared *http.Client
}

// New constructs a production Client backed by the Helm SDK. The supplied
// httpClient is used for every outbound OCI request; pass the shared
// SyncDeps.HTTPClient so transport-level timeouts, TLS config, and any
// middleware the sync pool layers on top apply uniformly. Passing nil
// falls back to http.DefaultClient (intended for tests / smoke scripts;
// production wiring should always supply an explicit client).
func New(httpClient *http.Client) Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &httpHelmClient{httpClient: httpClient}
}

// normalizeRef strips the "oci://" scheme prefix. The Helm SDK's
// newReference already trims this for Pull, but Tags and Resolve call
// oras' registry.ParseReference / remote.NewRepository directly without
// the strip — so we normalize up-front to keep behavior uniform.
//
// Scheme comparison is case-insensitive to match validateMirrorUpstreamURL
// and the sync handler's dispatch (both use strings.ToLower before the
// HasPrefix check). Without this, an "OCI://…" ref accepted by the
// validator would reach ORAS with a scheme ORAS can't parse as a
// registry reference. Codex batch-09 review surfaced the inconsistency.
func normalizeRef(ref string) string {
	if len(ref) >= len("oci://") && strings.EqualFold(ref[:len("oci://")], "oci://") {
		return ref[len("oci://"):]
	}
	return ref
}

// newSDKClient builds a fresh registry.Client for a single operation.
// ClientOptEnableCache(false) + ClientOptWriter(io.Discard) are mandatory
// and enforced for every caller path.
func (h *httpHelmClient) newSDKClient(creds AuthCreds) (*registry.Client, error) {
	opts := []registry.ClientOption{
		registry.ClientOptHTTPClient(h.httpClient),
		registry.ClientOptEnableCache(false), // MANDATORY — D-01 / INV-11-01-01
		registry.ClientOptWriter(io.Discard), // silences legacy media-type warning — D-03 / INV-11-01-02
	}
	if creds.User != "" {
		opts = append(opts, registry.ClientOptBasicAuth(creds.User, creds.Password))
	}
	return registry.NewClient(opts...)
}

// PullChart implements Client.PullChart.
//
// NOTE on ctx: cli.Pull uses context.Background() internally
// (vendor/helm.sh/helm/v3/pkg/registry/client.go:538). The ctx argument is
// NOT propagated into the SDK call — cancellation comes from timeouts on
// h.httpClient.Transport. We still accept ctx to honor the interface
// contract and to avoid callers assuming cancellation works.
func (h *httpHelmClient) PullChart(_ context.Context, ref string, creds AuthCreds) (*PullResult, error) {
	cli, err := h.newSDKClient(creds)
	if err != nil {
		return nil, fmt.Errorf("ociclient: new client: %w", err)
	}
	res, err := cli.Pull(normalizeRef(ref), registry.PullOptWithChart(true))
	if err != nil {
		return nil, fmt.Errorf("ociclient: pull %s: %w", ref, err)
	}
	if res == nil || res.Chart == nil {
		return nil, fmt.Errorf("ociclient: pull %s: nil chart in result", ref)
	}
	pr := &PullResult{
		Data:   res.Chart.Data,
		Digest: res.Chart.Digest,
		Size:   res.Chart.Size,
	}
	if res.Chart.Meta != nil {
		pr.Meta = ChartMeta{
			Name:        res.Chart.Meta.Name,
			Version:     res.Chart.Meta.Version,
			AppVersion:  res.Chart.Meta.AppVersion,
			Description: res.Chart.Meta.Description,
			Keywords:    append([]string(nil), res.Chart.Meta.Keywords...),
		}
	}
	return pr, nil
}

// Resolve implements Client.Resolve.
func (h *httpHelmClient) Resolve(_ context.Context, ref string, creds AuthCreds) (string, error) {
	cli, err := h.newSDKClient(creds)
	if err != nil {
		return "", fmt.Errorf("ociclient: new client: %w", err)
	}
	desc, err := cli.Resolve(normalizeRef(ref))
	if err != nil {
		return "", fmt.Errorf("ociclient: resolve %s: %w", ref, err)
	}
	return desc.Digest.String(), nil
}

// ListTags implements Client.ListTags.
func (h *httpHelmClient) ListTags(_ context.Context, ref string, creds AuthCreds) ([]string, error) {
	cli, err := h.newSDKClient(creds)
	if err != nil {
		return nil, fmt.Errorf("ociclient: new client: %w", err)
	}
	tags, err := cli.Tags(normalizeRef(ref))
	if err != nil {
		return nil, fmt.Errorf("ociclient: tags %s: %w", ref, err)
	}
	return tags, nil
}
