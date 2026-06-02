package oci_test

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"regexp"
	"testing"
)

// The docker_blobs refcount invariant: ref_count(blob) == the number of
// docker_manifests rows (in any repo) whose body references that blob digest.
// A blob is GC-eligible exactly when no manifest references it. These tests
// pin that invariant across multi-tag push, multi-tag delete, and tag-move —
// the cases where the historical per-tag accounting leaked refs (so blobs
// never GC'd) or, worse, freed blobs of a manifest that still existed.

var digestRe = regexp.MustCompile(`sha256:[0-9a-f]+`)

// expectedBlobRefcounts recomputes, from the source of truth (manifest
// bodies), how many manifest rows reference each blob digest. A manifest is
// counted once per blob it references regardless of how many tags point at it.
func expectedBlobRefcounts(t *testing.T, db *sql.DB) map[string]int64 {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		`SELECT digest, body FROM docker_manifests`)
	if err != nil {
		t.Fatalf("query manifests: %v", err)
	}
	defer func() { _ = rows.Close() }()
	want := map[string]int64{}
	for rows.Next() {
		var mfDigest string
		var body []byte
		if err := rows.Scan(&mfDigest, &body); err != nil {
			t.Fatalf("scan manifest: %v", err)
		}
		seen := map[string]struct{}{}
		for _, d := range digestRe.FindAllString(string(body), -1) {
			if d == mfDigest {
				continue // a manifest body does not "reference" itself as a blob
			}
			if _, ok := seen[d]; ok {
				continue // one manifest counts once per distinct referenced blob
			}
			seen[d] = struct{}{}
			want[d]++
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return want
}

// assertBlobRefcountsConsistent is the golden check: every docker_blobs row's
// ref_count must equal the number of manifest rows referencing it. Blobs that
// no manifest references must be at 0 (GC-eligible) — never stuck above 0
// (leak) and never below the live reference count (data loss).
func assertBlobRefcountsConsistent(t *testing.T, f *manifestFixture) {
	t.Helper()
	want := expectedBlobRefcounts(t, f.db.Reader)
	rows, err := f.db.Reader.QueryContext(context.Background(),
		`SELECT digest, ref_count FROM docker_blobs`)
	if err != nil {
		t.Fatalf("query blobs: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var digest string
		var rc int64
		if err := rows.Scan(&digest, &rc); err != nil {
			t.Fatalf("scan blob: %v", err)
		}
		if rc != want[digest] {
			t.Errorf("refcount invariant violated: blob %s ref_count=%d, want %d (live manifest references)",
				digest, rc, want[digest])
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
}

func blobRefcount(t *testing.T, f *manifestFixture, digest string) int64 {
	t.Helper()
	b, err := f.blobs.Stat(context.Background(), digest)
	if err != nil {
		t.Fatalf("stat %s: %v", digest, err)
	}
	if b == nil {
		t.Fatalf("blob %s missing", digest)
	}
	return b.RefCount
}

// TestMultiTagPush_BlobRefcountPerManifest: pushing one manifest to two tags
// must leave each referenced blob at ref_count 1 — the blob is referenced by a
// single manifest, regardless of how many tags point at it. The historical
// per-tag accounting bumped it to 2, so the blob could never be GC'd after the
// tags were removed.
func TestMultiTagPush_BlobRefcountPerManifest(t *testing.T) {
	f := newManifestFixture(t, false)
	cfg := f.seedBlob([]byte("cfg-multitag"))
	l1 := f.seedBlob([]byte("layer-multitag"))
	body := buildManifest(cfg, l1)

	for _, tag := range []string{"v1", "v2"} {
		resp := f.putManifest(tag, body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("push %s: status %d", tag, resp.StatusCode)
		}
	}

	for _, d := range []string{cfg, l1} {
		if got := blobRefcount(t, f, d); got != 1 {
			t.Errorf("%s ref_count=%d, want 1 (one manifest, two tags)", d, got)
		}
	}
	assertBlobRefcountsConsistent(t, f)
}

// TestMultiTagDelete_NoRefcountLeak: with one manifest under two tags,
// deleting the first tag must keep the blobs referenced (manifest still
// tagged), and deleting the second tag must drop them to 0 (manifest reaped).
// The historical bug left them stuck at 1 forever.
func TestMultiTagDelete_NoRefcountLeak(t *testing.T) {
	f := newManifestFixture(t, false)
	cfg := f.seedBlob([]byte("cfg-mtd"))
	l1 := f.seedBlob([]byte("layer-mtd"))
	body := buildManifest(cfg, l1)
	for _, tag := range []string{"v1", "v2"} {
		resp := f.putManifest(tag, body)
		resp.Body.Close()
	}

	// Delete first tag → manifest still tagged v2 → blobs stay referenced.
	del := f.deleteManifest("v1")
	del.Body.Close()
	if del.StatusCode != http.StatusAccepted {
		t.Fatalf("delete v1: %d", del.StatusCode)
	}
	for _, d := range []string{cfg, l1} {
		if got := blobRefcount(t, f, d); got != 1 {
			t.Errorf("after del v1: %s ref_count=%d, want 1 (manifest still tagged v2)", d, got)
		}
	}
	assertBlobRefcountsConsistent(t, f)

	// Delete last tag → manifest reaped → blobs free.
	del2 := f.deleteManifest("v2")
	del2.Body.Close()
	if del2.StatusCode != http.StatusAccepted {
		t.Fatalf("delete v2: %d", del2.StatusCode)
	}
	for _, d := range []string{cfg, l1} {
		if got := blobRefcount(t, f, d); got != 0 {
			t.Errorf("after del v2: %s ref_count=%d, want 0 (manifest reaped)", d, got)
		}
	}
	assertBlobRefcountsConsistent(t, f)
}

// TestTagMove_DoesNotFreeStillTaggedManifestBlobs: this is the data-loss case.
// Manifest M is tagged v1 and v2. Re-pointing v1 to a different manifest N must
// NOT free M's blobs — M is still pullable via v2. The historical tag-move path
// decremented the prior manifest's refs unconditionally, dropping M's blobs to
// 0 while M still existed → GC would delete bytes still served via v2.
func TestTagMove_DoesNotFreeStillTaggedManifestBlobs(t *testing.T) {
	f := newManifestFixture(t, false)
	cfgM := f.seedBlob([]byte("cfg-M"))
	l1 := f.seedBlob([]byte("layer-M"))
	cfgN := f.seedBlob([]byte("cfg-N"))
	l2 := f.seedBlob([]byte("layer-N"))
	bodyM := buildManifest(cfgM, l1)
	bodyN := buildManifest(cfgN, l2)

	for _, tag := range []string{"v1", "v2"} {
		resp := f.putManifest(tag, bodyM)
		resp.Body.Close()
	}
	// Re-point v1 onto N; M is still tagged v2.
	resp := f.putManifest("v1", bodyN)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("push N to v1: %d", resp.StatusCode)
	}

	// M's blobs must survive (M still tagged v2).
	for _, d := range []string{cfgM, l1} {
		if got := blobRefcount(t, f, d); got != 1 {
			t.Errorf("after tag-move: M blob %s ref_count=%d, want 1 (M still tagged v2)", d, got)
		}
	}
	// N's blobs referenced once.
	for _, d := range []string{cfgN, l2} {
		if got := blobRefcount(t, f, d); got != 1 {
			t.Errorf("after tag-move: N blob %s ref_count=%d, want 1", d, got)
		}
	}
	// M still pullable via v2.
	g := f.getManifest("v2")
	g.Body.Close()
	if g.StatusCode != http.StatusOK {
		t.Fatalf("M not pullable via v2 after tag-move: %d", g.StatusCode)
	}
	assertBlobRefcountsConsistent(t, f)
}

// TestDigestDelete_IndexChild_Refused pins the guard: a manifest still
// referenced by an image index (ref_count > 0) cannot be removed by a
// digest-form DELETE — doing so would dangle the parent index. The request is
// refused with 405 and the child stays pullable. After the index is deleted
// (releasing the reference), the child becomes deletable.
func TestDigestDelete_IndexChild_Refused(t *testing.T) {
	f := newManifestFixture(t, false)
	cfg := f.seedBlob([]byte("cfg-idx-child"))
	l1 := f.seedBlob([]byte("layer-idx-child"))
	childBody := buildManifest(cfg, l1)

	// Push the child image manifest (must exist before the index references it).
	cr := f.putManifest("child", childBody)
	cr.Body.Close()
	if cr.StatusCode != http.StatusCreated {
		t.Fatalf("push child: %d", cr.StatusCode)
	}
	childDigest := cr.Header.Get("Docker-Content-Digest")
	if childDigest == "" {
		t.Fatal("no child digest")
	}

	// Push an index referencing the child.
	indexBody := []byte(fmt.Sprintf(
		`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":%q,"size":%d}]}`,
		childDigest, len(childBody)))
	ir := f.putManifest("latest", indexBody)
	ir.Body.Close()
	if ir.StatusCode != http.StatusCreated {
		t.Fatalf("push index: %d", ir.StatusCode)
	}

	// Digest-delete of the index child must be refused with 405.
	del := f.deleteManifest(childDigest)
	del.Body.Close()
	if del.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("delete index child: status=%d, want 405", del.StatusCode)
	}
	// Child still pullable; index intact.
	g := f.getManifest(childDigest)
	g.Body.Close()
	if g.StatusCode != http.StatusOK {
		t.Fatalf("child not pullable after refused delete: %d", g.StatusCode)
	}
	gi := f.getManifest("latest")
	gi.Body.Close()
	if gi.StatusCode != http.StatusOK {
		t.Fatalf("index not pullable after refused child delete: %d", gi.StatusCode)
	}
	assertBlobRefcountsConsistent(t, f)

	// Delete the index → releases the child reference → child now deletable.
	di := f.deleteManifest(f.indexDigest(t, "latest"))
	di.Body.Close()
	if di.StatusCode != http.StatusAccepted {
		t.Fatalf("delete index: %d", di.StatusCode)
	}
	dc := f.deleteManifest(childDigest)
	dc.Body.Close()
	if dc.StatusCode != http.StatusAccepted {
		t.Fatalf("delete child after index gone: status=%d, want 202", dc.StatusCode)
	}
	assertBlobRefcountsConsistent(t, f)
}

// indexDigest resolves the manifest digest a tag points at (helper for
// deleting the index by digest).
func (f *manifestFixture) indexDigest(t *testing.T, tag string) string {
	t.Helper()
	var d string
	err := f.db.Reader.QueryRowContext(context.Background(),
		`SELECT digest FROM docker_tags WHERE repo_id=? AND tag=?`, f.repoID, tag).Scan(&d)
	if err != nil {
		t.Fatalf("resolve tag %s: %v", tag, err)
	}
	return d
}
