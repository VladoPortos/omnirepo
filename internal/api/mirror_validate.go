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
	"io"
	"net/url"
	"unicode/utf8"

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
// (D-01). Raw/S3/Git/Docker are out of scope for v1.1 per the design spec.
var mirrorSupportedTypes = map[string]struct{}{
	"deb":  {},
	"rpm":  {},
	"pypi": {},
	"helm": {},
}

// validateMirrorUpstreamURL enforces http(s) scheme + non-empty host.
// Rejects file://, javascript:, ftp://, bare paths, and missing hosts
// (T-08-01-03).
func validateMirrorUpstreamURL(raw string) bool {
	if raw == "" {
		return false
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
// the source struct (no json tags).
type debFilterShape struct {
	Names      []string `json:"Names,omitempty"`
	Globs      []string `json:"Globs,omitempty"`
	Suites     []string `json:"Suites,omitempty"`
	Components []string `json:"Components,omitempty"`
	Arches     []string `json:"Arches,omitempty"`
}

// rpmFilterShape mirrors internal/protocol/rpm.SyncFilter.
type rpmFilterShape struct {
	Names []string `json:"Names,omitempty"`
	Globs []string `json:"Globs,omitempty"`
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
