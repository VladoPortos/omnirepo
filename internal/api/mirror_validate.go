// Package api — Phase 8 Plan 01 mirror-repo request-shape validators.
//
// These helpers enforce the 5 validation branches for the mirror fields on
// POST /api/v1/projects/{name}/repos and PATCH
// /api/v1/projects/{name}/repos/{type}/{repo}:
//
//   A. mirror_type_unsupported — is_mirror=true only with deb/rpm/pypi/helm
//   B. mirror_url_invalid      — upstream URL must be http(s) with a host
//   C. mirror_filter_invalid   — filter JSON must decode into the protocol
//                                 SyncFilter shape
//   D. mirror_cred_wrong_project — mirror_cred_id must belong to the same
//                                   project as the repo (T-08-01-07)
//   E. mirror_url_immutable    — PATCH cannot change is_mirror or
//                                 mirror_upstream_url
//
// Validation is JSON-shape-only here; the repo-layer migration ensures
// mirror_filter_json round-trips as TEXT. We deliberately do NOT import the
// protocol packages — a copy of the SyncFilter shape per protocol lives
// here (5–6 fields total) so the api package stays cycle-free.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/dxc-internal/omnirepo/internal/httperr"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"golang.org/x/text/unicode/norm"
)

// Reasonable upper bounds for the mirror_filter_json payload. These exist to
// prevent buffer-growth abuse on the validator and storage layers, not to
// enforce a tight product policy — an operator running a full Ubuntu archive
// could legitimately list thousands of packages. If you hit these ceilings in
// practice, raise them; don't treat them as product invariants.
const (
	maxFilterJSONBytes    = 64 * 1024 // matches the request body cap
	maxFilterArrayEntries = 4096      // per field (Names, Globs, ...)
	maxFilterStringLen    = 1024      // per entry, measured in bytes after NFC
)

// mirrorSupportedTypes enumerates repo types eligible for the is_mirror flag
// (D-01). Raw/S3/Docker remain out of scope; Git is widened in Phase 11
// (plan 11-05, GITMIRROR-01) — HTTPS+PAT only per GITMIRROR-05, with
// oci:// / ssh:// / file:// filtered out by validateMirrorUpstreamURL's
// scheme check.
var mirrorSupportedTypes = map[string]struct{}{
	"deb":  {},
	"rpm":  {},
	"pypi": {},
	"helm": {},
	"git":  {}, // Phase 11 / GITMIRROR-01 — HTTPS-PAT mirror via go-git v6.
}

// HelmSourceKind is the api-layer mirror of helm.EntrySourceKind.
// Duplicated here to keep the api package cycle-free (the header comment
// above the package declaration forbids importing protocol packages —
// the same rationale applies to a new helm import). The three constants
// are kept in lock-step with helm.EntrySource* by convention; downstream
// callers that need the helm-native kind map through a local switch.
type HelmSourceKind int

// Helm upstream classifier kinds (plan 11-02).
const (
	HelmSourceUnknown HelmSourceKind = iota
	HelmSourceHTTP
	HelmSourceOCI
)

// classifyHelmUpstream returns the fetch-transport kind for a helm mirror
// upstream URL (OCIHELM-03). Supported schemes: http, https, oci. Scheme
// comparison is case-insensitive. oci:// must have both a host and a
// path component ("oci://host/chart") — bare "oci://host" is rejected so
// the downstream pull has an unambiguous reference.
//
// Bare-host strings without any scheme (e.g. "registry-1.docker.io/foo")
// are rejected in v1.4 even though the Helm SDK accepts them — the
// validator requires an explicit scheme for UX clarity and to keep the
// Docker Hub gate in refuseDockerHubWithoutCred deterministic.
func classifyHelmUpstream(raw string) (HelmSourceKind, error) {
	if raw == "" {
		return HelmSourceUnknown, fmt.Errorf("mirror upstream URL is empty")
	}
	lower := strings.ToLower(raw)
	switch {
	case strings.HasPrefix(lower, "oci://"):
		rest := raw[len("oci://"):]
		if rest == "" || !strings.Contains(rest, "/") {
			return HelmSourceUnknown, fmt.Errorf("oci upstream missing host/path: %q", raw)
		}
		// First segment = host; must be non-empty.
		if idx := strings.Index(rest, "/"); idx == 0 || idx == -1 {
			return HelmSourceUnknown, fmt.Errorf("oci upstream missing host: %q", raw)
		}
		return HelmSourceOCI, nil
	case strings.HasPrefix(lower, "http://"), strings.HasPrefix(lower, "https://"):
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return HelmSourceUnknown, fmt.Errorf("invalid http upstream: %q", raw)
		}
		return HelmSourceHTTP, nil
	}
	return HelmSourceUnknown, fmt.Errorf("unsupported upstream scheme: %q", raw)
}

// validateMirrorUpstreamURL enforces http(s) scheme + non-empty host, with a
// repoType-gated widening for helm mirrors that additionally accept oci://
// (plan 11-02, OCIHELM-03). Rejects file://, javascript:, ftp://, bare
// paths, and missing hosts (T-08-01-03) regardless of repoType.
func validateMirrorUpstreamURL(raw, repoType string) bool {
	if raw == "" {
		return false
	}
	lower := strings.ToLower(raw)
	if strings.HasPrefix(lower, "oci://") {
		if repoType != "helm" {
			return false
		}
		_, err := classifyHelmUpstream(raw)
		return err == nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	if u.Host == "" {
		return false
	}
	return true
}

// debFilterShape mirrors internal/protocol/deb.SyncFilter. Keeps the api
// package cycle-free. PascalCase JSON keys match Go's default encoding of
// the source struct (no json tags) — that is the canonical *stored* shape
// in mirror_filter_json. The UnmarshalJSON below ALSO accepts snake_case
// keys (D-3) so REST/CLI callers can use the same casing convention as
// the rest of the OmniRepo API. Mixed-case payloads are not allowed —
// pick one.
type debFilterShape struct {
	Names      []string `json:"Names,omitempty"`
	Globs      []string `json:"Globs,omitempty"`
	Suites     []string `json:"Suites,omitempty"`
	Components []string `json:"Components,omitempty"`
	Arches     []string `json:"Arches,omitempty"`
}

// debFilterShapeBoth is the decode-side intermediate that recognizes BOTH
// the legacy PascalCase keys and the new snake_case keys for D-3. The
// non-empty side wins per field; if both sides are populated for the same
// field UnmarshalJSON returns an error so we don't silently drop one.
type debFilterShapeBoth struct {
	NamesP      []string `json:"Names,omitempty"`
	GlobsP      []string `json:"Globs,omitempty"`
	SuitesP     []string `json:"Suites,omitempty"`
	ComponentsP []string `json:"Components,omitempty"`
	ArchesP     []string `json:"Arches,omitempty"`
	NamesS      []string `json:"names,omitempty"`
	GlobsS      []string `json:"globs,omitempty"`
	SuitesS     []string `json:"suites,omitempty"`
	ComponentsS []string `json:"components,omitempty"`
	ArchesS     []string `json:"arches,omitempty"`
}

func (f *debFilterShape) UnmarshalJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var b debFilterShapeBoth
	if err := dec.Decode(&b); err != nil {
		return err
	}
	var err error
	if f.Names, err = pickEitherCase("Names", b.NamesP, b.NamesS); err != nil {
		return err
	}
	if f.Globs, err = pickEitherCase("Globs", b.GlobsP, b.GlobsS); err != nil {
		return err
	}
	if f.Suites, err = pickEitherCase("Suites", b.SuitesP, b.SuitesS); err != nil {
		return err
	}
	if f.Components, err = pickEitherCase("Components", b.ComponentsP, b.ComponentsS); err != nil {
		return err
	}
	if f.Arches, err = pickEitherCase("Arches", b.ArchesP, b.ArchesS); err != nil {
		return err
	}
	return nil
}

// rpmFilterShape mirrors internal/protocol/rpm.SyncFilter. PyPI and Helm
// share the same shape via type aliases below — defining UnmarshalJSON
// here covers all three protocols.
type rpmFilterShape struct {
	Names []string `json:"Names,omitempty"`
	Globs []string `json:"Globs,omitempty"`
}

type rpmFilterShapeBoth struct {
	NamesP []string `json:"Names,omitempty"`
	GlobsP []string `json:"Globs,omitempty"`
	NamesS []string `json:"names,omitempty"`
	GlobsS []string `json:"globs,omitempty"`
}

func (f *rpmFilterShape) UnmarshalJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var b rpmFilterShapeBoth
	if err := dec.Decode(&b); err != nil {
		return err
	}
	var err error
	if f.Names, err = pickEitherCase("Names", b.NamesP, b.NamesS); err != nil {
		return err
	}
	if f.Globs, err = pickEitherCase("Globs", b.GlobsP, b.GlobsS); err != nil {
		return err
	}
	return nil
}

// pickEitherCase chooses between PascalCase and snake_case values for the
// same logical field. Returns an error when both sides are populated so
// the caller surfaces mirror_filter_invalid rather than silently dropping
// one of the inputs.
func pickEitherCase(field string, pascal, snake []string) ([]string, error) {
	if len(pascal) > 0 && len(snake) > 0 {
		return nil, fmt.Errorf("filter: %s specified in both PascalCase and snake_case — pick one", field)
	}
	if len(snake) > 0 {
		return snake, nil
	}
	return pascal, nil
}

// pypiFilterShape mirrors internal/protocol/pypi.SyncFilter.
type pypiFilterShape = rpmFilterShape

// helmFilterShape mirrors internal/protocol/helm.SyncFilter.
type helmFilterShape = rpmFilterShape

// validateMirrorFilter decodes the filter against the protocol's SyncFilter
// shape. On success it returns the canonical form — re-serialized from the
// decoded struct with every string NFC-normalized — so the caller stores a
// single canonical byte sequence regardless of the input's Unicode form.
//
// Returns (false, nil) if any of the following holds:
//   - repoType is not one of deb/rpm/pypi/helm
//   - raw length exceeds maxFilterJSONBytes
//   - the JSON contains a duplicate key at any object nesting level
//   - decode fails, or unknown keys are present
//   - any array has more than maxFilterArrayEntries entries
//   - any string is not valid UTF-8 or exceeds maxFilterStringLen after NFC
//
// Empty or null filters are legal (sync-everything) and return (true, nil)
// so callers can store nothing in mirror_filter_json.
func validateMirrorFilter(repoType string, filter json.RawMessage) (bool, json.RawMessage) {
	if len(filter) == 0 {
		return true, nil
	}
	if len(filter) > maxFilterJSONBytes {
		return false, nil
	}
	// encoding/json silently replaces malformed UTF-8 byte sequences inside
	// string literals with U+FFFD; reject the whole payload at the byte level
	// instead so callers get a deterministic failure rather than a mangled
	// stored value.
	if !utf8.Valid(filter) {
		return false, nil
	}
	if !filterHasUniqueKeys(filter) {
		return false, nil
	}

	dec := json.NewDecoder(bytes.NewReader(filter))
	dec.DisallowUnknownFields()

	switch repoType {
	case "deb":
		var f debFilterShape
		if dec.Decode(&f) != nil {
			return false, nil
		}
		if !normalizeFilterStrings(&f.Names) || !normalizeFilterStrings(&f.Globs) ||
			!normalizeFilterStrings(&f.Suites) || !normalizeFilterStrings(&f.Components) ||
			!normalizeFilterStrings(&f.Arches) {
			return false, nil
		}
		return reencodeFilter(f)
	case "rpm":
		var f rpmFilterShape
		if dec.Decode(&f) != nil {
			return false, nil
		}
		if !normalizeFilterStrings(&f.Names) || !normalizeFilterStrings(&f.Globs) {
			return false, nil
		}
		return reencodeFilter(f)
	case "pypi":
		var f pypiFilterShape
		if dec.Decode(&f) != nil {
			return false, nil
		}
		if !normalizeFilterStrings(&f.Names) || !normalizeFilterStrings(&f.Globs) {
			return false, nil
		}
		return reencodeFilter(f)
	case "helm":
		var f helmFilterShape
		if dec.Decode(&f) != nil {
			return false, nil
		}
		if !normalizeFilterStrings(&f.Names) || !normalizeFilterStrings(&f.Globs) {
			return false, nil
		}
		return reencodeFilter(f)
	case "git":
		// Git mirrors are all-refs, no per-ref filter API (GITMIRROR-01).
		// Accept only the empty-object placeholder `{}` (or null/empty
		// raw — already handled above) and normalize to nil so the repo
		// row stores nothing. Any other payload is rejected.
		var f struct{}
		if dec.Decode(&f) != nil {
			return false, nil
		}
		return true, nil
	default:
		return false, nil
	}
}

// filterHasUniqueKeys walks every JSON object in the document and reports
// whether each key appears at most once within its own object. Nested objects
// are tracked independently via a per-depth seen-set.
//
// The walker tracks, per open object, whether the next string token is a key
// or a value (they alternate after the opening '{').
func filterHasUniqueKeys(raw json.RawMessage) bool {
	dec := json.NewDecoder(bytes.NewReader(raw))
	type frame struct {
		seen       map[string]struct{}
		isObject   bool
		expectKey  bool
	}
	var stack []*frame
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return len(stack) == 0
		}
		if err != nil {
			return false
		}
		switch v := tok.(type) {
		case json.Delim:
			switch v {
			case '{':
				stack = append(stack, &frame{seen: map[string]struct{}{}, isObject: true, expectKey: true})
			case '[':
				stack = append(stack, &frame{isObject: false})
			case '}', ']':
				if len(stack) == 0 {
					return false
				}
				stack = stack[:len(stack)-1]
				if n := len(stack); n > 0 && stack[n-1].isObject {
					stack[n-1].expectKey = true
				}
			}
		case string:
			if n := len(stack); n > 0 && stack[n-1].isObject {
				top := stack[n-1]
				if top.expectKey {
					if _, dup := top.seen[v]; dup {
						return false
					}
					top.seen[v] = struct{}{}
					top.expectKey = false
				} else {
					top.expectKey = true
				}
			}
		default:
			// bool / number / null — a value inside an object flips us back
			// to expect-key; inside an array no state needs updating.
			if n := len(stack); n > 0 && stack[n-1].isObject {
				stack[n-1].expectKey = true
			}
		}
	}
}

// normalizeFilterStrings mutates the slice in place: NFC-normalizes each
// entry and rejects entries that fail the UTF-8 / length gates. Reports
// whether the slice is still valid after normalization.
func normalizeFilterStrings(ss *[]string) bool {
	if ss == nil || *ss == nil {
		return true
	}
	if len(*ss) > maxFilterArrayEntries {
		return false
	}
	for i, s := range *ss {
		if !utf8.ValidString(s) {
			return false
		}
		n := norm.NFC.String(s)
		if len(n) > maxFilterStringLen {
			return false
		}
		(*ss)[i] = n
	}
	return true
}

// reencodeFilter marshals the decoded filter struct back to JSON so the
// caller stores a canonical byte sequence. `omitempty` on the struct tags
// keeps empty arrays out of the output.
func reencodeFilter(v any) (bool, json.RawMessage) {
	out, err := json.Marshal(v)
	if err != nil {
		return false, nil
	}
	return true, out
}

// mirrorCredOwnership asserts credID belongs to ownerProjectID.
// Returns (ok, exists):
//   - ok=true, exists=true:   cred exists and is owned by projectID
//   - ok=false, exists=true:  cross-project (T-08-01-07)
//   - ok=false, exists=false: cred id is missing or repo unavailable
//
// Handlers render the 400 mirror_cred_wrong_project envelope from
// !ok, regardless of which sub-case triggered — leaking
// existence/not-existence across projects would be an info-disclosure
// vector.
func mirrorCredOwnership(ctx context.Context, upstreamCreds *metadata.UpstreamCredsRepo, projectID, credID int64) (ok bool, exists bool) {
	if upstreamCreds == nil {
		return false, false
	}
	ownerID, err := upstreamCreds.GetProjectID(ctx, credID)
	if err != nil {
		return false, false
	}
	if ownerID != projectID {
		return false, true
	}
	return true, true
}

// dockerHubOCIHost is the single OCI-flavoured Docker Hub host name. The
// Docker Hub gate applies only to oci:// upstreams targeting this host —
// http(s) upstreams to the same host are a different product (the Docker
// Hub web UI, not a Helm chart source) and are handled by generic URL
// validation, not by this gate.
const dockerHubOCIHost = "registry-1.docker.io"

// refuseDockerHubWithoutCred returns a 422 httperr.Error with envelope
// code `mirror.docker_hub_requires_credential` when upstreamURL targets
// Docker Hub (registry-1.docker.io) via oci:// AND no credential is
// attached (credKind==""). Returns nil for any other combination so the
// caller can chain it with the rest of the validator suite (D-04,
// OCIHELM-05).
//
// Message copy is verbatim per D-04 — the 100/6h rate-limit phrase is the
// single operator-facing cue that distinguishes this gate from the generic
// "missing credential" UX. Host comparison is case-insensitive because
// the v1.2 UI does not lowercase-normalize upstream URLs before storing
// (T-11-02-01 mitigation).
//
// Plans 11-03 integrate this validator at the POST /repos/helm and
// PATCH /repos/helm endpoints — this plan only lands the function +
// tests so downstream work compiles against a known shape.
func refuseDockerHubWithoutCred(upstreamURL, credKind string) *httperr.Error {
	lower := strings.ToLower(upstreamURL)
	// Gate applies to oci:// upstreams only. https://registry-1.docker.io/*
	// is the web UI, not a chart source; other schemes fall out of scope.
	if !strings.HasPrefix(lower, "oci://") {
		return nil
	}
	rest := lower[len("oci://"):]
	host := rest
	if idx := strings.Index(rest, "/"); idx >= 0 {
		host = rest[:idx]
	}
	if host != dockerHubOCIHost {
		return nil
	}
	if credKind != "" {
		// Any non-empty cred kind unblocks. D-06 locks the expected kind
		// to "basic" for v1.4; this validator only enforces "something
		// is attached" so the caller's cred-kind filter owns the
		// kind-specific semantics.
		return nil
	}
	return httperr.Validation(
		"mirror.docker_hub_requires_credential",
		"Docker Hub enforces a 100 requests / 6h anonymous rate limit per source IP. Attach a basic credential (username + PAT) to sync reliably.",
		httperr.WithStatus(http.StatusUnprocessableEntity),
	)
}
