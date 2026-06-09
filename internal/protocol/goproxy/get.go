package goproxy

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	chimw "github.com/go-chi/chi/v5/middleware"
	"golang.org/x/mod/semver"

	"github.com/vladoportos/omnirepo/internal/metadata"
)

// get dispatches GET /<project>/go/<repo>/<module>/@v/... and /@latest.
func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	res, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	if !h.actorCanRead(r, res.repo) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	switch res.req.Op {
	case "list":
		h.getList(w, r, res)
	case "latest":
		h.getLatest(w, r, res)
	case "info":
		h.getInfo(w, r, res)
	case "mod":
		h.serveFile(w, r, res, "mod", "text/plain; charset=utf-8")
	case "zip":
		h.serveFile(w, r, res, "zip", "application/zip")
	default:
		http.Error(w, "malformed module path", http.StatusBadRequest)
	}
}

// getList serves /@v/list: known versions, semver-sorted ascending, one
// per line. An empty body (with 200) means "no versions" per the proxy
// protocol — the go command treats it as module-not-found.
func (h *Handler) getList(w http.ResponseWriter, r *http.Request, res resolved) {
	rows, err := h.goModules.ListVersions(r.Context(), res.repo.ID, res.req.ModulePath)
	if err != nil {
		slog.ErrorContext(r.Context(), "goproxy.list_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("module", res.req.ModulePath),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	versions := make([]string, 0, len(rows))
	for _, m := range rows {
		versions = append(versions, m.Version)
	}
	semverSort(versions)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	for _, v := range versions {
		_, _ = io.WriteString(w, v+"\n")
	}
}

// getLatest serves /@latest: the highest release version, or the highest
// pre-release when no release exists (mirrors the proxy protocol's
// definition). 404 when the module has no versions at all.
func (h *Handler) getLatest(w http.ResponseWriter, r *http.Request, res resolved) {
	rows, err := h.goModules.ListVersions(r.Context(), res.repo.ID, res.req.ModulePath)
	if err != nil {
		slog.ErrorContext(r.Context(), "goproxy.latest_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("module", res.req.ModulePath),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	var best *metadata.GoModule
	for i := range rows {
		m := &rows[i]
		if best == nil {
			best = m
			continue
		}
		bestPre := semver.Prerelease(best.Version) != ""
		mPre := semver.Prerelease(m.Version) != ""
		switch {
		case bestPre && !mPre:
			best = m
		case bestPre == mPre && semver.Compare(m.Version, best.Version) > 0:
			best = m
		}
	}
	if best == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeInfo(w, best)
}

// getInfo serves /@v/<version>.info from the row.
func (h *Handler) getInfo(w http.ResponseWriter, r *http.Request, res resolved) {
	m, err := h.goModules.FindByModuleVersion(r.Context(), res.repo.ID, res.req.ModulePath, res.req.Version)
	if err != nil {
		if errors.Is(err, metadata.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		slog.ErrorContext(r.Context(), "goproxy.info_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("module", res.req.ModulePath),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	writeInfo(w, m)
}

// writeInfo emits the {"Version","Time"} JSON shared by .info and @latest.
func writeInfo(w http.ResponseWriter, m *metadata.GoModule) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(struct {
		Version string
		Time    time.Time
	}{Version: m.Version, Time: m.UploadedAt.UTC()})
}

// serveFile streams the on-disk .mod or .zip for a recorded version.
// The row is checked first so a file orphaned by a failed delete cannot
// resurrect a deleted version.
func (h *Handler) serveFile(w http.ResponseWriter, r *http.Request, res resolved, ext, contentType string) {
	if _, err := h.goModules.FindByModuleVersion(r.Context(), res.repo.ID, res.req.ModulePath, res.req.Version); err != nil {
		if errors.Is(err, metadata.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		slog.ErrorContext(r.Context(), "goproxy.row_lookup_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("module", res.req.ModulePath),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}

	key := storageKeyFor(res.project.Name, res.repo.Name, res.req.EscapedPath, res.req.Version, ext)
	abs := filepath.Join(h.repoRoot, filepath.FromSlash(key))
	info, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		slog.ErrorContext(r.Context(), "goproxy.stat_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("path", abs),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	f, err := os.Open(abs)
	if err != nil {
		slog.ErrorContext(r.Context(), "goproxy.open_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("path", abs),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	defer func() { _ = f.Close() }()

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f)
}

// semverSort orders versions ascending per semver.Compare.
func semverSort(versions []string) {
	sort.Slice(versions, func(i, j int) bool {
		return semver.Compare(versions[i], versions[j]) < 0
	})
}
