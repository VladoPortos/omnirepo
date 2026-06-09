package common

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/vladoportos/omnirepo/internal/storage"
)

// StagedUpload describes a request body streamed to a temp file.
type StagedUpload struct {
	TmpPath string
	Size    int64
	Sum256  string // lowercase hex digest, no "sha256:" prefix
}

// StageBody caps r.Body at maxBytes and streams it to a temp file under
// <repoRoot>/.tmp-<proto>-uploads while computing SHA-256 in one pass.
// Memory consumption stays bounded by the OS write buffer regardless of
// artifact size. On failure the appropriate HTTP error is written to w
// (413 on MaxBytesError, 500 otherwise), the failure is logged under
// "<proto>.put.*", and ok=false is returned.
//
// On success the caller owns the temp file and must
// `defer os.Remove(st.TmpPath)`.
func StageBody(w http.ResponseWriter, r *http.Request, repoRoot, proto, tmpPattern, filename string, maxBytes int64) (StagedUpload, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)

	tmpDir := filepath.Join(repoRoot, ".tmp-"+proto+"-uploads")
	if err := os.MkdirAll(tmpDir, 0o750); err != nil {
		slog.ErrorContext(r.Context(), proto+".put.mkdir_tmp_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return StagedUpload{}, false
	}
	tmpF, err := os.CreateTemp(tmpDir, tmpPattern)
	if err != nil {
		slog.ErrorContext(r.Context(), proto+".put.tmp_create_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return StagedUpload{}, false
	}
	tmpPath := tmpF.Name()

	hasher := sha256.New()
	size, err := io.Copy(io.MultiWriter(tmpF, hasher), r.Body)
	if err != nil {
		_ = tmpF.Close()
		_ = os.Remove(tmpPath)
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return StagedUpload{}, false
		}
		slog.ErrorContext(r.Context(), proto+".put.read_body_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("filename", filename),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return StagedUpload{}, false
	}
	if err := tmpF.Close(); err != nil {
		_ = os.Remove(tmpPath)
		slog.ErrorContext(r.Context(), proto+".put.tmp_close_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("tmp_path", tmpPath),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return StagedUpload{}, false
	}
	return StagedUpload{
		TmpPath: tmpPath,
		Size:    size,
		Sum256:  hex.EncodeToString(hasher.Sum(nil)),
	}, true
}

// PromoteStaged re-opens the staged temp file and writes it into pathStore
// at storageKey (atomic temp+fsync+rename inside PathStore.Put). The OS
// page cache covers the bytes just written, and *os.File satisfies
// io.Reader without allocating a full-body buffer. On failure a 500 is
// written to w, the failure is logged under "<proto>.put.*", and false is
// returned.
func PromoteStaged(w http.ResponseWriter, r *http.Request, pathStore storage.PathStore, proto, storageKey, tmpPath, filename string) bool {
	putF, err := os.Open(tmpPath)
	if err != nil {
		slog.ErrorContext(r.Context(), proto+".put.tmp_reopen_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("tmp_path", tmpPath),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return false
	}
	if _, err := pathStore.Put(r.Context(), storageKey, putF); err != nil {
		_ = putF.Close()
		slog.ErrorContext(r.Context(), proto+".put.storage_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("filename", filename),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return false
	}
	_ = putF.Close()
	return true
}
