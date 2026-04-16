// Package backend implements the gofakes3.Backend + gofakes3.MultipartBackend
// surfaces on top of OmniRepo's typed metadata repos + storage primitives
// (Phase 04 Plan 06, D-15..D-22).
//
// Split across three files:
//   - backend.go — bucket CRUD + object Put/Get/Head/Delete
//   - list.go    — ListBuckets / ListBucket with prefix+delimiter pagination
//   - multipart.go — Create/Upload/Complete/Abort + OrphanGC sweeper
//
// Design notes:
//   - Every write path uses storage.WriteAndRename so mid-stream client
//     disconnects never leave partial files at the canonical key path.
//   - Per-bucket writes are serialized via storage.Locks. Reads are
//     lock-free — they go straight to the reader pool.
//   - PutObject computes sha256 + md5 in a single io.TeeReader stream.
//     ETag for single-object put = hex(md5). MetadataJSON is the JSON
//     serialization of the user-metadata header map.
//   - Bucket provisioning is REST-driven (Plan 07). gofakes3's
//     CreateBucket path is exercised only if a client PUTs a bucket that
//     doesn't exist yet; we honour the interface by attaching it to the
//     backend's DefaultProjectID (if set by the admin wiring layer).
package backend

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/johannesboyne/gofakes3"

	"github.com/dxc-internal/omnirepo/internal/httpx"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/storage"
)

// lockProject is the synthetic project key used for per-bucket mutexes.
// S3 buckets live in a global namespace (D-05) so one sentinel string is
// sufficient; the Repo field of RepoKey carries the bucket name.
const lockProject = "s3"

// Backend is the gofakes3.Backend / gofakes3.MultipartBackend implementation.
//
// Caller supplies the DataRoot (e.g. /var/lib/omnirepo) — files land under
// <DataRoot>/s3/<bucket>/<key>. Multipart staging goes under <DataRoot>/tmp/s3.
type Backend struct {
	DataRoot  string
	DB        *metadata.DB
	Objects   *metadata.S3ObjectsRepo
	Multipart *metadata.S3MultipartRepo
	Locks     storage.Locks

	// DefaultProjectID is the project that owns any bucket created via the
	// gofakes3 CreateBucket path. Zero means CreateBucket is disabled at
	// the S3 layer (REST path must provision buckets).
	DefaultProjectID int64

	// PartCountLimit caps the number of parts per multipart upload. AWS
	// limit is 10000. Zero → use default.
	PartCountLimit int
}

// Compile-time interface assertions.
var (
	_ gofakes3.Backend          = (*Backend)(nil)
	_ gofakes3.MultipartBackend = (*Backend)(nil)
)

// New constructs a Backend with sensible defaults.
func New(dataRoot string, db *metadata.DB, locks storage.Locks) *Backend {
	return &Backend{
		DataRoot:       dataRoot,
		DB:             db,
		Objects:        metadata.NewS3ObjectsRepo(db),
		Multipart:      metadata.NewS3MultipartRepo(db),
		Locks:          locks,
		PartCountLimit: 10000,
	}
}

// bucketRoot returns the on-disk directory where a bucket's objects live.
func (b *Backend) bucketRoot(name string) string {
	return filepath.Join(b.DataRoot, "s3", name)
}

// tmpRoot returns the shared tmp directory for atomic writes.
func (b *Backend) tmpRoot() string {
	return filepath.Join(b.DataRoot, "tmp", "s3")
}

// bucketLock returns the per-bucket mutex (storage.Locks keyed on bucket name).
// Call .Lock() before any write, .Unlock() after the writer tx commits.
func (b *Backend) bucketLock(name string) *lockedMutex {
	mu := b.Locks.For(storage.RepoKey{Project: lockProject, Type: "bucket", Repo: name})
	return &lockedMutex{mu: mu}
}

type lockedMutex struct {
	mu interface {
		Lock()
		Unlock()
	}
}

func (l *lockedMutex) Lock()   { l.mu.Lock() }
func (l *lockedMutex) Unlock() { l.mu.Unlock() }

// bucketNameRe matches the AWS-subset bucket-name regex (D-06).
var bucketNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9.\-]{2,62}$`)

// validateBucketName enforces D-06: length 3..63, lowercase alnum + dot +
// hyphen, no leading/trailing dot, no double dots, not an IPv4 literal, and
// not a reserved top-level URL prefix. Returns a gofakes3-style error on fail.
func validateBucketName(name string) error {
	if !bucketNameRe.MatchString(name) {
		return gofakes3.ErrorMessage(gofakes3.ErrInvalidBucketName, `bucket name must match ^[a-z0-9][a-z0-9.\-]{2,62}$`)
	}
	if strings.HasSuffix(name, ".") {
		return gofakes3.ErrorMessage(gofakes3.ErrInvalidBucketName, "bucket name may not end with '.'")
	}
	if strings.Contains(name, "..") {
		return gofakes3.ErrorMessage(gofakes3.ErrInvalidBucketName, "bucket name may not contain consecutive dots")
	}
	// Reject IPv4 literal.
	if ip := net.ParseIP(name); ip != nil && ip.To4() != nil {
		return gofakes3.ErrorMessage(gofakes3.ErrInvalidBucketName, "bucket name may not be an IPv4 literal")
	}
	// Reject reserved prefixes (D-17 / Phase 1 D-26).
	if httpx.IsReserved(name) {
		return gofakes3.ErrorMessage(gofakes3.ErrInvalidBucketName, "bucket name is a reserved prefix")
	}
	return nil
}

// validateObjectKey rejects keys that would let a crafted request escape the
// bucket tree via path-traversal. gofakes3 normalizes most of this but we
// belt-and-brace here for T-04-06-03.
func validateObjectKey(key string) error {
	if key == "" {
		return gofakes3.ErrorMessage(gofakes3.ErrInvalidArgument, "key must not be empty")
	}
	if strings.HasPrefix(key, "/") {
		return gofakes3.ErrorMessage(gofakes3.ErrInvalidArgument, "key must not start with '/'")
	}
	// Reject any segment that is exactly ".." — handles "../", "a/../b", etc.
	for _, seg := range strings.Split(key, "/") {
		if seg == ".." {
			return gofakes3.ErrorMessage(gofakes3.ErrInvalidArgument, "key must not contain '..' segments")
		}
	}
	return nil
}

// findBucketID looks up (id, name, exists?) for a bucket. Soft-deleted rows
// (deleted_at IS NOT NULL) are treated as not-found.
func (b *Backend) findBucketID(ctx context.Context, name string) (int64, bool, error) {
	var id int64
	err := b.DB.Reader.QueryRowContext(ctx,
		`SELECT id FROM s3_buckets WHERE name=? AND deleted_at IS NULL`, name,
	).Scan(&id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("backend: find bucket: %w", err)
	}
	return id, true, nil
}

// ListBuckets returns every non-deleted bucket. (List implementation lives in list.go.)

// CreateBucket is the gofakes3.Backend method. Requires DefaultProjectID to
// be non-zero; otherwise returns ErrInvalidBucketName advising the admin to
// use the REST API. Production wiring (Plan 07) keeps DefaultProjectID=0 and
// fronts gofakes3's /<bucket> PUT with middleware that returns a clear error.
func (b *Backend) CreateBucket(name string) error {
	if err := validateBucketName(name); err != nil {
		return err
	}
	if b.DefaultProjectID == 0 {
		return gofakes3.ErrorMessage(gofakes3.ErrInvalidBucketName, "bucket provisioning is administrative; use the REST API")
	}
	return b.CreateBucketForProject(name, b.DefaultProjectID)
}

// CreateBucketForProject is the REST-side entry point for bucket creation.
// Validates the name, inserts the s3_buckets row inside a writer tx, and
// creates the on-disk directory. Returns ErrBucketAlreadyExists on conflict.
func (b *Backend) CreateBucketForProject(name string, projectID int64) error {
	if err := validateBucketName(name); err != nil {
		return err
	}
	if projectID <= 0 {
		return errors.New("backend: projectID required")
	}
	ctx := context.Background()
	// Pre-check to surface a clean ErrBucketAlreadyExists rather than a raw
	// UNIQUE-constraint violation.
	var existing int64
	if err := b.DB.Reader.QueryRowContext(ctx,
		`SELECT id FROM s3_buckets WHERE name=? AND deleted_at IS NULL`, name,
	).Scan(&existing); err == nil {
		return gofakes3.ResourceError(gofakes3.ErrBucketAlreadyExists, name)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("backend: precheck: %w", err)
	}
	if err := b.DB.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO s3_buckets(name, project_id) VALUES (?, ?)`, name, projectID,
		)
		return err
	}); err != nil {
		return fmt.Errorf("backend: insert bucket: %w", err)
	}
	if err := os.MkdirAll(b.bucketRoot(name), 0o750); err != nil {
		return fmt.Errorf("backend: mkdir bucket root: %w", err)
	}
	return nil
}

// BucketExists is lock-free by design (D-15 — concurrent with PutObject).
func (b *Backend) BucketExists(name string) (bool, error) {
	_, ok, err := b.findBucketID(context.Background(), name)
	return ok, err
}

// DeleteBucket refuses a non-empty bucket with ErrBucketNotEmpty.
func (b *Backend) DeleteBucket(name string) error {
	ctx := context.Background()
	mu := b.bucketLock(name)
	mu.Lock()
	defer mu.Unlock()
	id, ok, err := b.findBucketID(ctx, name)
	if err != nil {
		return err
	}
	if !ok {
		return gofakes3.BucketNotFound(name)
	}
	// Check emptiness by listing up to 1 object.
	page, err := b.Objects.ListByBucket(ctx, id, "", "", 1)
	if err != nil {
		return fmt.Errorf("backend: empty check: %w", err)
	}
	if len(page.Objects) > 0 {
		return gofakes3.ResourceError(gofakes3.ErrBucketNotEmpty, name)
	}
	if err := b.DB.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM s3_buckets WHERE id=?`, id)
		return err
	}); err != nil {
		return fmt.Errorf("backend: delete bucket row: %w", err)
	}
	// Remove the on-disk directory (best-effort; log but don't surface).
	_ = os.Remove(b.bucketRoot(name))
	return nil
}

// ForceDeleteBucket wipes the bucket + all its objects. Cascades via the
// s3_objects FK ON DELETE CASCADE and rm -rf of the on-disk tree.
func (b *Backend) ForceDeleteBucket(name string) error {
	ctx := context.Background()
	mu := b.bucketLock(name)
	mu.Lock()
	defer mu.Unlock()
	id, ok, err := b.findBucketID(ctx, name)
	if err != nil {
		return err
	}
	if !ok {
		return gofakes3.BucketNotFound(name)
	}
	if err := b.DB.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM s3_buckets WHERE id=?`, id)
		return err
	}); err != nil {
		return fmt.Errorf("backend: force delete: %w", err)
	}
	_ = os.RemoveAll(b.bucketRoot(name))
	return nil
}

// PutObject streams the body via storage.WriteAndRename (atomic temp+rename)
// and upserts the s3_objects row. Mid-stream errors leave NO file at the
// canonical path and NO row in the DB.
func (b *Backend) PutObject(bucketName, key string, meta map[string]string, input io.Reader, size int64, conditions *gofakes3.PutConditions) (gofakes3.PutObjectResult, error) {
	ctx := context.Background()
	if err := validateObjectKey(key); err != nil {
		return gofakes3.PutObjectResult{}, err
	}
	id, ok, err := b.findBucketID(ctx, bucketName)
	if err != nil {
		return gofakes3.PutObjectResult{}, err
	}
	if !ok {
		return gofakes3.PutObjectResult{}, gofakes3.BucketNotFound(bucketName)
	}

	mu := b.bucketLock(bucketName)
	mu.Lock()
	defer mu.Unlock()

	// Conditional write check (If-Match / If-None-Match).
	if conditions != nil {
		existing, err := b.Objects.FindByBucketAndKey(ctx, id, key)
		info := &gofakes3.ConditionalObjectInfo{}
		switch {
		case errors.Is(err, metadata.ErrNotFound):
			info.Exists = false
		case err != nil:
			return gofakes3.PutObjectResult{}, err
		default:
			info.Exists = true
			// existing.ETag in storage may or may not be hex-only; treat as hex md5.
			if h, hexErr := hex.DecodeString(stripQuotes(existing.ETag)); hexErr == nil {
				info.Hash = h
			}
		}
		if err := gofakes3.CheckPutConditions(conditions, info); err != nil {
			return gofakes3.PutObjectResult{}, err
		}
	}

	h256 := sha256.New()
	md := md5.New()
	tee := io.TeeReader(input, io.MultiWriter(h256, md))

	dst := filepath.Join(b.bucketRoot(bucketName), filepath.FromSlash(key))
	written, err := storage.WriteAndRename(ctx, b.tmpRoot(), dst, tee)
	if err != nil {
		return gofakes3.PutObjectResult{}, fmt.Errorf("backend: write: %w", err)
	}

	etag := hex.EncodeToString(md.Sum(nil))
	sha := hex.EncodeToString(h256.Sum(nil))
	metaJSON := marshalMeta(meta)
	contentType := ""
	if meta != nil {
		contentType = meta["Content-Type"]
		if contentType == "" {
			contentType = meta["content-type"]
		}
	}

	if err := b.DB.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := b.Objects.Upsert(ctx, tx, &metadata.S3Object{
			BucketID:     id,
			Key:          key,
			SizeBytes:    written,
			ETag:         etag,
			ContentType:  contentType,
			MetadataJSON: metaJSON,
			SHA256:       sha,
		})
		return err
	}); err != nil {
		// Best-effort: remove the file we just wrote since the row failed.
		_ = os.Remove(dst)
		return gofakes3.PutObjectResult{}, fmt.Errorf("backend: upsert: %w", err)
	}
	_ = size // advisory; written is the source of truth
	return gofakes3.PutObjectResult{}, nil
}

// GetObject returns the object body, respecting an optional range request.
func (b *Backend) GetObject(bucketName, key string, rangeRequest *gofakes3.ObjectRangeRequest) (*gofakes3.Object, error) {
	ctx := context.Background()
	id, ok, err := b.findBucketID(ctx, bucketName)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, gofakes3.BucketNotFound(bucketName)
	}
	row, err := b.Objects.FindByBucketAndKey(ctx, id, key)
	if err != nil {
		if errors.Is(err, metadata.ErrNotFound) {
			return nil, gofakes3.KeyNotFound(key)
		}
		return nil, err
	}
	path := filepath.Join(b.bucketRoot(bucketName), filepath.FromSlash(key))
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("backend: open object: %w", err)
	}
	// Compute effective range.
	rng, err := rangeRequest.Range(row.SizeBytes)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	var body io.ReadCloser = f
	if rng != nil {
		if _, err := f.Seek(rng.Start, io.SeekStart); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("backend: seek: %w", err)
		}
		body = &limitedCloser{R: io.LimitReader(f, rng.Length), C: f}
	}
	hash, _ := hex.DecodeString(row.ETag)
	return &gofakes3.Object{
		Name:     key,
		Metadata: unmarshalMeta(row.MetadataJSON, row.ContentType),
		Size:     row.SizeBytes,
		Contents: body,
		Hash:     hash,
		Range:    rng,
	}, nil
}

// HeadObject returns the same metadata as GetObject without opening the body.
func (b *Backend) HeadObject(bucketName, key string) (*gofakes3.Object, error) {
	ctx := context.Background()
	id, ok, err := b.findBucketID(ctx, bucketName)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, gofakes3.BucketNotFound(bucketName)
	}
	row, err := b.Objects.FindByBucketAndKey(ctx, id, key)
	if err != nil {
		if errors.Is(err, metadata.ErrNotFound) {
			return nil, gofakes3.KeyNotFound(key)
		}
		return nil, err
	}
	hash, _ := hex.DecodeString(row.ETag)
	return &gofakes3.Object{
		Name:     key,
		Metadata: unmarshalMeta(row.MetadataJSON, row.ContentType),
		Size:     row.SizeBytes,
		Contents: io.NopCloser(bytes.NewReader(nil)),
		Hash:     hash,
	}, nil
}

// DeleteObject removes the row and the on-disk file. Idempotent — absence
// is not an error (S3 semantics).
func (b *Backend) DeleteObject(bucketName, key string) (gofakes3.ObjectDeleteResult, error) {
	ctx := context.Background()
	id, ok, err := b.findBucketID(ctx, bucketName)
	if err != nil {
		return gofakes3.ObjectDeleteResult{}, err
	}
	if !ok {
		return gofakes3.ObjectDeleteResult{}, gofakes3.BucketNotFound(bucketName)
	}
	mu := b.bucketLock(bucketName)
	mu.Lock()
	defer mu.Unlock()
	if err := b.DB.WriteTx(ctx, func(tx *sql.Tx) error {
		return b.Objects.Delete(ctx, tx, id, key)
	}); err != nil {
		return gofakes3.ObjectDeleteResult{}, err
	}
	path := filepath.Join(b.bucketRoot(bucketName), filepath.FromSlash(key))
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return gofakes3.ObjectDeleteResult{}, fmt.Errorf("backend: remove file: %w", err)
	}
	return gofakes3.ObjectDeleteResult{}, nil
}

// DeleteMulti iterates DeleteObject, accumulating per-key errors into the
// MultiDeleteResult. A single error does not short-circuit the batch.
func (b *Backend) DeleteMulti(bucketName string, objects ...string) (gofakes3.MultiDeleteResult, error) {
	res := gofakes3.MultiDeleteResult{}
	for _, key := range objects {
		if _, err := b.DeleteObject(bucketName, key); err != nil {
			res.Error = append(res.Error, gofakes3.ErrorResult{
				Key:     key,
				Code:    gofakes3.ErrInternal,
				Message: err.Error(),
			})
			continue
		}
		res.Deleted = append(res.Deleted, gofakes3.ObjectID{Key: key})
	}
	return res, nil
}

// CopyObject is implemented via the stock Get+Put helper.
func (b *Backend) CopyObject(srcBucket, srcKey, dstBucket, dstKey string, meta map[string]string) (gofakes3.CopyObjectResult, error) {
	return gofakes3.CopyObject(b, srcBucket, srcKey, dstBucket, dstKey, meta)
}

// -- helpers --------------------------------------------------------------

type limitedCloser struct {
	R io.Reader
	C io.Closer
}

func (l *limitedCloser) Read(p []byte) (int, error) { return l.R.Read(p) }
func (l *limitedCloser) Close() error               { return l.C.Close() }

func marshalMeta(m map[string]string) string {
	if len(m) == 0 {
		return "{}"
	}
	buf, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(buf)
}

func unmarshalMeta(raw, contentType string) map[string]string {
	m := map[string]string{}
	if raw != "" && raw != "{}" {
		_ = json.Unmarshal([]byte(raw), &m)
	}
	if contentType != "" {
		if _, ok := m["Content-Type"]; !ok {
			m["Content-Type"] = contentType
		}
	}
	return m
}

func stripQuotes(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

// newContentTime exposes time.Time → gofakes3.ContentTime without pulling
// callers into the messages package.
func newContentTime(t time.Time) gofakes3.ContentTime { return gofakes3.NewContentTime(t) }
