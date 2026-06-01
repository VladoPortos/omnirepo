package ociclient

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// FakeClient is an in-memory Client used by tests. Populate Results + Tags
// with canned data; use Errors to inject per-ref error returns. Calls are
// recorded for assertions.
//
// Key conventions:
//   - Results is keyed on the FULL ref WITH tag (after stripping oci://),
//     e.g. "registry-1.docker.io/bitnamicharts/nginx:15.14.0".
//   - Tags is keyed on the BASE ref WITHOUT tag,
//     e.g. "registry-1.docker.io/bitnamicharts/nginx".
//   - Errors is keyed on whichever form the caller would pass — matched
//     after oci:// prefix strip.
//
// All exported maps are safe to populate at test-setup time; the mutex
// protects concurrent reads+updates of Calls / LastCreds during the test.
type FakeClient struct {
	mu sync.Mutex

	// Results is the per-ref canned PullResult returned by PullChart /
	// whose Digest is returned by Resolve.
	Results map[string]*PullResult

	// Tags is the per-base-ref list returned by ListTags.
	Tags map[string][]string

	// Errors overrides the return for any matching ref across all three
	// methods. Presence in Errors wins over presence in Results/Tags.
	Errors map[string]error

	// Calls records every method invocation in the form
	// "{Method}:{ref}" (post oci:// strip). Order of appends matches
	// order of calls.
	Calls []string

	// LastCreds captures the AuthCreds argument from the most recent
	// method invocation. Tests assert it to verify credential threading.
	LastCreds AuthCreds
}

// NewFake returns an empty FakeClient ready for tests to populate. The
// three maps are initialized so tests can write directly
// (`f.Results["ref"] = ...`) without checking for nil.
func NewFake() *FakeClient {
	return &FakeClient{
		Results: map[string]*PullResult{},
		Tags:    map[string][]string{},
		Errors:  map[string]error{},
	}
}

// record appends a call entry under the mutex and captures LastCreds.
func (f *FakeClient) record(method, ref string, creds AuthCreds) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, fmt.Sprintf("%s:%s", method, ref))
	f.LastCreds = creds
}

// Resolve implements Client.Resolve against canned data.
func (f *FakeClient) Resolve(_ context.Context, ref string, creds AuthCreds) (string, error) {
	ref = strings.TrimPrefix(ref, "oci://")
	f.record("Resolve", ref, creds)
	if err, ok := f.Errors[ref]; ok {
		return "", err
	}
	if r, ok := f.Results[ref]; ok {
		return r.Digest, nil
	}
	return "", errors.New("fake: ref not found")
}

// ListTags implements Client.ListTags against canned data. The returned
// slice is a copy so callers mutating it does not affect later calls /
// other tests.
func (f *FakeClient) ListTags(_ context.Context, ref string, creds AuthCreds) ([]string, error) {
	ref = strings.TrimPrefix(ref, "oci://")
	f.record("ListTags", ref, creds)
	if err, ok := f.Errors[ref]; ok {
		return nil, err
	}
	if tags, ok := f.Tags[ref]; ok {
		return append([]string(nil), tags...), nil
	}
	return nil, errors.New("fake: tags not found")
}

// PullChart implements Client.PullChart against canned data. The returned
// PullResult is a shallow copy with Data (the .tgz bytes) cloned; callers
// can safely mutate the returned struct without affecting the canned
// data stored on the FakeClient.
func (f *FakeClient) PullChart(_ context.Context, ref string, creds AuthCreds) (*PullResult, error) {
	ref = strings.TrimPrefix(ref, "oci://")
	f.record("PullChart", ref, creds)
	if err, ok := f.Errors[ref]; ok {
		return nil, err
	}
	if r, ok := f.Results[ref]; ok {
		out := *r
		if r.Data != nil {
			out.Data = append([]byte(nil), r.Data...)
		}
		if r.Meta.Keywords != nil {
			out.Meta.Keywords = append([]string(nil), r.Meta.Keywords...)
		}
		return &out, nil
	}
	return nil, errors.New("fake: ref not found")
}

// Compile-time check: FakeClient satisfies Client. INV-11-01-03.
var _ Client = (*FakeClient)(nil)
