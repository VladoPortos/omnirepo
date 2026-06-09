package npm

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	chimw "github.com/go-chi/chi/v5/middleware"
	"golang.org/x/mod/semver"

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/auth"
	"github.com/vladoportos/omnirepo/internal/metadata"
)

// delete handles DELETE /<name>/-/<version>.
//
// Flow mirrors the rpm/goproxy delete ordering: stat first (preserves
// the partial-state heal), commit the DB tx BEFORE trash.Move, then move
// the tarball to trash. Dist-tags pointing at the deleted version are
// dropped in the same tx; when "latest" was among them and other
// versions remain, latest is re-pointed at the highest remaining
// version so the packument stays installable.
func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	res, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	if res.req.Op != "delete" {
		http.Error(w, "delete addresses <name>/-/<version>", http.StatusBadRequest)
		return
	}
	if err := validateVersion(res.req.Version); err != nil {
		http.Error(w, "invalid version", http.StatusBadRequest)
		return
	}
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return
	}
	if !h.requireRepoWrite(r.Context(), actor, res.project.ID, auth.ActionUpdateRepo) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	target := res.req.Name + "@" + res.req.Version
	row, err := h.packages.FindByNameVersion(r.Context(), res.repo.ID, res.req.Name, res.req.Version)
	if err != nil || row == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	key := storageKeyFor(res.project.Name, res.repo.Name, res.req.Name, row.Tarball)
	abs := filepath.Join(h.repoRoot, filepath.FromSlash(key))
	onDisk := false
	if _, err := os.Stat(abs); err == nil {
		onDisk = true
	} else if !errors.Is(err, os.ErrNotExist) {
		slog.ErrorContext(r.Context(), "npm.delete.stat_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("package", target),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}

	// Remaining versions (computed pre-tx) drive the latest re-point.
	siblings, err := h.packages.ListVersions(r.Context(), res.repo.ID, res.req.Name)
	if err != nil {
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	tags, _ := h.packages.DistTags(r.Context(), res.repo.ID, res.req.Name)
	latestWasDeleted := tags["latest"] == res.req.Version
	newLatest := highestVersionExcept(siblings, res.req.Version)

	if err := h.db.WriteTx(r.Context(), func(tx *sql.Tx) error {
		if err := h.packages.Delete(r.Context(), tx, row.ID); err != nil {
			return err
		}
		if err := h.packages.DeleteDistTagsPointingAt(r.Context(), tx, res.repo.ID, res.req.Name, res.req.Version); err != nil {
			return err
		}
		if latestWasDeleted && newLatest != "" {
			if err := h.packages.SetDistTag(r.Context(), tx, res.repo.ID, res.req.Name, "latest", newLatest); err != nil {
				return err
			}
		}
		return metadata.IndexArtifactDelete(r.Context(), tx, res.repo.ID, row.Integrity)
	}); err != nil {
		slog.ErrorContext(r.Context(), "npm.delete.commit_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("package", target),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}

	if onDisk {
		if _, err := h.trash.Move(r.Context(), abs, "npm-package", res.repo.ID, auth.ActorLoginFromContext(r.Context())); err != nil {
			slog.WarnContext(r.Context(), "npm.delete.trash_failed_post_commit",
				slog.String("incident_id", chimw.GetReqID(r.Context())),
				slog.String("package", target),
				slog.Any("err", err),
			)
			http.Error(w, "storage error", http.StatusInternalServerError)
			return
		}
	}

	h.auditEvent(r, audit.EvtNPMDelete, target, "ok", map[string]any{
		"project": res.project.Name,
		"repo":    res.repo.Name,
		"package": res.req.Name,
		"version": res.req.Version,
	})

	w.WriteHeader(http.StatusNoContent)
}

// highestVersionExcept returns the semver-highest version in rows other
// than excluded ("" when none remain). npm versions lack the "v" prefix
// x/mod/semver requires, so compare with it prepended.
func highestVersionExcept(rows []metadata.NPMPackage, excluded string) string {
	best := ""
	for i := range rows {
		v := rows[i].Version
		if v == excluded {
			continue
		}
		if best == "" || semver.Compare("v"+v, "v"+best) > 0 {
			best = v
		}
	}
	return best
}

// DeleteREST is the exported wrapper that the session-authed /api/v1 shim
// mounts at DELETE /api/v1/projects/{name}/repos/npm/{repo}/*. It
// dispatches to the same internal logic as the protocol-native DELETE
// route; resolveRepo handles the {name} → {project} URL-param fallback.
func (h *Handler) DeleteREST(w http.ResponseWriter, r *http.Request) {
	h.delete(w, r)
}
