package metadata

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Repo mirrors the repos table.
type Repo struct {
	ID              int64
	ProjectID       int64
	Type            string // rpm|deb|pypi|docker|helm|git|raw
	Name            string
	DescriptionMD   string
	AutoScan        bool
	BlockOnSeverity string // none|low|medium|high|critical
	PublicRead      bool
	SizeBytes       int64
	CreatedAt       time.Time
	DeletedAt       *time.Time
}

// ReposRepo owns CRUD on repos.
//
// The table DDL (001_initial.up.sql) enforces:
//   - CHECK(type IN ('rpm','deb','pypi','docker','helm','git','raw')) — REPO-02
//   - CHECK(block_on_severity IN ('none','low','medium','high','critical'))
//   - UNIQUE(project_id, type, name) — REPO-01
//
// The app layer (bootstrap validator V13, api handlers) validates the same
// set before INSERT so bad input surfaces a typed Go error rather than the
// raw CHECK-constraint string.
type ReposRepo struct{ db *DB }

// NewReposRepo constructs a repo bound to db.
func NewReposRepo(db *DB) *ReposRepo { return &ReposRepo{db: db} }

// Create inserts a repo row and returns the generated id. Nil/empty optional
// arguments fall back to schema defaults.
func (r *ReposRepo) Create(
	ctx context.Context,
	projectID int64,
	typ, name, descriptionMD string,
	autoScan *bool,
	blockOnSeverity *string,
	publicRead *bool,
) (int64, error) {
	var id int64
	err := r.db.WriteTx(ctx, func(tx *sql.Tx) error {
		// Assemble an insert that lets schema defaults apply when the pointer is nil.
		as := int64(1)
		if autoScan != nil {
			as = boolInt(*autoScan)
		}
		bos := "none"
		if blockOnSeverity != nil && *blockOnSeverity != "" {
			bos = *blockOnSeverity
		}
		pr := int64(0)
		if publicRead != nil {
			pr = boolInt(*publicRead)
		}
		res, execErr := tx.ExecContext(ctx, `
			INSERT INTO repos(project_id, type, name, description_md, auto_scan, block_on_severity, public_read)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, projectID, typ, name, descriptionMD, as, bos, pr)
		if execErr != nil {
			return fmt.Errorf("repos: create (project=%d type=%s name=%s): %w", projectID, typ, name, execErr)
		}
		lid, lidErr := res.LastInsertId()
		if lidErr != nil {
			return fmt.Errorf("repos: last insert id: %w", lidErr)
		}
		id = lid
		return nil
	})
	return id, err
}

// FindByTriple returns the live repo with matching (projectID, type, name).
func (r *ReposRepo) FindByTriple(ctx context.Context, projectID int64, typ, name string) (*Repo, error) {
	return r.scanOne(ctx, `project_id=? AND type=? AND name=? AND deleted_at IS NULL`, projectID, typ, name)
}

// FindByID returns the live repo by id.
func (r *ReposRepo) FindByID(ctx context.Context, id int64) (*Repo, error) {
	return r.scanOne(ctx, `id=? AND deleted_at IS NULL`, id)
}

// SoftDelete stamps deleted_at.
func (r *ReposRepo) SoftDelete(ctx context.Context, id int64) error {
	return r.db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE repos SET deleted_at=CURRENT_TIMESTAMP WHERE id=?`, id)
		if err != nil {
			return fmt.Errorf("repos: soft delete %d: %w", id, err)
		}
		return nil
	})
}

// ListByProject returns every live repo belonging to projectID.
func (r *ReposRepo) ListByProject(ctx context.Context, projectID int64) ([]Repo, error) {
	rows, err := r.db.Reader.QueryContext(ctx, `
		SELECT id, project_id, type, name, description_md, auto_scan, block_on_severity,
		       public_read, size_bytes, created_at, deleted_at
		FROM repos WHERE project_id=? AND deleted_at IS NULL
		ORDER BY type, name
	`, projectID)
	if err != nil {
		return nil, fmt.Errorf("repos: list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Repo
	for rows.Next() {
		rr, err := scanRepo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *rr)
	}
	return out, rows.Err()
}

// ListAll returns every live repo across every project.
func (r *ReposRepo) ListAll(ctx context.Context) ([]Repo, error) {
	rows, err := r.db.Reader.QueryContext(ctx, `
		SELECT id, project_id, type, name, description_md, auto_scan, block_on_severity,
		       public_read, size_bytes, created_at, deleted_at
		FROM repos WHERE deleted_at IS NULL ORDER BY project_id, type, name
	`)
	if err != nil {
		return nil, fmt.Errorf("repos: list all: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Repo
	for rows.Next() {
		rr, err := scanRepo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *rr)
	}
	return out, rows.Err()
}

func (r *ReposRepo) scanOne(ctx context.Context, where string, args ...any) (*Repo, error) {
	row := r.db.Reader.QueryRowContext(ctx, `
		SELECT id, project_id, type, name, description_md, auto_scan, block_on_severity,
		       public_read, size_bytes, created_at, deleted_at
		FROM repos WHERE `+where, args...)
	rr, err := scanRepoRow(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("repos: scan: %w", err)
	}
	return rr, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanRepo(rs scanner) (*Repo, error) {
	return scanRepoRow(rs)
}

// UpdateFields carries optional edits for ReposRepo.Update (REPO-05, D-34).
// Nil pointers leave the corresponding column untouched; non-nil values are
// applied verbatim. Callers are responsible for validating enum values (e.g.
// block_on_severity) BEFORE invoking Update — the DDL CHECK constraint is the
// last line of defense.
type UpdateFields struct {
	DescriptionMD   *string
	AutoScan        *bool
	BlockOnSeverity *string
	PublicRead      *bool
}

// Update applies a partial field update to the repo identified by repoID.
// Runs inside the caller-supplied *sql.Tx so the UPDATE rides in the same
// writer transaction as the audit row emission. Returns the updated row.
//
// When every field pointer in f is nil, Update is a no-op and returns the
// current row via a tx-scoped read.
//
// Returns ErrNotFound if the repo id does not exist (including when it was
// soft-deleted) so handlers can distinguish 404 from 500.
func (r *ReposRepo) Update(ctx context.Context, tx *sql.Tx, repoID int64, f UpdateFields) (Repo, error) {
	sets := make([]string, 0, 4)
	args := make([]any, 0, 5)
	if f.DescriptionMD != nil {
		sets = append(sets, "description_md = ?")
		args = append(args, *f.DescriptionMD)
	}
	if f.AutoScan != nil {
		sets = append(sets, "auto_scan = ?")
		args = append(args, boolInt(*f.AutoScan))
	}
	if f.BlockOnSeverity != nil {
		sets = append(sets, "block_on_severity = ?")
		args = append(args, *f.BlockOnSeverity)
	}
	if f.PublicRead != nil {
		sets = append(sets, "public_read = ?")
		args = append(args, boolInt(*f.PublicRead))
	}
	if len(sets) > 0 {
		args = append(args, repoID)
		q := "UPDATE repos SET " + strings.Join(sets, ", ") + " WHERE id = ? AND deleted_at IS NULL"
		res, err := tx.ExecContext(ctx, q, args...)
		if err != nil {
			return Repo{}, fmt.Errorf("repos: update %d: %w", repoID, err)
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return Repo{}, ErrNotFound
		}
	}
	// Read-back via the same tx so the caller sees its own write before commit.
	row := tx.QueryRowContext(ctx, `
		SELECT id, project_id, type, name, description_md, auto_scan, block_on_severity,
		       public_read, size_bytes, created_at, deleted_at
		FROM repos WHERE id = ? AND deleted_at IS NULL
	`, repoID)
	rr, err := scanRepoRow(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Repo{}, ErrNotFound
		}
		return Repo{}, fmt.Errorf("repos: update read-back %d: %w", repoID, err)
	}
	return *rr, nil
}

// WipeDocker deletes every docker_tags + docker_manifests row for repoID and
// decrements per-blob ref_count for every previously-referenced digest.
// Returns (artifactCount, bytesFreed) where artifactCount is the number of
// manifests removed and bytesFreed is the sum of sizes of blobs whose
// ref_count dropped to zero in this call (i.e., blobs now eligible for GC).
//
// CAS files are NEVER touched here (Pitfall 8): blobs may be shared with
// other repos; GC is the only code path allowed to delete CAS bytes.
// artifacts_fts entries for each manifest digest are removed in the same tx.
func (r *ReposRepo) WipeDocker(ctx context.Context, tx *sql.Tx, repoID int64) (int64, int64, error) {
	// 1) Collect every blob digest referenced by a manifest in this repo
	//    (referenced = config digest + all layer digests encoded in body). In
	//    Phase 02, the single pattern of ref-tracking is:
	//    docker_blobs.ref_count was IncRef'd by the /v2 PUT handler for each
	//    distinct digest pulled into the repo. We can't recover the exact
	//    IncRef set from the manifest body without re-parsing JSON, so we
	//    rely on a helper view: every blob whose ref_count is > 0 AND which
	//    is referenced by at least one manifest in this repo.
	//
	//    The stable approach is: parse each manifest body and extract every
	//    "digest" string it carries. JSON parsing here is tolerant (no
	//    schema assumption) — we take every digest field at any depth.
	rows, err := tx.QueryContext(ctx, `
		SELECT digest, body, size_bytes FROM docker_manifests WHERE repo_id = ?
	`, repoID)
	if err != nil {
		return 0, 0, fmt.Errorf("repos: wipe docker list manifests (%d): %w", repoID, err)
	}
	type manifestRow struct {
		digest string
		body   []byte
		size   int64
	}
	var manifests []manifestRow
	for rows.Next() {
		var mr manifestRow
		if err := rows.Scan(&mr.digest, &mr.body, &mr.size); err != nil {
			_ = rows.Close()
			return 0, 0, fmt.Errorf("repos: wipe docker scan: %w", err)
		}
		manifests = append(manifests, mr)
	}
	if err := rows.Close(); err != nil {
		return 0, 0, err
	}
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}

	// 2) Collect all blob digests referenced by these manifests.
	refs := make(map[string]struct{})
	for _, mr := range manifests {
		for _, d := range extractDigests(mr.body) {
			refs[d] = struct{}{}
		}
	}

	// 3) DELETE tags + manifests for this repo. FTS5 artifact index cleared
	//    per manifest digest.
	if _, err := tx.ExecContext(ctx, `DELETE FROM docker_tags WHERE repo_id = ?`, repoID); err != nil {
		return 0, 0, fmt.Errorf("repos: wipe docker tags (%d): %w", repoID, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM docker_manifests WHERE repo_id = ?`, repoID); err != nil {
		return 0, 0, fmt.Errorf("repos: wipe docker manifests (%d): %w", repoID, err)
	}
	for _, mr := range manifests {
		if err := IndexArtifactDelete(ctx, tx, repoID, mr.digest); err != nil {
			return 0, 0, err
		}
	}

	// 4) DecRef each referenced blob and compute bytes_freed from blobs that
	//    dropped to ref_count == 0 as a result. Blobs no longer referenced
	//    here may still be referenced elsewhere; only those at 0 count as
	//    freed. We DecRef via the row-aware UPDATE pattern and re-stat to
	//    observe the resulting ref_count in the same tx.
	var bytesFreed int64
	for digest := range refs {
		res, err := tx.ExecContext(ctx, `
			UPDATE docker_blobs
			SET ref_count = ref_count - 1, last_touched_at = CURRENT_TIMESTAMP
			WHERE digest = ? AND ref_count > 0
		`, digest)
		if err != nil {
			return 0, 0, fmt.Errorf("repos: wipe docker decref %s: %w", digest, err)
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			// Either the blob row was missing or ref_count was already 0.
			// Neither case is fatal — a corrupt refcount is better left
			// untouched; GC will reconcile via its own sweep.
			continue
		}
		// Re-stat to see if we dropped to 0.
		var (
			rc   int64
			sz   int64
			gotD string
		)
		err = tx.QueryRowContext(ctx,
			`SELECT digest, size_bytes, ref_count FROM docker_blobs WHERE digest = ?`, digest,
		).Scan(&gotD, &sz, &rc)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return 0, 0, fmt.Errorf("repos: wipe docker restat %s: %w", digest, err)
		}
		if rc == 0 {
			bytesFreed += sz
		}
	}

	return int64(len(manifests)), bytesFreed, nil
}

// WipeRaw deletes every raw_files row for repoID and returns
// (fileCount, bytesFreed). FTS5 artifact index entries for each file path are
// removed in the same tx.
func (r *ReposRepo) WipeRaw(ctx context.Context, tx *sql.Tx, repoID int64) (int64, int64, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT path, size_bytes FROM raw_files WHERE repo_id = ?
	`, repoID)
	if err != nil {
		return 0, 0, fmt.Errorf("repos: wipe raw list (%d): %w", repoID, err)
	}
	type rawRow struct {
		path string
		size int64
	}
	var files []rawRow
	for rows.Next() {
		var rr rawRow
		if err := rows.Scan(&rr.path, &rr.size); err != nil {
			_ = rows.Close()
			return 0, 0, fmt.Errorf("repos: wipe raw scan: %w", err)
		}
		files = append(files, rr)
	}
	if err := rows.Close(); err != nil {
		return 0, 0, err
	}
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}

	var bytesFreed int64
	for _, f := range files {
		bytesFreed += f.size
		// artifacts_fts index for RAW uses the path as the digest key
		// (02-08 decision); mirror that here.
		if err := IndexArtifactDelete(ctx, tx, repoID, f.path); err != nil {
			return 0, 0, err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM raw_files WHERE repo_id = ?`, repoID); err != nil {
		return 0, 0, fmt.Errorf("repos: wipe raw delete (%d): %w", repoID, err)
	}
	return int64(len(files)), bytesFreed, nil
}

// extractDigests walks a manifest body and returns every "sha256:..." string
// value appearing under a "digest" key. Tolerant to manifest-list / image-
// index / single-manifest shapes because it traverses any JSON structure.
// Bodies that fail to parse yield an empty slice — wipe treats the manifest
// as referencing no blobs in that case (safer than erroring, because the row
// still needs to be removed and the GC sweep will reclaim unreferenced
// blobs).
func extractDigests(body []byte) []string {
	var raw any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil
	}
	seen := make(map[string]struct{})
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			if d, ok := t["digest"].(string); ok && strings.HasPrefix(d, "sha256:") {
				seen[d] = struct{}{}
			}
			for _, child := range t {
				walk(child)
			}
		case []any:
			for _, child := range t {
				walk(child)
			}
		}
	}
	walk(raw)
	out := make([]string, 0, len(seen))
	for d := range seen {
		out = append(out, d)
	}
	return out
}

func scanRepoRow(rs scanner) (*Repo, error) {
	var r Repo
	var as, pr int64
	var deleted sql.NullTime
	if err := rs.Scan(&r.ID, &r.ProjectID, &r.Type, &r.Name, &r.DescriptionMD, &as, &r.BlockOnSeverity, &pr, &r.SizeBytes, &r.CreatedAt, &deleted); err != nil {
		return nil, err
	}
	r.AutoScan = as != 0
	r.PublicRead = pr != 0
	if deleted.Valid {
		t := deleted.Time
		r.DeletedAt = &t
	}
	return &r, nil
}
