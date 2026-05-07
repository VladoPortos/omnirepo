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
	// GitMaxPushBytes (Phase 4 Plan 02, D-35) — per-repo override for the
	// global git push-size cap. NULL → inherit cfg.repos.git.max_push_bytes.
	// Only meaningful when Type == "git"; populated by migration 017.
	GitMaxPushBytes *int64

	// Phase 8 Plan 01 (MIRROR-01..07) — upstream mirror fields added by
	// migration 024. Only meaningful when Type ∈ {deb,rpm,pypi,helm};
	// IsMirror + MirrorUpstreamURL are immutable post-creation (enforced
	// at the API layer). MirrorCredID may be NULL even for mirror repos
	// (public upstream archives); set via ON DELETE SET NULL when the
	// referenced upstream_creds row is removed — the next sync surfaces
	// "credential missing" rather than wedging the repo row.
	IsMirror          bool
	MirrorUpstreamURL string
	MirrorFilterJSON  string
	MirrorCredID      *int64
	ScanOnSync        bool

	// DriftPurge (v1.5 Phase 6 / DRIFTPURGE-04, D-17): opt-in per-mirror
	// flag — when true, a successful mirror sync soft-deletes local rows
	// whose upstream key vanished. Default false on upgrade (migration 035)
	// to preserve v1.4 additive-only behaviour. Editable via PATCH;
	// mirror-only invariant enforced in handlePatchRepo.
	DriftPurge bool
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
//
// Phase 01 Plan 01-03 (LIFECYCLE-09): SoftDelete + Restore now drive the FTS5
// prune and reindex steps in the same WriteTx as the row UPDATE. The optional
// reindexer (set via WithReindexer) drives Restore; nil-reindexer is the
// test-time default (no reindex side-effect).
type ReposRepo struct {
	db        *DB
	reindexer *FTSReindexer
}

// NewReposRepo constructs a repo bound to db.
func NewReposRepo(db *DB) *ReposRepo { return &ReposRepo{db: db} }

// WithReindexer wires an FTSReindexer used by Repos.Restore (and indirectly by
// Projects.Restore via cascade). Returns the receiver for compact builder
// chaining at bootstrap. Tests that don't exercise Restore can leave the
// reindexer unset — Restore becomes a row-only UPDATE in that case.
func (r *ReposRepo) WithReindexer(rx *FTSReindexer) *ReposRepo {
	r.reindexer = rx
	return r
}

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
		var insertErr error
		id, insertErr = r.CreateInTx(ctx, tx, projectID, typ, name, descriptionMD, autoScan, blockOnSeverity, publicRead)
		return insertErr
	})
	return id, err
}

// CreateInTx is the tx-aware sibling of Create. Lets callers atomically
// commit related rows alongside the repo row (Phase 03 Plan 04: rpm/deb
// signing-key generation in the same writer tx as the repos INSERT, D-02).
func (r *ReposRepo) CreateInTx(
	ctx context.Context,
	tx *sql.Tx,
	projectID int64,
	typ, name, descriptionMD string,
	autoScan *bool,
	blockOnSeverity *string,
	publicRead *bool,
) (int64, error) {
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
		return 0, fmt.Errorf("repos: create (project=%d type=%s name=%s): %w", projectID, typ, name, execErr)
	}
	lid, lidErr := res.LastInsertId()
	if lidErr != nil {
		return 0, fmt.Errorf("repos: last insert id: %w", lidErr)
	}
	return lid, nil
}

// MirrorConfig is the tuple of mirror-repo columns set exactly once at repo
// creation (Phase 8 Plan 01, D-02). IsMirror + UpstreamURL are immutable
// post-creation; FilterJSON + CredID + ScanOnSync can be edited via
// ReposRepo.Update. CredID may be nil (public upstream archives).
type MirrorConfig struct {
	IsMirror    bool
	UpstreamURL string
	FilterJSON  string
	CredID      *int64
	ScanOnSync  bool
}

// SetMirrorConfigInTx flips the 5 mirror columns on repoID. Intended to be
// called in the same writer-tx as CreateInTx so the repo row is never
// observable in a half-mirror state. Called again via a caller-supplied
// UPDATE would violate the immutability rule; handlers that patch mirror
// fields go through ReposRepo.Update with UpdateFields instead.
func (r *ReposRepo) SetMirrorConfigInTx(ctx context.Context, tx *sql.Tx, repoID int64, c MirrorConfig) error {
	var credID sql.NullInt64
	if c.CredID != nil {
		credID = sql.NullInt64{Int64: *c.CredID, Valid: true}
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE repos SET
		  is_mirror = ?,
		  mirror_upstream_url = ?,
		  mirror_filter_json = ?,
		  mirror_cred_id = ?,
		  scan_on_sync = ?
		WHERE id = ?
	`, boolInt(c.IsMirror), c.UpstreamURL, c.FilterJSON, credID, boolInt(c.ScanOnSync), repoID)
	if err != nil {
		return fmt.Errorf("repos: set mirror config (repo=%d): %w", repoID, err)
	}
	return nil
}

// FindByTriple returns the live repo with matching (projectID, type, name).
func (r *ReposRepo) FindByTriple(ctx context.Context, projectID int64, typ, name string) (*Repo, error) {
	return r.scanOne(ctx, `project_id=? AND type=? AND name=? AND deleted_at IS NULL`, projectID, typ, name)
}

// FindByID returns the live repo by id.
func (r *ReposRepo) FindByID(ctx context.Context, id int64) (*Repo, error) {
	return r.scanOne(ctx, `id=? AND deleted_at IS NULL`, id)
}

// SoftDelete stamps deleted_at and prunes every per-repo FTS5 row in the same
// WriteTx (LIFECYCLE-09). The `WHERE deleted_at IS NULL` filter on the UPDATE
// makes the operation idempotent — a second SoftDelete is a no-op cascade
// (PruneRepoFTS itself is idempotent because its DELETEs match zero rows).
func (r *ReposRepo) SoftDelete(ctx context.Context, id int64) error {
	return r.db.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`UPDATE repos SET deleted_at=CURRENT_TIMESTAMP WHERE id=? AND deleted_at IS NULL`, id,
		); err != nil {
			return fmt.Errorf("repos: soft delete %d: %w", id, err)
		}
		return PruneRepoFTS(ctx, tx, id)
	})
}

// SoftDeleteRepoForProjectCascade is the project-cascade variant of SoftDelete:
// stamps repos.deleted_at = cascadeTS (instead of CURRENT_TIMESTAMP) so
// Projects.Restore can identify which repos to reindex via timestamp-equality
// (D-14). Idempotent — already soft-deleted repos are skipped via the
// `deleted_at IS NULL` filter.
//
// Lives at package level (not as a method on ReposRepo) so the project cascade
// closure in projects.go can call it from inside its WriteTx without holding a
// ReposRepo reference; mirrors the SoftDeleteAllBucketsForProject pattern.
func SoftDeleteRepoForProjectCascade(ctx context.Context, tx *sql.Tx, repoID int64, cascadeTS string) error {
	if _, err := tx.ExecContext(ctx,
		`UPDATE repos SET deleted_at=? WHERE id=? AND deleted_at IS NULL`, cascadeTS, repoID,
	); err != nil {
		return fmt.Errorf("repos: cascade soft-delete %d: %w", repoID, err)
	}
	return PruneRepoFTS(ctx, tx, repoID)
}

// Restore clears the soft-delete timestamp, making the repo live again. When
// a reindexer is wired (production path), it re-derives FTS5 rows from the
// canonical base tables in the same WriteTx — base tables are untouched by
// SoftDelete so the reindex is loss-free.
func (r *ReposRepo) Restore(ctx context.Context, id int64) error {
	return r.db.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE repos SET deleted_at=NULL WHERE id=?`, id); err != nil {
			return fmt.Errorf("repos: restore %d: %w", id, err)
		}
		if r.reindexer != nil {
			if err := r.reindexer.ReindexRepo(ctx, tx, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// ListByProject returns every live repo belonging to projectID.
func (r *ReposRepo) ListByProject(ctx context.Context, projectID int64) ([]Repo, error) {
	rows, err := r.db.Reader.QueryContext(ctx, `
		SELECT id, project_id, type, name, description_md, auto_scan, block_on_severity,
		       public_read, size_bytes, created_at, deleted_at, git_max_push_bytes,
		       is_mirror, mirror_upstream_url, mirror_filter_json, mirror_cred_id, scan_on_sync,
		       drift_purge
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

// CatalogScope drives ListDockerCatalog's filter selection.
type CatalogScope struct {
	// SuperAdmin: include every docker repo across every project.
	SuperAdmin bool
	// UserProjectIDs (optional): only repos in these project ids are included.
	// nil/empty AND SuperAdmin=false AND Anonymous=false yields zero rows.
	UserProjectIDs []int64
	// Anonymous: include only repos with public_read=true.
	Anonymous bool
}

// ListDockerCatalog returns "<project>/docker/<repo>" strings for the OCI
// /v2/_catalog endpoint (D-07), cursor-paginated lexicographically (strictly
// > after) with limit clamped to [1,1000]. Scoping:
//
//   - scope.SuperAdmin → every docker repo
//   - scope.Anonymous → only repos with public_read=true
//   - otherwise → docker repos whose project_id ∈ scope.UserProjectIDs
//     AND (repo.public_read=true OR member). Since members see all their
//     project's repos regardless of public_read, the query simply filters
//     by project_id ∈ UserProjectIDs OR public_read=true.
//
// Returns a sorted slice — empty (not nil) when no rows match.
func (r *ReposRepo) ListDockerCatalog(ctx context.Context, scope CatalogScope, limit int, after string) ([]string, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}

	// Build the query based on scope. Every branch selects
	// (p.name || '/docker/' || r.name) as the ordered key. SQLite does not
	// allow SELECT aliases in WHERE, so we inline the expression in both
	// clauses.
	const pathExpr = `(p.name || '/docker/' || r.name)`
	switch {
	case scope.SuperAdmin:
		return r.catalogQuery(ctx, `
			SELECT `+pathExpr+` AS path
			FROM repos r
			JOIN projects p ON p.id = r.project_id
			WHERE r.type = 'docker' AND r.deleted_at IS NULL AND p.deleted_at IS NULL
			  AND `+pathExpr+` > ?
			ORDER BY path ASC
			LIMIT ?
		`, after, limit)

	case scope.Anonymous:
		return r.catalogQuery(ctx, `
			SELECT `+pathExpr+` AS path
			FROM repos r
			JOIN projects p ON p.id = r.project_id
			WHERE r.type = 'docker' AND r.deleted_at IS NULL AND p.deleted_at IS NULL
			  AND r.public_read = 1
			  AND `+pathExpr+` > ?
			ORDER BY path ASC
			LIMIT ?
		`, after, limit)

	default:
		// Authenticated non-super-admin: project membership ∪ public_read.
		if len(scope.UserProjectIDs) == 0 {
			// No memberships → only public repos visible.
			return r.catalogQuery(ctx, `
				SELECT `+pathExpr+` AS path
				FROM repos r
				JOIN projects p ON p.id = r.project_id
				WHERE r.type = 'docker' AND r.deleted_at IS NULL AND p.deleted_at IS NULL
				  AND r.public_read = 1
				  AND `+pathExpr+` > ?
				ORDER BY path ASC
				LIMIT ?
			`, after, limit)
		}
		// Build an IN(?,?,...) placeholder list.
		placeholders := make([]string, len(scope.UserProjectIDs))
		args := make([]any, 0, len(scope.UserProjectIDs)+2)
		for i, id := range scope.UserProjectIDs {
			placeholders[i] = "?"
			args = append(args, id)
		}
		args = append(args, after, limit)
		q := `
			SELECT ` + pathExpr + ` AS path
			FROM repos r
			JOIN projects p ON p.id = r.project_id
			WHERE r.type = 'docker' AND r.deleted_at IS NULL AND p.deleted_at IS NULL
			  AND (r.project_id IN (` + strings.Join(placeholders, ",") + `) OR r.public_read = 1)
			  AND ` + pathExpr + ` > ?
			ORDER BY path ASC
			LIMIT ?
		`
		return r.catalogQuery(ctx, q, args...)
	}
}

func (r *ReposRepo) catalogQuery(ctx context.Context, q string, args ...any) ([]string, error) {
	rows, err := r.db.Reader.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("repos: catalog query: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]string, 0)
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, fmt.Errorf("repos: catalog scan: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *ReposRepo) scanOne(ctx context.Context, where string, args ...any) (*Repo, error) {
	row := r.db.Reader.QueryRowContext(ctx, `
		SELECT id, project_id, type, name, description_md, auto_scan, block_on_severity,
		       public_read, size_bytes, created_at, deleted_at, git_max_push_bytes,
		       is_mirror, mirror_upstream_url, mirror_filter_json, mirror_cred_id, scan_on_sync,
		       drift_purge
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
//
// Phase 8 Plan 01 (D-02): MirrorFilterJSON / MirrorCredID / ScanOnSync are the
// three editable mirror fields — is_mirror + mirror_upstream_url are
// intentionally absent from UpdateFields so the schema cannot be mutated post-
// creation via this path; the API-layer PATCH validator enforces the same
// rule at the request boundary.
type UpdateFields struct {
	DescriptionMD   *string
	AutoScan        *bool
	BlockOnSeverity *string
	PublicRead      *bool

	MirrorFilterJSON *string
	MirrorCredID     *int64 // pointer-of-pointer not needed; nil means "no change", non-nil with *val == 0 means "clear"
	MirrorCredIDSet  bool   // distinguishes "no change" from "clear to NULL"
	ScanOnSync       *bool

	// DriftPurge (v1.5 Phase 6 / DRIFTPURGE-04, D-17): nil = no change.
	// Non-nil sets the per-repo drift_purge flag. The mirror-only invariant
	// (drift_purge=true requires IsMirror=true) is enforced one layer up in
	// handlePatchRepo, NOT here — direct callers must pre-validate.
	DriftPurge *bool
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
	if f.MirrorFilterJSON != nil {
		sets = append(sets, "mirror_filter_json = ?")
		args = append(args, *f.MirrorFilterJSON)
	}
	if f.MirrorCredIDSet {
		sets = append(sets, "mirror_cred_id = ?")
		if f.MirrorCredID != nil {
			args = append(args, sql.NullInt64{Int64: *f.MirrorCredID, Valid: true})
		} else {
			args = append(args, sql.NullInt64{})
		}
	}
	if f.ScanOnSync != nil {
		sets = append(sets, "scan_on_sync = ?")
		args = append(args, boolInt(*f.ScanOnSync))
	}
	if f.DriftPurge != nil {
		sets = append(sets, "drift_purge = ?")
		args = append(args, boolInt(*f.DriftPurge))
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
		       public_read, size_bytes, created_at, deleted_at, git_max_push_bytes,
		       is_mirror, mirror_upstream_url, mirror_filter_json, mirror_cred_id, scan_on_sync,
		       drift_purge
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

// Phase 03 Plan 01 (D-12): metadata_state helpers. The regen goroutine
// transitions clean -> dirty on each package write, clean/dirty ->
// regenerating when it starts, and regenerating -> clean (or dirty +
// last_regen_error) on completion.

// Valid values for repos.metadata_state.
const (
	MetadataStateClean        = "clean"
	MetadataStateDirty        = "dirty"
	MetadataStateRegenerating = "regenerating"
)

// SetMetadataState updates the metadata_state column. The DDL CHECK
// constraint is the authority — passing an unknown state yields a DB
// error, not a silent default. Runs inside the caller's tx.
func (r *ReposRepo) SetMetadataState(ctx context.Context, tx *sql.Tx, repoID int64, state string) error {
	if _, err := tx.ExecContext(ctx,
		`UPDATE repos SET metadata_state=? WHERE id=?`, state, repoID,
	); err != nil {
		return fmt.Errorf("repos: set metadata_state (repo=%d state=%s): %w", repoID, state, err)
	}
	return nil
}

// SetLastRegenError updates the last_regen_error column. Empty string
// clears the prior error (call after a successful regen).
func (r *ReposRepo) SetLastRegenError(ctx context.Context, tx *sql.Tx, repoID int64, errStr string) error {
	if _, err := tx.ExecContext(ctx,
		`UPDATE repos SET last_regen_error=? WHERE id=?`, errStr, repoID,
	); err != nil {
		return fmt.Errorf("repos: set last_regen_error (repo=%d): %w", repoID, err)
	}
	return nil
}

// GetMetadataState returns the (state, last_regen_error) pair for repoID.
func (r *ReposRepo) GetMetadataState(ctx context.Context, repoID int64) (state, lastErr string, err error) {
	row := r.db.Reader.QueryRowContext(ctx,
		`SELECT metadata_state, last_regen_error FROM repos WHERE id=?`, repoID,
	)
	if err = row.Scan(&state, &lastErr); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", ErrNotFound
		}
		return "", "", fmt.Errorf("repos: get metadata_state (repo=%d): %w", repoID, err)
	}
	return state, lastErr, nil
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
	var as, pr, isMirror, scanOnSync, driftPurge int64
	var deleted sql.NullTime
	var gitMax, mirrorCredID sql.NullInt64
	var mirrorURL, mirrorFilter sql.NullString
	if err := rs.Scan(
		&r.ID, &r.ProjectID, &r.Type, &r.Name, &r.DescriptionMD,
		&as, &r.BlockOnSeverity, &pr, &r.SizeBytes,
		&r.CreatedAt, &deleted, &gitMax,
		&isMirror, &mirrorURL, &mirrorFilter, &mirrorCredID, &scanOnSync,
		&driftPurge,
	); err != nil {
		return nil, err
	}
	r.AutoScan = as != 0
	r.PublicRead = pr != 0
	if deleted.Valid {
		t := deleted.Time
		r.DeletedAt = &t
	}
	if gitMax.Valid {
		v := gitMax.Int64
		r.GitMaxPushBytes = &v
	}
	r.IsMirror = isMirror != 0
	r.ScanOnSync = scanOnSync != 0
	r.DriftPurge = driftPurge != 0
	if mirrorURL.Valid {
		r.MirrorUpstreamURL = mirrorURL.String
	}
	if mirrorFilter.Valid {
		r.MirrorFilterJSON = mirrorFilter.String
	}
	if mirrorCredID.Valid {
		v := mirrorCredID.Int64
		r.MirrorCredID = &v
	}
	return &r, nil
}
