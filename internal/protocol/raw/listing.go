package raw

import (
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	chimw "github.com/go-chi/chi/v5/middleware"
)

// listingItem is the JSON-encoded shape of one directory entry (D-30).
type listingItem struct {
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	IsDir    bool      `json:"is_dir"`
	Modified time.Time `json:"modified"`
}

// listDir serves a directory listing for absDir. Content negotiation per
// D-30: Accept: application/json → JSON array; default → minimal HTML.
//
// On a non-existent directory (e.g. fresh repo with no files) returns an
// empty list rather than 404 — the repo exists, it just has no contents.
func (h *Handler) listDir(w http.ResponseWriter, r *http.Request, _ resolved, absDir string) {
	entries, err := os.ReadDir(absDir)
	if err != nil && !os.IsNotExist(err) {
		slog.ErrorContext(r.Context(), "raw.listing.readdir_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("path", absDir),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	items := make([]listingItem, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		items = append(items, listingItem{
			Name:     e.Name(),
			Size:     info.Size(),
			IsDir:    e.IsDir(),
			Modified: info.ModTime().UTC(),
		})
	}

	accept := r.Header.Get("Accept")
	if strings.Contains(accept, "application/json") {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(items)
		return
	}

	// Minimal HTML — no styling; UI rendering is Phase 5's concern.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "<!doctype html><html><body><ul>")
	for _, it := range items {
		name := html.EscapeString(it.Name)
		display := name
		href := name
		if it.IsDir {
			display += "/"
			href += "/"
		}
		fmt.Fprintf(w, `<li><a href="%s">%s</a> %d</li>`, href, display, it.Size)
	}
	fmt.Fprint(w, "</ul></body></html>")
}
