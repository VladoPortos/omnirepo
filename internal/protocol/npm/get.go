package npm

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	chimw "github.com/go-chi/chi/v5/middleware"
)

// get dispatches GET /-/ping, packuments, and tarball downloads.
func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	res, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	if res.req.Op == "ping" {
		// `npm ping` — cheap liveness, allowed for anyone who can reach
		// the repo path (no package data is exposed).
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = io.WriteString(w, "{}")
		return
	}
	if !h.actorCanRead(r, res.repo) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	switch res.req.Op {
	case "packument":
		h.getPackument(w, r, res)
	case "tarball":
		h.getTarball(w, r, res)
	default:
		http.Error(w, "malformed package path", http.StatusBadRequest)
	}
}

// getPackument assembles the package document from npm_packages rows:
// dist-tags from npm_dist_tags, one versions entry per row with
// dist.tarball rewritten to THIS server (the stored manifest may carry
// the URL of whatever registry the publisher had configured).
func (h *Handler) getPackument(w http.ResponseWriter, r *http.Request, res resolved) {
	rows, err := h.packages.ListVersions(r.Context(), res.repo.ID, res.req.Name)
	if err != nil {
		slog.ErrorContext(r.Context(), "npm.packument_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("package", res.req.Name),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	if len(rows) == 0 {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":"Not found"}`)
		return
	}

	versions := make(map[string]json.RawMessage, len(rows))
	times := map[string]string{}
	for i := range rows {
		p := &rows[i]
		var manifest map[string]any
		if err := json.Unmarshal([]byte(p.VersionJSON), &manifest); err != nil {
			manifest = map[string]any{"name": p.Name, "version": p.Version}
		}
		manifest["dist"] = map[string]any{
			"tarball":   h.tarballURL(r, res.project.Name, res.repo.Name, p.Name, p.Tarball),
			"shasum":    p.Shasum,
			"integrity": p.Integrity,
		}
		b, err := json.Marshal(manifest)
		if err != nil {
			continue
		}
		versions[p.Version] = b
		times[p.Version] = p.UploadedAt.UTC().Format("2006-01-02T15:04:05.000Z")
	}

	tags, err := h.packages.DistTags(r.Context(), res.repo.ID, res.req.Name)
	if err != nil || len(tags) == 0 {
		// Self-heal: a packument without dist-tags breaks `npm install`.
		// Point latest at the lexicographically last version as a fallback
		// (publish always writes the real tag rows).
		tags = map[string]string{"latest": rows[len(rows)-1].Version}
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"_id":       res.req.Name,
		"name":      res.req.Name,
		"dist-tags": tags,
		"versions":  versions,
		"time":      times,
	})
}

// tarballURL builds the absolute dist.tarball URL for this server,
// honoring X-Forwarded-Proto for reverse-proxy deployments.
func (h *Handler) tarballURL(r *http.Request, project, repo, name, file string) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if xf := r.Header.Get("X-Forwarded-Proto"); xf == "https" || xf == "http" {
		scheme = xf
	}
	return scheme + "://" + r.Host + "/" + project + "/npm/" + repo + "/" + name + "/-/" + file
}

// getTarball streams a stored tarball. The row is checked first so a
// file orphaned by a failed delete cannot resurrect a deleted version.
func (h *Handler) getTarball(w http.ResponseWriter, r *http.Request, res resolved) {
	rows, err := h.packages.ListVersions(r.Context(), res.repo.ID, res.req.Name)
	if err != nil {
		slog.ErrorContext(r.Context(), "npm.tarball.row_lookup_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("package", res.req.Name),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	known := false
	for i := range rows {
		if rows[i].Tarball == res.req.File {
			known = true
			break
		}
	}
	if !known {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	key := storageKeyFor(res.project.Name, res.repo.Name, res.req.Name, res.req.File)
	abs := filepath.Join(h.repoRoot, filepath.FromSlash(key))
	info, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		slog.ErrorContext(r.Context(), "npm.tarball.stat_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("path", abs),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	f, err := os.Open(abs)
	if err != nil {
		slog.ErrorContext(r.Context(), "npm.tarball.open_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("path", abs),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	defer func() { _ = f.Close() }()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f)
}
