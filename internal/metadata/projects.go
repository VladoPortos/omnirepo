package metadata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Project mirrors the projects table.
type Project struct {
	ID            int64
	Name          string
	DescriptionMD string
	CreatedAt     time.Time
	DeletedAt     *time.Time
}

// cascadeStepHook is called between cascade steps inside SoftDelete and the
// shared restoreCascadeInTx helper. When non-nil and returning a non-nil
// error, the surrounding WriteTx aborts and rolls back — proving cascade
// atomicity end-to-end.
//
// step values used by SoftDelete:        "after_project_update", "s3_keys", "buckets", "api_keys", "fts"
// step values used by restoreCascadeInTx: "s3_keys", "buckets", "api_keys", "fts"
//
// Production code never sets this; only TestProjectsRepo_SoftDelete_Atomicity
// (and TestProjectsRepo_Restore_Atomicity) install it via the package-private
// setter withCascadeStepHookForTest. The setter is unexported by design — it
// has no API stability guarantees and would be a footgun if exported.
type cascadeStepHook func(step string) error

// ProjectsRepo owns CRUD on projects.
//
// The cascade step chain ends with an "fts" step — SoftDelete cascades the
// per-repo FTS prune via SoftDeleteRepoForProjectCascade for every live repo
// in the project; Restore reverse-cascades by re-indexing only repos whose
// deleted_at exactly matches the project's prior tombstone timestamp
// (timestamp-equality filter).
type ProjectsRepo struct {
	db              *DB
	cascadeStepHook cascadeStepHook // nil in production; set only by internal tests
	reindexer       *FTSReindexer   // nil in tests that don't exercise FTS reindex; set in production wiring
}

// NewProjectsRepo constructs a repo bound to db.
func NewProjectsRepo(db *DB) *ProjectsRepo { return &ProjectsRepo{db: db} }

// WithReindexer wires an FTSReindexer used by the FTS step of the project
// Restore cascade. Returns the receiver for compact builder chaining at
// bootstrap. Tests that don't exercise Restore can leave this unset — the
// FTS step then becomes a row-only un-tombstone.
func (r *ProjectsRepo) WithReindexer(rx *FTSReindexer) *ProjectsRepo {
	r.reindexer = rx
	return r
}

// withCascadeStepHookForTest installs (or clears, when h is nil) a hook fired
// between cascade steps. Lives on ProjectsRepo so internal tests can call it
// from the same package. Returns the receiver to keep test setup compact.
func (r *ProjectsRepo) withCascadeStepHookForTest(h cascadeStepHook) *ProjectsRepo {
	r.cascadeStepHook = h
	return r
}

// Create inserts a project row and returns the generated id. Duplicate names
// surface the UNIQUE-constraint error from SQLite verbatim.
func (r *ProjectsRepo) Create(ctx context.Context, name, descriptionMD string) (int64, error) {
	var id int64
	err := r.db.WriteTx(ctx, func(tx *sql.Tx) error {
		var insErr error
		id, insErr = r.CreateInTx(ctx, tx, name, descriptionMD)
		return insErr
	})
	return id, err
}

// CreateInTx is the tx-scoped form of Create. Callers that need to compose
// the project insert with a follow-up mutation (e.g. adding the creator as a
// member) can do so in a single writer tx so either both rows commit or
// neither does, eliminating orphan projects.
func (r *ProjectsRepo) CreateInTx(ctx context.Context, tx *sql.Tx, name, descriptionMD string) (int64, error) {
	res, execErr := tx.ExecContext(ctx, `
		INSERT INTO projects(name, description_md) VALUES (?, ?)
	`, name, descriptionMD)
	if execErr != nil {
		return 0, fmt.Errorf("projects: create %q: %w", name, execErr)
	}
	lid, lidErr := res.LastInsertId()
	if lidErr != nil {
		return 0, fmt.Errorf("projects: last insert id: %w", lidErr)
	}
	return lid, nil
}

// FindByName returns the live project with matching name. Returns ErrNotFound
// when absent or soft-deleted.
func (r *ProjectsRepo) FindByName(ctx context.Context, name string) (*Project, error) {
	return r.scanOne(ctx, `name=? AND deleted_at IS NULL`, name)
}

// FindByID returns the live project by id.
func (r *ProjectsRepo) FindByID(ctx context.Context, id int64) (*Project, error) {
	return r.scanOne(ctx, `id=? AND deleted_at IS NULL`, id)
}

// SoftDelete soft-deletes the project and cascades to its s3_access_keys,
// s3_buckets, and project-owned api_keys. All four UPDATEs ride a single
// WriteTx — any error rolls back all four. Idempotent: a second SoftDelete is
// a no-op for already-deleted rows.
//
// Cascade timestamp marker: after stamping projects.deleted_at, we read it
// back inside the same tx and pass that exact string to each cascade helper.
// Restore uses the same value to reverse-cascade ONLY rows whose timestamp
// matches (independently revoked rows survive Restore).
//
// modernc/sqlite normalizes TIMESTAMP-affinity columns to ISO-8601 on read,
// but bound parameters in WHERE clauses still match the originally stored
// bytes. Reading projects.deleted_at and reusing that exact string for
// downstream UPDATEs gives us byte-for-byte equality on Restore.
func (r *ProjectsRepo) SoftDelete(ctx context.Context, id int64) error {
	return r.db.WriteTx(ctx, func(tx *sql.Tx) error {
		// 1) Stamp the project row.
		if _, err := tx.ExecContext(ctx,
			`UPDATE projects SET deleted_at=CURRENT_TIMESTAMP WHERE id=? AND deleted_at IS NULL`, id,
		); err != nil {
			return fmt.Errorf("projects: soft delete %d: %w", id, err)
		}
		if r.cascadeStepHook != nil {
			if err := r.cascadeStepHook("after_project_update"); err != nil {
				return fmt.Errorf("projects: cascade hook (after_project_update): %w", err)
			}
		}

		// 2) Read back the project's deleted_at as a TEXT marker for the
		//    cascade. Even when the project was already soft-deleted (the
		//    UPDATE above stamped 0 rows), we still want the existing
		//    timestamp so a second SoftDelete is a no-op cascade (the
		//    children are either already revoked at that timestamp, or
		//    independently revoked and must be left alone).
		var deletedAt sql.NullString
		if err := tx.QueryRowContext(ctx,
			`SELECT deleted_at FROM projects WHERE id=?`, id,
		).Scan(&deletedAt); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("projects: read-back deleted_at %d: %w", id, err)
		}
		if !deletedAt.Valid {
			// Edge case: project row exists but deleted_at NULL after the
			// UPDATE — should never happen because CURRENT_TIMESTAMP can't
			// produce NULL. Surface explicitly so we don't pass an empty
			// cascadeTS into the child helpers (which would clobber every
			// row revoked at the empty string, however unlikely).
			return fmt.Errorf("projects: soft delete %d: deleted_at NULL after UPDATE", id)
		}
		cascadeTS := deletedAt.String

		// 3) Cascade in fixed order: s3_access_keys, s3_buckets, api_keys.
		s3keys := &S3KeysRepo{db: r.db}
		if _, err := s3keys.RevokeAllForProject(ctx, tx, id, cascadeTS); err != nil {
			return err
		}
		if r.cascadeStepHook != nil {
			if err := r.cascadeStepHook("s3_keys"); err != nil {
				return fmt.Errorf("projects: cascade hook (s3_keys): %w", err)
			}
		}
		if _, err := SoftDeleteAllBucketsForProject(ctx, tx, id, cascadeTS); err != nil {
			return err
		}
		if r.cascadeStepHook != nil {
			if err := r.cascadeStepHook("buckets"); err != nil {
				return fmt.Errorf("projects: cascade hook (buckets): %w", err)
			}
		}
		apikeys := &APIKeysRepo{db: r.db}
		if _, err := apikeys.RevokeProjectOwnedForProject(ctx, tx, id, cascadeTS); err != nil {
			return err
		}
		if r.cascadeStepHook != nil {
			if err := r.cascadeStepHook("api_keys"); err != nil {
				return fmt.Errorf("projects: cascade hook (api_keys): %w", err)
			}
		}
		// 4) FTS cascade. For every live repo in this project,
		//    cascade-soft-delete + prune FTS in the same tx. Repos already
		//    independently soft-deleted have a different deleted_at and are
		//    skipped by the `deleted_at IS NULL` filter inside
		//    SoftDeleteRepoForProjectCascade — Restore later only reindexes
		//    repos whose deleted_at exactly equals priorTS.
		liveRepoRows, err := tx.QueryContext(ctx,
			`SELECT id FROM repos WHERE project_id=? AND deleted_at IS NULL`, id)
		if err != nil {
			return fmt.Errorf("projects: cascade list repos %d: %w", id, err)
		}
		var repoIDs []int64
		for liveRepoRows.Next() {
			var rid int64
			if err := liveRepoRows.Scan(&rid); err != nil {
				_ = liveRepoRows.Close()
				return fmt.Errorf("projects: cascade scan repo %d: %w", id, err)
			}
			repoIDs = append(repoIDs, rid)
		}
		if err := liveRepoRows.Close(); err != nil {
			return fmt.Errorf("projects: cascade close repos %d: %w", id, err)
		}
		for _, rid := range repoIDs {
			if err := SoftDeleteRepoForProjectCascade(ctx, tx, rid, cascadeTS); err != nil {
				return err
			}
		}
		if r.cascadeStepHook != nil {
			if err := r.cascadeStepHook("fts"); err != nil {
				return fmt.Errorf("projects: cascade hook (fts): %w", err)
			}
		}
		return nil
	})
}

// Restore clears deleted_at for id and reverse-cascades to its s3_access_keys,
// s3_buckets, and project-owned api_keys via timestamp-equality. Rows that
// were independently revoked / soft-deleted at a different timestamp before
// the cascade are left alone.
func (r *ProjectsRepo) Restore(ctx context.Context, id int64) error {
	return r.db.WriteTx(ctx, func(tx *sql.Tx) error {
		priorTS, err := r.readPriorDeletedAt(ctx, tx, id)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE projects SET deleted_at=NULL WHERE id=?`, id,
		); err != nil {
			return fmt.Errorf("projects: restore %d: %w", id, err)
		}
		if priorTS == "" {
			return nil // already live → nothing to reverse-cascade
		}
		return r.restoreCascadeInTx(ctx, tx, id, priorTS)
	})
}

// readPriorDeletedAt fetches projects.deleted_at as a string for the
// cascade-equality filter. Empty string when the row is already live (the
// caller skips the reverse cascade in that case).
func (r *ProjectsRepo) readPriorDeletedAt(ctx context.Context, tx *sql.Tx, id int64) (string, error) {
	var prior sql.NullString
	if err := tx.QueryRowContext(ctx,
		`SELECT deleted_at FROM projects WHERE id=?`, id,
	).Scan(&prior); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("projects: restore lookup %d: %w", id, err)
	}
	if !prior.Valid {
		return "", nil
	}
	return prior.String, nil
}

// restoreCascadeInTx reverses the cascade for projectID, restoring only rows
// whose tombstone timestamp exactly equals priorTS. Shared by Restore and
// RestoreIfNameFree so the atomicity guarantee holds for both.
func (r *ProjectsRepo) restoreCascadeInTx(ctx context.Context, tx *sql.Tx, projectID int64, priorTS string) error {
	s3keys := &S3KeysRepo{db: r.db}
	if _, err := s3keys.RestoreCascadedForProject(ctx, tx, projectID, priorTS); err != nil {
		return err
	}
	if r.cascadeStepHook != nil {
		if err := r.cascadeStepHook("s3_keys"); err != nil {
			return fmt.Errorf("projects: restore cascade hook (s3_keys): %w", err)
		}
	}
	if _, err := RestoreCascadedBucketsForProject(ctx, tx, projectID, priorTS); err != nil {
		return err
	}
	if r.cascadeStepHook != nil {
		if err := r.cascadeStepHook("buckets"); err != nil {
			return fmt.Errorf("projects: restore cascade hook (buckets): %w", err)
		}
	}
	apikeys := &APIKeysRepo{db: r.db}
	if _, err := apikeys.RestoreCascadedProjectOwnedForProject(ctx, tx, projectID, priorTS); err != nil {
		return err
	}
	if r.cascadeStepHook != nil {
		if err := r.cascadeStepHook("api_keys"); err != nil {
			return fmt.Errorf("projects: restore cascade hook (api_keys): %w", err)
		}
	}
	// FTS reverse-cascade. Reindex only repos whose deleted_at
	// exactly equals priorTS — i.e. repos cascaded by THIS project soft-delete.
	// Independently soft-deleted repos (different timestamp) stay tombstoned.
	restoredRepoRows, err := tx.QueryContext(ctx,
		`SELECT id FROM repos WHERE project_id=? AND deleted_at=?`, projectID, priorTS)
	if err != nil {
		return fmt.Errorf("projects: cascade restore list repos %d: %w", projectID, err)
	}
	var restoredIDs []int64
	for restoredRepoRows.Next() {
		var rid int64
		if err := restoredRepoRows.Scan(&rid); err != nil {
			_ = restoredRepoRows.Close()
			return fmt.Errorf("projects: cascade restore scan repo %d: %w", projectID, err)
		}
		restoredIDs = append(restoredIDs, rid)
	}
	if err := restoredRepoRows.Close(); err != nil {
		return fmt.Errorf("projects: cascade restore close repos %d: %w", projectID, err)
	}
	if len(restoredIDs) > 0 {
		if _, err := tx.ExecContext(ctx,
			`UPDATE repos SET deleted_at=NULL WHERE project_id=? AND deleted_at=?`, projectID, priorTS,
		); err != nil {
			return fmt.Errorf("projects: cascade restore repos %d: %w", projectID, err)
		}
		if r.reindexer != nil {
			for _, rid := range restoredIDs {
				if err := r.reindexer.ReindexRepo(ctx, tx, rid); err != nil {
					return err
				}
			}
		}
	}
	if r.cascadeStepHook != nil {
		if err := r.cascadeStepHook("fts"); err != nil {
			return fmt.Errorf("projects: restore cascade hook (fts): %w", err)
		}
	}
	return nil
}

// ErrNameTaken is returned by RestoreIfNameFree when the project's name
// is already claimed by a live row. Callers map this to HTTP 409.
var ErrNameTaken = errors.New("projects: name already taken by a live project")

// RestoreIfNameFree restores the soft-deleted project by id inside a
// single writer tx, refusing the UPDATE (ErrNameTaken) when the name
// has since been claimed by another live project. Closes the TOCTOU
// window between a pre-check and the UPDATE.
//
// On success, runs the same reverse-cascade as Restore: every row tombstoned
// by THIS project's soft-delete is un-tombstoned via timestamp-equality.
func (r *ProjectsRepo) RestoreIfNameFree(ctx context.Context, id int64) error {
	return r.db.WriteTx(ctx, func(tx *sql.Tx) error {
		var name string
		if err := tx.QueryRowContext(ctx, `SELECT name FROM projects WHERE id=?`, id).Scan(&name); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("projects: restore lookup %d: %w", id, err)
		}
		var n int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM projects WHERE name=? AND deleted_at IS NULL AND id!=?`,
			name, id,
		).Scan(&n); err != nil {
			return fmt.Errorf("projects: restore count live %s: %w", name, err)
		}
		if n > 0 {
			return ErrNameTaken
		}
		// Capture the prior deleted_at BEFORE clearing it — reverse cascade
		// needs the tombstone timestamp to identify cascade-tombstoned rows.
		priorTS, err := r.readPriorDeletedAt(ctx, tx, id)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE projects SET deleted_at=NULL WHERE id=?`, id); err != nil {
			return fmt.Errorf("projects: restore %d: %w", id, err)
		}
		if priorTS == "" {
			return nil
		}
		return r.restoreCascadeInTx(ctx, tx, id, priorTS)
	})
}

// ErrProjectHasRepos is returned by HardDelete when the project still
// has repos (live OR soft-deleted). Purging would otherwise silently
// cascade via ON DELETE CASCADE and take down repo rows, artifact rows,
// and leave on-disk content orphaned. Admins must purge repos explicitly
// before a project can be hard-deleted.
var ErrProjectHasRepos = errors.New("projects: project still has repos")

// HardDelete removes the project row + member links permanently. Refuses
// to proceed when any repo row (live or soft-deleted) still references
// this project: cascading through repos into docker_blobs / rpm_packages
// / raw_files / etc. is out of scope for a UI purge and would leave
// on-disk artifact trees orphaned.
func (r *ProjectsRepo) HardDelete(ctx context.Context, id int64) error {
	return r.db.WriteTx(ctx, func(tx *sql.Tx) error {
		var repoCount int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM repos WHERE project_id=?`, id,
		).Scan(&repoCount); err != nil {
			return fmt.Errorf("projects: hard delete count repos %d: %w", id, err)
		}
		if repoCount > 0 {
			return ErrProjectHasRepos
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM project_members WHERE project_id=?`, id); err != nil {
			return fmt.Errorf("projects: hard delete members %d: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM projects WHERE id=?`, id); err != nil {
			return fmt.Errorf("projects: hard delete %d: %w", id, err)
		}
		return nil
	})
}

// ListDeleted returns every soft-deleted project, newest first. Powers
// the admin Trash page project-restore flow.
func (r *ProjectsRepo) ListDeleted(ctx context.Context) ([]Project, error) {
	rows, err := r.db.Reader.QueryContext(ctx, `
		SELECT id, name, description_md, created_at, deleted_at
		FROM projects WHERE deleted_at IS NOT NULL ORDER BY deleted_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("projects: list deleted: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Project
	for rows.Next() {
		var p Project
		var deleted sql.NullTime
		if err := rows.Scan(&p.ID, &p.Name, &p.DescriptionMD, &p.CreatedAt, &deleted); err != nil {
			return nil, fmt.Errorf("projects: scan: %w", err)
		}
		if deleted.Valid {
			t := deleted.Time
			p.DeletedAt = &t
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListAll returns every live project, ordered by name.
func (r *ProjectsRepo) ListAll(ctx context.Context) ([]Project, error) {
	rows, err := r.db.Reader.QueryContext(ctx, `
		SELECT id, name, description_md, created_at, deleted_at
		FROM projects WHERE deleted_at IS NULL ORDER BY name
	`)
	if err != nil {
		return nil, fmt.Errorf("projects: list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Project
	for rows.Next() {
		var p Project
		var deleted sql.NullTime
		if err := rows.Scan(&p.ID, &p.Name, &p.DescriptionMD, &p.CreatedAt, &deleted); err != nil {
			return nil, fmt.Errorf("projects: scan: %w", err)
		}
		if deleted.Valid {
			t := deleted.Time
			p.DeletedAt = &t
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *ProjectsRepo) scanOne(ctx context.Context, where string, args ...any) (*Project, error) {
	row := r.db.Reader.QueryRowContext(ctx, `
		SELECT id, name, description_md, created_at, deleted_at
		FROM projects WHERE `+where, args...)
	var p Project
	var deleted sql.NullTime
	if err := row.Scan(&p.ID, &p.Name, &p.DescriptionMD, &p.CreatedAt, &deleted); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("projects: scan: %w", err)
	}
	if deleted.Valid {
		t := deleted.Time
		p.DeletedAt = &t
	}
	return &p, nil
}
