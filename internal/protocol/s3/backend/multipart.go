// Multipart path: MultipartBackend (Plan 01 probe confirmed PRESENT).
//
// Package backend — S3 multipart upload implementation on top of the
// s3_multipart_uploads + s3_multipart_parts tables and a disk staging tree
// at <DataRoot>/tmp/s3/<uploadId>/.
//
// Flow (D-18..D-22):
//  1. CreateMultipartUpload       — inserts row, mkdir staging.
//  2. UploadPart(N, body)         — writes <staging>/<N>.bin atomically,
//     upserts s3_multipart_parts(md5, size).
//  3. CompleteMultipartUpload     — merges parts in ascending order with
//     io.Copy into a single temp file, renames
//     atomically to the final bucket/key path,
//     upserts s3_objects, deletes multipart rows.
//  4. AbortMultipartUpload        — deletes multipart rows + staging tree.
//
// ETag math (RESEARCH §Pattern 5):
//
//	etag = hex(md5(hex-decode(part[0].md5) || hex-decode(part[1].md5) || ...)) + "-" + len
//
// Part-size bounds (D-22): UploadPart rejects > 5 GiB at the gate; the
// "each part (except last) >= 5 MiB" rule is checked in CompleteMultipartUpload
// because UploadPart does not know which part is last.
package backend

import (
	"context"
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/johannesboyne/gofakes3"

	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/storage"
)

// Compile-time MultipartBackend assertion — gofakes3 will dispatch multipart
// HTTP requests directly to these methods.
var _ gofakes3.MultipartBackend = (*Backend)(nil)

const (
	// Minimum part size (except last part): 5 MiB.
	minPartSize int64 = 5 * 1024 * 1024
	// Maximum part size: 5 GiB.
	maxPartSize int64 = 5 * 1024 * 1024 * 1024
	// AWS limits multipart uploads to 10000 parts.
	defaultPartCountLimit = 10000
)

// multipartStaging returns the per-upload staging directory.
func (b *Backend) multipartStaging(uploadID string) string {
	return filepath.Join(b.DataRoot, "tmp", "s3", uploadID)
}

func (b *Backend) partCountLimit() int {
	if b.PartCountLimit > 0 {
		return b.PartCountLimit
	}
	return defaultPartCountLimit
}

// CreateMultipartUpload opens a new upload. Returns the client-visible
// UploadID (uuid v4 string).
func (b *Backend) CreateMultipartUpload(bucket, object string, meta map[string]string) (gofakes3.UploadID, error) {
	ctx := context.Background()
	if err := validateObjectKey(object); err != nil {
		return "", err
	}
	id, ok, err := b.findBucketID(ctx, bucket)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", gofakes3.BucketNotFound(bucket)
	}
	uploadID := uuid.NewString()
	metaJSON := marshalMeta(meta)

	if err := b.DB.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := b.Multipart.StartUpload(ctx, tx, &metadata.S3MultipartUpload{
			UploadID:          uploadID,
			BucketID:          id,
			Key:               object,
			InitiatedByUserID: multipartInitiatorFallback, // REST layer sets this; see note.
			MetadataJSON:      metaJSON,
		})
		return err
	}); err != nil {
		return "", fmt.Errorf("backend: start upload: %w", err)
	}
	if err := os.MkdirAll(b.multipartStaging(uploadID), 0o750); err != nil {
		return "", fmt.Errorf("backend: mkdir staging: %w", err)
	}
	return gofakes3.UploadID(uploadID), nil
}

// multipartInitiatorFallback is the sentinel user id used when the gofakes3
// call path doesn't thread an authenticated user through to the Backend.
// The REST-API path (Plan 07) wraps CreateMultipartUpload with an explicit
// user id; this sentinel keeps the FK satisfied in the meantime. Value 0 is
// reserved; we use 1 (the bootstrap super-admin, present in every install).
const multipartInitiatorFallback int64 = 1

// verifyUploadOwnership checks that the upload exists, belongs to the requested
// bucket, and matches the expected object key. Prevents cross-bucket multipart
// attacks where a valid upload_id from bucket A is used against bucket B.
func (b *Backend) verifyUploadOwnership(ctx context.Context, bucket, object, uploadID string) (*metadata.S3MultipartUpload, error) {
	up, err := b.Multipart.FindUpload(ctx, uploadID)
	if err != nil {
		if errors.Is(err, metadata.ErrNotFound) {
			return nil, gofakes3.ErrNoSuchUpload
		}
		return nil, err
	}
	if up.Key != object {
		return nil, gofakes3.ErrNoSuchUpload
	}
	bucketID, ok, err := b.findBucketID(ctx, bucket)
	if err != nil {
		return nil, err
	}
	if !ok || up.BucketID != bucketID {
		return nil, gofakes3.ErrNoSuchUpload
	}
	return up, nil
}

// UploadPart stages one part under <staging>/<partNumber>.bin and records
// its md5 + size.
func (b *Backend) UploadPart(bucket, object string, id gofakes3.UploadID, partNumber int, contentLength int64, input io.Reader) (etag string, err error) {
	ctx := context.Background()
	if partNumber <= 0 || partNumber > b.partCountLimit() {
		return "", gofakes3.ErrorMessagef(gofakes3.ErrInvalidPart, "part number %d out of range (1..%d)", partNumber, b.partCountLimit())
	}
	if contentLength > maxPartSize {
		return "", gofakes3.ErrorMessage(gofakes3.ErrInvalidPart, "part exceeds 5 GiB maximum")
	}
	uploadID := string(id)
	if _, err := b.verifyUploadOwnership(ctx, bucket, object, uploadID); err != nil {
		return "", err
	}

	mu := b.bucketLock(bucket)
	mu.Lock()
	defer mu.Unlock()

	staging := b.multipartStaging(uploadID)
	if err := os.MkdirAll(staging, 0o750); err != nil {
		return "", fmt.Errorf("backend: mkdir staging: %w", err)
	}
	partPath := filepath.Join(staging, strconv.Itoa(partNumber)+".bin")

	hasher := md5.New()
	tee := io.TeeReader(input, hasher)
	written, err := storage.WriteAndRename(ctx, b.tmpRoot(), partPath, tee)
	if err != nil {
		return "", fmt.Errorf("backend: write part: %w", err)
	}
	if written > maxPartSize {
		_ = os.Remove(partPath)
		return "", gofakes3.ErrorMessage(gofakes3.ErrInvalidPart, "part exceeds 5 GiB maximum")
	}
	md5hex := hex.EncodeToString(hasher.Sum(nil))

	if err := b.DB.WriteTx(ctx, func(tx *sql.Tx) error {
		return b.Multipart.AddPart(ctx, tx, &metadata.S3MultipartPart{
			UploadID:   uploadID,
			PartNumber: partNumber,
			SizeBytes:  written,
			MD5:        md5hex,
		})
	}); err != nil {
		return "", fmt.Errorf("backend: upsert part: %w", err)
	}
	return `"` + md5hex + `"`, nil
}

// CompleteMultipartUpload concats parts in ascending order, writes the final
// object atomically, upserts s3_objects, and cleans up the staging tree.
func (b *Backend) CompleteMultipartUpload(bucket, object string, id gofakes3.UploadID, input *gofakes3.CompleteMultipartUploadRequest) (versionID gofakes3.VersionID, etag string, err error) {
	ctx := context.Background()
	uploadID := string(id)

	up, err := b.verifyUploadOwnership(ctx, bucket, object, uploadID)
	if err != nil {
		return "", "", err
	}
	bucketID, ok, err := b.findBucketID(ctx, bucket)
	if err != nil {
		return "", "", err
	}
	if !ok {
		return "", "", gofakes3.BucketNotFound(bucket)
	}

	mu := b.bucketLock(bucket)
	mu.Lock()
	defer mu.Unlock()

	stored, err := b.Multipart.ListParts(ctx, uploadID)
	if err != nil {
		return "", "", err
	}
	storedByNum := make(map[int]metadata.S3MultipartPart, len(stored))
	for _, p := range stored {
		storedByNum[p.PartNumber] = p
	}

	if input == nil || len(input.Parts) == 0 {
		return "", "", gofakes3.ErrorMessage(gofakes3.ErrInvalidPart, "empty complete request")
	}
	if len(input.Parts) > b.partCountLimit() {
		return "", "", gofakes3.ErrorMessagef(gofakes3.ErrInvalidPart, "part count %d exceeds limit %d", len(input.Parts), b.partCountLimit())
	}

	// Validate ascending order + ETag match + size bounds.
	parts := append([]gofakes3.CompletedPart(nil), input.Parts...)
	if !sort.SliceIsSorted(parts, func(i, j int) bool { return parts[i].PartNumber < parts[j].PartNumber }) {
		return "", "", gofakes3.ErrInvalidPartOrder
	}
	for i, cp := range parts {
		stored, present := storedByNum[cp.PartNumber]
		if !present {
			return "", "", gofakes3.ErrorMessagef(gofakes3.ErrInvalidPart, "part number %d not uploaded", cp.PartNumber)
		}
		want := stripQuotes(cp.ETag)
		if want != stored.MD5 {
			return "", "", gofakes3.ErrorMessagef(gofakes3.ErrInvalidPart, "etag mismatch for part %d", cp.PartNumber)
		}
		// Every non-last part must be >= 5 MiB.
		if i < len(parts)-1 && stored.SizeBytes < minPartSize {
			return "", "", gofakes3.ErrorMessagef(gofakes3.ErrInvalidPart, "part %d is below 5 MiB minimum (EntityTooSmall)", cp.PartNumber)
		}
		if stored.SizeBytes > maxPartSize {
			return "", "", gofakes3.ErrorMessagef(gofakes3.ErrInvalidPart, "part %d exceeds 5 GiB maximum (EntityTooLarge)", cp.PartNumber)
		}
	}

	// Streaming merge: io.Copy into a single writer that lands atomically at
	// <bucket>/<key>. We don't use storage.WriteAndRename directly because we
	// need a per-open file handle we control across multiple io.Copy calls —
	// instead we roll the same temp+fsync+rename pattern inline.
	dst := filepath.Join(b.bucketRoot(bucket), filepath.FromSlash(object))
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return "", "", fmt.Errorf("backend: mkdir dst parent: %w", err)
	}
	if err := os.MkdirAll(b.tmpRoot(), 0o750); err != nil {
		return "", "", fmt.Errorf("backend: mkdir tmp: %w", err)
	}
	tmpF, err := os.CreateTemp(b.tmpRoot(), ".omnirepo-mpu-*.tmp")
	if err != nil {
		return "", "", fmt.Errorf("backend: create temp: %w", err)
	}
	tmpName := tmpF.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()

	mergeHasher := md5.New() // hash over concatenated raw-md5 bytes → multipart ETag
	var total int64

	for _, cp := range parts {
		partPath := filepath.Join(b.multipartStaging(uploadID), strconv.Itoa(cp.PartNumber)+".bin")
		pf, err := os.Open(partPath)
		if err != nil {
			_ = tmpF.Close()
			return "", "", fmt.Errorf("backend: open part %d: %w", cp.PartNumber, err)
		}
		n, err := io.Copy(tmpF, pf)
		_ = pf.Close()
		if err != nil {
			_ = tmpF.Close()
			return "", "", fmt.Errorf("backend: copy part %d: %w", cp.PartNumber, err)
		}
		total += n
		// Feed raw md5 bytes into the merge hasher.
		raw, herr := hex.DecodeString(storedByNum[cp.PartNumber].MD5)
		if herr != nil {
			_ = tmpF.Close()
			return "", "", fmt.Errorf("backend: decode md5 for part %d: %w", cp.PartNumber, herr)
		}
		mergeHasher.Write(raw)
	}
	if err := tmpF.Sync(); err != nil {
		_ = tmpF.Close()
		return "", "", fmt.Errorf("backend: fsync temp: %w", err)
	}
	if err := tmpF.Close(); err != nil {
		return "", "", fmt.Errorf("backend: close temp: %w", err)
	}

	finalETag := fmt.Sprintf("%s-%d", hex.EncodeToString(mergeHasher.Sum(nil)), len(parts))

	// HI-03: rename first, then commit the DB tx. If rename fails, nothing
	// changed (DB still consistent, tmp cleaned up by deferred cleanup). If
	// rename succeeds but DB commit fails, we delete the freshly-renamed
	// final file so the DB row never points to an object that survives.
	if err := os.Rename(tmpName, dst); err != nil {
		return "", "", fmt.Errorf("backend: rename: %w", err)
	}
	cleanup = false
	if err := b.DB.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := b.Objects.Upsert(ctx, tx, &metadata.S3Object{
			BucketID:     bucketID,
			Key:          object,
			SizeBytes:    total,
			ETag:         finalETag,
			ContentType:  contentTypeFromMeta(up.MetadataJSON),
			MetadataJSON: up.MetadataJSON,
			SHA256:       "multipart:" + finalETag,
		}); err != nil {
			return err
		}
		return b.Multipart.DeleteUpload(ctx, tx, uploadID)
	}); err != nil {
		// Compensating file delete — the DB row was never persisted, so
		// the final file is now an orphan. Best-effort.
		_ = os.Remove(dst)
		return "", "", fmt.Errorf("backend: commit multipart: %w", err)
	}
	if pf, err := os.Open(filepath.Dir(dst)); err == nil {
		_ = pf.Sync()
		_ = pf.Close()
	}
	// Clean up staging tree (best-effort).
	_ = os.RemoveAll(b.multipartStaging(uploadID))

	return "", `"` + finalETag + `"`, nil
}

// AbortMultipartUpload removes rows + staging. Idempotent: returns nil if the
// upload is already gone.
func (b *Backend) AbortMultipartUpload(bucket, object string, id gofakes3.UploadID) error {
	ctx := context.Background()
	uploadID := string(id)
	_, err := b.verifyUploadOwnership(ctx, bucket, object, uploadID)
	if err != nil {
		if errors.Is(err, gofakes3.ErrNoSuchUpload) {
			_ = os.RemoveAll(b.multipartStaging(uploadID))
			return nil
		}
		return err
	}
	if err := b.DB.WriteTx(ctx, func(tx *sql.Tx) error {
		return b.Multipart.DeleteUpload(ctx, tx, uploadID)
	}); err != nil {
		return fmt.Errorf("backend: abort delete: %w", err)
	}
	_ = os.RemoveAll(b.multipartStaging(uploadID))
	return nil
}

// ListMultipartUploads is a minimal implementation — Plan 07 may refine the
// marker semantics. For Phase 4 we satisfy the interface with a single-page
// response; pagination semantics are not required by the spec.
func (b *Backend) ListMultipartUploads(bucket string, marker *gofakes3.UploadListMarker, prefix gofakes3.Prefix, limit int64) (*gofakes3.ListMultipartUploadsResult, error) {
	ctx := context.Background()
	id, ok, err := b.findBucketID(ctx, bucket)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, gofakes3.BucketNotFound(bucket)
	}
	rows, err := b.DB.Reader.QueryContext(ctx, `
		SELECT upload_id, key, initiated_at
		FROM s3_multipart_uploads WHERE bucket_id=? ORDER BY key, upload_id
	`, id)
	if err != nil {
		return nil, fmt.Errorf("backend: list mpu: %w", err)
	}
	defer func() { _ = rows.Close() }()
	res := &gofakes3.ListMultipartUploadsResult{
		Bucket:     bucket,
		MaxUploads: limit,
	}
	for rows.Next() {
		var upID, key, initiated string
		if err := rows.Scan(&upID, &key, &initiated); err != nil {
			return nil, err
		}
		if prefix.HasPrefix && !matchesPrefix(key, prefix.Prefix) {
			continue
		}
		t, _ := time.Parse("2006-01-02T15:04:05.000Z", initiated)
		res.Uploads = append(res.Uploads, gofakes3.ListMultipartUploadItem{
			Key:       key,
			UploadID:  gofakes3.UploadID(upID),
			Initiated: gofakes3.NewContentTime(t),
		})
	}
	return res, rows.Err()
}

// ListParts returns every part of an upload.
func (b *Backend) ListParts(bucket, object string, uploadID gofakes3.UploadID, marker int, limit int64) (*gofakes3.ListMultipartUploadPartsResult, error) {
	ctx := context.Background()
	if _, err := b.verifyUploadOwnership(ctx, bucket, object, string(uploadID)); err != nil {
		return nil, err
	}
	parts, err := b.Multipart.ListParts(ctx, string(uploadID))
	if err != nil {
		return nil, err
	}
	res := &gofakes3.ListMultipartUploadPartsResult{
		Bucket:   bucket,
		Key:      object,
		UploadID: uploadID,
		MaxParts: limit,
	}
	for _, p := range parts {
		if p.PartNumber <= marker {
			continue
		}
		res.Parts = append(res.Parts, gofakes3.ListMultipartUploadPartItem{
			PartNumber:   p.PartNumber,
			LastModified: gofakes3.NewContentTime(p.UploadedAt),
			ETag:         `"` + p.MD5 + `"`,
			Size:         p.SizeBytes,
		})
	}
	return res, nil
}

// SweepOrphanMultiparts aborts multipart uploads whose initiated_at is older
// than `now - 24h`. Intended to be invoked daily by the scheduler (D-21 /
// Plan 12 wiring).
func (b *Backend) SweepOrphanMultiparts(ctx context.Context, now time.Time) error {
	cutoff := now.Add(-24 * time.Hour)
	stale, err := b.Multipart.ListStaleUploads(ctx, cutoff)
	if err != nil {
		return fmt.Errorf("backend: list stale: %w", err)
	}
	for _, up := range stale {
		// Resolve bucket name for AbortMultipartUpload's contract.
		var bucketName string
		if err := b.DB.Reader.QueryRowContext(ctx,
			`SELECT name FROM s3_buckets WHERE id=?`, up.BucketID,
		).Scan(&bucketName); err != nil {
			// Bucket gone → still remove the orphan rows + staging.
			_ = b.DB.WriteTx(ctx, func(tx *sql.Tx) error {
				return b.Multipart.DeleteUpload(ctx, tx, up.UploadID)
			})
			_ = os.RemoveAll(b.multipartStaging(up.UploadID))
			continue
		}
		if err := b.AbortMultipartUpload(bucketName, up.Key, gofakes3.UploadID(up.UploadID)); err != nil {
			return fmt.Errorf("backend: abort stale %s: %w", up.UploadID, err)
		}
	}
	return nil
}

// -- internal helpers -----------------------------------------------------

func matchesPrefix(key, prefix string) bool {
	if prefix == "" {
		return true
	}
	return len(key) >= len(prefix) && key[:len(prefix)] == prefix
}

func contentTypeFromMeta(metaJSON string) string {
	if metaJSON == "" || metaJSON == "{}" {
		return ""
	}
	m := map[string]string{}
	if err := json.Unmarshal([]byte(metaJSON), &m); err != nil {
		return ""
	}
	if v, ok := m["Content-Type"]; ok {
		return v
	}
	if v, ok := m["content-type"]; ok {
		return v
	}
	return ""
}
