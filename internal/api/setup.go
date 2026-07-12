package api

// First-run setup endpoints. Exposed under /api/v1/setup/* as unauthenticated
// routes, parallel to /auth/login. Purpose: let an operator bring up a fresh
// OmniRepo instance without seeding bootstrap.json, by creating the first
// super-admin through the web UI.
//
// Security shape:
//   - GET  /api/v1/setup/status      — returns {needs_setup: bool} based on
//     whether the users table has zero live rows. Safe to poll.
//   - POST /api/v1/setup/superadmin  — creates the super-admin only if the
//     users table is still empty. Returns 409 once any user exists, so the
//     endpoint is naturally one-shot; no feature flag or disable step needed.
//
// Both endpoints are unauthenticated by design — there is no one to
// authenticate as on an empty database. The 409 gate on the write endpoint
// is the single source of truth for "setup already done".

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/auth"
)

// SetupStatusResponse, SetupSuperAdminBody, and SetupSuperAdminReply
// are generated from openapi.yaml (types_gen.go). The two request/response
// aliases below preserve the hand-written call-site names within this
// file so existing handler code keeps compiling unchanged.

// SetupSuperAdminRequest is an alias for the generated request body.
// SetupSuperAdminResponse is an alias for the generated success reply.
type (
	SetupSuperAdminRequest  = SetupSuperAdminBody
	SetupSuperAdminResponse = SetupSuperAdminReply
)

// ErrSetupAlreadyDone is returned when the endpoint is called after at least
// one user already exists in the database.
var ErrSetupAlreadyDone = errors.New("setup already completed")

// usersEmpty counts live users. A zero count means the install is fresh and
// /setup should be available.
func (d Deps) usersEmpty(ctx context.Context) (bool, error) {
	n, err := d.Users.Count(ctx)
	if err != nil {
		return false, err
	}
	return n == 0, nil
}

func (d Deps) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	empty, err := d.usersEmpty(r.Context())
	if err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	writeJSON(w, http.StatusOK, SetupStatusResponse{NeedsSetup: empty})
}

func (d Deps) handleSetupSuperAdmin(w http.ResponseWriter, r *http.Request) {
	// Setup is permanently retired as soon as the first live user exists.
	// Reject before decoding or hashing so the unauthenticated one-shot route
	// cannot be abused as an Argon2 CPU/memory sink after installation. The
	// authoritative empty-check remains inside WriteTx below to serialize two
	// legitimate first-run requests racing an empty database.
	empty, err := d.usersEmpty(r.Context())
	if err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	if !empty {
		writeJSONError(w, r, http.StatusConflict, ErrConflict, "setup already completed")
		return
	}

	var req SetupSuperAdminRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		writeJSONError(w, r, http.StatusBadRequest, ErrValidationFailed, "invalid JSON")
		return
	}

	// Cheap pre-validate BEFORE taking the writer lock so obviously bad
	// input doesn't burn argon2 CPU or hold the single write connection.
	// These are re-asserted in the tx below so a client can't bypass them
	// by racing past the pre-check.
	if err := auth.LoginValid(req.Login); err != nil {
		writeJSONError(w, r, http.StatusUnprocessableEntity, ErrValidationFailed, err.Error())
		return
	}
	if strings.TrimSpace(req.Email) == "" {
		writeJSONError(w, r, http.StatusUnprocessableEntity, ErrValidationFailed, "email empty")
		return
	}
	if err := auth.PasswordValid(req.Password); err != nil {
		writeJSONError(w, r, http.StatusUnprocessableEntity, ErrValidationFailed, err.Error())
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}

	// Atomic empty-check-and-insert inside one write tx. BEGIN IMMEDIATE +
	// Writer.SetMaxOpenConns(1) (metadata/tx.go) serialize writers, so the
	// COUNT and INSERT share the same reserved lock. Two concurrent
	// bootstrap requests with different logins can no longer both pass the
	// empty-check and both succeed — the second one sees count>0 inside its
	// tx and returns ErrSetupAlreadyDone.
	err = d.DB.WriteTx(r.Context(), func(tx *sql.Tx) error {
		var n int
		if scanErr := tx.QueryRowContext(r.Context(),
			`SELECT COUNT(*) FROM users WHERE deleted_at IS NULL`,
		).Scan(&n); scanErr != nil {
			return scanErr
		}
		if n > 0 {
			return ErrSetupAlreadyDone
		}
		if _, execErr := tx.ExecContext(r.Context(), `
			INSERT INTO users(login, email, password_hash, is_super_admin, must_change_password)
			VALUES (?, ?, ?, 1, 0)
		`, req.Login, req.Email, hash); execErr != nil {
			return execErr
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrSetupAlreadyDone) {
			writeJSONError(w, r, http.StatusConflict, ErrConflict, "setup already completed")
			return
		}
		// Defence in depth: if somehow a row with this login already existed
		// outside the soft-deleted window, the UNIQUE index fires. Map that
		// to 409 with the same message rather than 500.
		if strings.Contains(err.Error(), "UNIQUE") || strings.Contains(err.Error(), "constraint") {
			writeJSONError(w, r, http.StatusConflict, ErrConflict, "setup already completed")
			return
		}
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}

	// Audit — reuse EvtUserCreated with outcome=first_run so the audit
	// enumeration test (TestEveryStateChangingActionEmitsEvent) does not
	// need a new kind. The actor is nil because no one is authenticated.
	d.recordAudit(r, audit.Event{
		Kind:       audit.EvtUserCreated,
		TargetKind: "user",
		TargetID:   req.Login,
		Outcome:    "first_run_superadmin",
	})

	writeJSON(w, http.StatusOK, SetupSuperAdminResponse{Login: req.Login, IsSuperAdmin: true})
}
