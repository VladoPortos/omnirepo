package pypi

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/auth"
	"github.com/vladoportos/omnirepo/internal/httperr"
)

// stagedFile holds the on-disk path + content hash of a single file
// staged inside a PEP 694 upload session, awaiting commit.
type stagedFile struct {
	TmpPath string
	Parsed  *File
}

// PEP694Session is one upload session. ActorID binds the session to the
// authenticated principal that created it — Get() rejects mismatched
// actors so a session-id leak alone can't be used to upload.
type PEP694Session struct {
	ID     string
	RepoID int64
	// ActorKey is "user:<id>" or "apikey:<id>" — Get checks equality.
	ActorKey  string
	Project   string
	Version   string
	CreatedAt time.Time
	ExpiresAt time.Time

	mu    sync.Mutex
	Files map[string]*stagedFile // filename → staged
}

// PEP694Sessions is the in-memory session store with TTL eviction
// (in-memory acceptable for v1).
type PEP694Sessions struct {
	mu  sync.Mutex
	m   map[string]*PEP694Session
	ttl time.Duration
}

// NewPEP694Sessions constructs the store; ttl <= 0 defaults to 1 hour.
func NewPEP694Sessions(ttl time.Duration) *PEP694Sessions {
	if ttl <= 0 {
		ttl = time.Hour
	}
	return &PEP694Sessions{m: make(map[string]*PEP694Session), ttl: ttl}
}

// Create allocates a new session bound to actorKey.
func (s *PEP694Sessions) Create(repoID, projectID int64, actorKey, project, version string) *PEP694Session {
	now := time.Now().UTC()
	id := newSessionID()
	sess := &PEP694Session{
		ID:        id,
		RepoID:    repoID,
		ActorKey:  actorKey,
		Project:   project,
		Version:   version,
		CreatedAt: now,
		ExpiresAt: now.Add(s.ttl),
		Files:     make(map[string]*stagedFile),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gcLocked(now)
	s.m[id] = sess
	return sess
}

// Get returns the session for id IF it exists, has not expired, AND
// belongs to actorKey. Returns sentinel errors for the three negative
// cases so handlers can pick the correct HTTP status.
var (
	ErrSessionNotFound   = errors.New("pypi: session not found")
	ErrSessionExpired    = errors.New("pypi: session expired")
	ErrSessionWrongActor = errors.New("pypi: session does not belong to actor")
)

func (s *PEP694Sessions) Get(id, actorKey string) (*PEP694Session, error) {
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	// Check expiry FIRST so the caller can distinguish "expired" from
	// "never existed". Only GC unrelated expired sessions afterwards.
	sess, ok := s.m[id]
	if !ok {
		s.gcLocked(now)
		return nil, ErrSessionNotFound
	}
	if now.After(sess.ExpiresAt) {
		// Tear down THIS session before returning Expired.
		sess.mu.Lock()
		for _, f := range sess.Files {
			_ = os.Remove(f.TmpPath)
		}
		sess.mu.Unlock()
		delete(s.m, id)
		s.gcLocked(now)
		return nil, ErrSessionExpired
	}
	if sess.ActorKey != actorKey {
		return nil, ErrSessionWrongActor
	}
	return sess, nil
}

// Delete removes a session and cleans up any staged tmp files.
func (s *PEP694Sessions) Delete(id string) {
	s.mu.Lock()
	sess := s.m[id]
	delete(s.m, id)
	s.mu.Unlock()
	if sess == nil {
		return
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	for _, f := range sess.Files {
		_ = os.Remove(f.TmpPath)
	}
}

// gcLocked evicts expired sessions. Called from Create / Get under mu.
func (s *PEP694Sessions) gcLocked(now time.Time) {
	for id, sess := range s.m {
		if now.After(sess.ExpiresAt) {
			// Best-effort tmp cleanup outside the mu — but holding mu is
			// short-lived and tmp files are bounded in count.
			sess.mu.Lock()
			for _, f := range sess.Files {
				_ = os.Remove(f.TmpPath)
			}
			sess.mu.Unlock()
			delete(s.m, id)
		}
	}
}

// newSessionID returns a 32-byte crypto/rand session id, hex-encoded
// (64 chars). Sufficiently unguessable for capability-as-identifier.
func newSessionID() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand should never fail in practice on a healthy host;
		// if it does, fall back to time-based bytes so the server
		// doesn't panic at request time.
		now := time.Now().UnixNano()
		for i := range b {
			b[i] = byte(now >> (i % 8))
		}
	}
	return hex.EncodeToString(b[:])
}

// actorKeyFromContext returns "user:<id>" / "apikey:<id>" / "" depending
// on the actor in ctx. Used to bind a PEP 694 session to the creator.
func actorKey(actor auth.Actor) string {
	switch actor.Kind {
	case auth.ActorKindUser:
		return fmt.Sprintf("user:%d", actor.ID)
	case auth.ActorKindAPIKey:
		return fmt.Sprintf("apikey:%d", actor.APIKeyID)
	}
	return ""
}

// -----------------------------------------------------------------------------
// HTTP handlers
// -----------------------------------------------------------------------------

// handleCreateSession services POST /<project>/pypi/<repo>/+upload/.
//
// Body: {"meta":{"api-version":"1.0"},"name":"foo","version":"1.0"}
// Response 201: {"session-id":"<hex>","upload-urls":[...]}
func (h *Handler) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	res, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return
	}
	if !h.requireRepoWrite(r.Context(), actor, res.project.ID, auth.ActionPyPIUpload) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	ak := actorKey(actor)
	if ak == "" {
		http.Error(w, "actor cannot create sessions", http.StatusForbidden)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	defer func() { _ = r.Body.Close() }()
	var req struct {
		Meta    map[string]any `json:"meta"`
		Name    string         `json:"name"`
		Version string         `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.Version == "" {
		http.Error(w, "name and version required", http.StatusBadRequest)
		return
	}
	sess := h.pep694.Create(res.repo.ID, res.project.ID, ak, Normalize(req.Name), req.Version)

	h.auditEvent(r, audit.EvtPyPIUpload, sess.ID, "session_created", map[string]any{
		"project":            res.project.Name,
		"repo":               res.repo.Name,
		"project_normalized": sess.Project,
		"version":            sess.Version,
		"session_id":         sess.ID,
		"flow":               "pep694",
	})

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	resp := map[string]any{
		"session-id": sess.ID,
		"upload-url-template": fmt.Sprintf("/%s/pypi/%s/+upload/%s/{filename}",
			res.project.Name, res.repo.Name, sess.ID),
		"commit-url": fmt.Sprintf("/%s/pypi/%s/+upload/%s/commit",
			res.project.Name, res.repo.Name, sess.ID),
		"expires-at": sess.ExpiresAt.Format(time.RFC3339),
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// handleUploadFile services PUT /<project>/pypi/<repo>/+upload/<sid>/<filename>.
// Stages the body in tmp keyed by filename.
func (h *Handler) handleUploadFile(w http.ResponseWriter, r *http.Request) {
	res, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return
	}
	if !h.requireRepoWrite(r.Context(), actor, res.project.ID, auth.ActionPyPIUpload) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	sid := chi.URLParam(r, "session_id")
	filename, err := validateFilename(chi.URLParam(r, "filename"))
	if err != nil {
		http.Error(w, "invalid filename", http.StatusBadRequest)
		return
	}
	sess, gerr := h.pep694.Get(sid, actorKey(actor))
	switch {
	case errors.Is(gerr, ErrSessionNotFound):
		http.Error(w, "session not found", http.StatusNotFound)
		return
	case errors.Is(gerr, ErrSessionExpired):
		http.Error(w, "session expired", http.StatusGone)
		return
	case errors.Is(gerr, ErrSessionWrongActor):
		http.Error(w, "session does not belong to actor", http.StatusForbidden)
		return
	case gerr != nil:
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	if sess.RepoID != res.repo.ID {
		http.Error(w, "session does not match repo", http.StatusForbidden)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, h.maxPutBytes)
	defer func() { _ = r.Body.Close() }()
	tmpDir := filepath.Join(h.repoRoot, ".tmp-pypi-uploads")
	if err := os.MkdirAll(tmpDir, 0o750); err != nil {
		slog.ErrorContext(r.Context(), "pypi.pep694.mkdir_tmp_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	tf, err := os.CreateTemp(tmpDir, "pep694-*")
	if err != nil {
		slog.ErrorContext(r.Context(), "pypi.pep694.tmp_create_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	tmpPath := tf.Name()
	// panic-leak guard. The promote step takes ownership of the temp path
	// on success; until then a panic in io.Copy / parsing would leak the
	// temp file. Deferred cleanup with an ownership-transfer flag closes
	// the gap. ownsTmp starts true and flips to false only when path-store
	// has accepted the file.
	ownsTmp := true
	defer func() {
		if ownsTmp && tmpPath != "" {
			_ = os.Remove(tmpPath)
		}
	}()
	hasher := sha256.New()
	n, err := io.Copy(io.MultiWriter(tf, hasher), r.Body)
	// Capture tf.Close() error — disk-full / writeback failure surfaces at
	// close even if io.Copy succeeded.
	if cerr := tf.Close(); cerr != nil && err == nil {
		err = cerr
	}
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		slog.ErrorContext(r.Context(), "pypi.pep694.tmp_write_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("tmp_path", tmpPath),
			slog.String("filename", filename),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	digest := "sha256:" + hex.EncodeToString(hasher.Sum(nil))

	// Parse to validate before staging.
	var (
		parsed *File
		perr   error
	)
	switch {
	case strings.HasSuffix(filename, ".whl"):
		parsed, perr = ParseWheelAs(tmpPath, filename)
	case strings.HasSuffix(filename, ".tar.gz"), strings.HasSuffix(filename, ".tgz"),
		strings.HasSuffix(filename, ".zip"):
		parsed, perr = ParseSdistAs(tmpPath, filename)
	default:
		perr = fmt.Errorf("unknown filename extension")
	}
	if perr != nil {
		// Cleanup is handled by the deferred ownership-flag remove
		// above. Explicit Remove here would be a no-op after the defer.
		h.auditEvent(r, audit.EvtPyPIUpload, filename, "rejected", map[string]any{
			"project": res.project.Name,
			"repo":    res.repo.Name,
			"reason":  "invalid_package",
			"flow":    "pep694",
			"error":   perr.Error(),
		})
		http.Error(w, "invalid_package: "+perr.Error(), http.StatusBadRequest)
		return
	}
	parsed.Filename = filename
	parsed.Digest = digest
	parsed.SizeBytes = n

	sess.mu.Lock()
	if existing, ok := sess.Files[filename]; ok {
		_ = os.Remove(existing.TmpPath)
	}
	sess.Files[filename] = &stagedFile{
		TmpPath: tmpPath,
		Parsed:  parsed,
	}
	sess.mu.Unlock()
	// Ownership has transferred to the session map. The commit endpoint
	// reads sess.Files[filename].TmpPath later — the deferred panic-leak
	// guard must NOT remove the file now.
	ownsTmp = false

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusAccepted)
	fmt.Fprintf(w, `{"status":"staged","filename":%q,"sha256":%q,"size":%d}`,
		filename, strings.TrimPrefix(digest, "sha256:"), n)
}

// handleCommit services POST /<project>/pypi/<repo>/+upload/<sid>/commit.
// Promotes every staged file into PathStore + writes pypi_files rows in
// individual tx (one per file), kicks the coalescer once at the end.
func (h *Handler) handleCommit(w http.ResponseWriter, r *http.Request) {
	res, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return
	}
	if !h.requireRepoWrite(r.Context(), actor, res.project.ID, auth.ActionPyPIUpload) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	sid := chi.URLParam(r, "session_id")
	sess, gerr := h.pep694.Get(sid, actorKey(actor))
	switch {
	case errors.Is(gerr, ErrSessionNotFound):
		http.Error(w, "session not found", http.StatusNotFound)
		return
	case errors.Is(gerr, ErrSessionExpired):
		http.Error(w, "session expired", http.StatusGone)
		return
	case errors.Is(gerr, ErrSessionWrongActor):
		http.Error(w, "session does not belong to actor", http.StatusForbidden)
		return
	case gerr != nil:
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	if sess.RepoID != res.repo.ID {
		http.Error(w, "session does not match repo", http.StatusForbidden)
		return
	}

	sess.mu.Lock()
	files := make([]*stagedFile, 0, len(sess.Files))
	for _, f := range sess.Files {
		files = append(files, f)
	}
	sess.mu.Unlock()

	if len(files) == 0 {
		http.Error(w, "no files staged", http.StatusBadRequest)
		return
	}

	committed := make([]string, 0, len(files))
	for _, f := range files {
		storageKey := packageStorageKey(res.project.Name, res.repo.Name, f.Parsed.Filename)

		// Hold the per-(repo, filename) mutex across pre-check → Put →
		// commit/rollback so concurrent first-uploads of the same filename
		// can't both Put (last-rename-wins on disk) with only one committing
		// a row. The defer fires at the end of this loop iteration so the
		// next file's handler can acquire its own lock; handler returns
		// inside the body also run the defer on the way out.
		unlock := h.lockUpload(storageKey)
		handled := false
		func() {
			defer unlock()
			// Pre-check existence BEFORE PathStore.Put to avoid overwriting
			// a winner's blob + unlinking it on rollback.
			if existing, ferr := h.pypiFiles.FindByFilename(r.Context(), res.repo.ID, f.Parsed.Filename); ferr == nil && existing != nil {
				h.auditEvent(r, audit.EvtPyPIUpload, f.Parsed.Filename, "rejected", map[string]any{
					"project":    res.project.Name,
					"repo":       res.repo.Name,
					"reason":     "file_exists",
					"session_id": sess.ID,
					"flow":       "pep694",
				})
				httperr.Write(w, r, httperr.Validation(
					"pypi.file_exists",
					"That filename already exists in this repo — delete it first if you need to replace it.",
					httperr.WithStatus(http.StatusConflict),
				))
				handled = true
				return
			}
			// Re-open the staged tmp file as *os.File and pass to
			// pathStore.Put — io.Copy streams the bytes directly, avoiding
			// the prior os.ReadFile that pulled the full body back into RAM.
			// Move the open INSIDE the locked closure so the fd lifetime
			// matches the critical section (no fd leak if a later check
			// returns early).
			putF, oerr := os.Open(f.TmpPath)
			if oerr != nil {
				slog.ErrorContext(r.Context(), "pypi.pep694.tmp_reopen_failed",
					slog.String("incident_id", chimw.GetReqID(r.Context())),
					slog.String("filename", f.Parsed.Filename),
					slog.Any("err", oerr),
				)
				http.Error(w, "storage error", http.StatusInternalServerError)
				handled = true
				return
			}
			if _, err := h.pathStore.Put(r.Context(), storageKey, putF); err != nil {
				_ = putF.Close()
				slog.ErrorContext(r.Context(), "pypi.pep694.storage_failed",
					slog.String("incident_id", chimw.GetReqID(r.Context())),
					slog.String("filename", f.Parsed.Filename),
					slog.Any("err", err),
				)
				http.Error(w, "storage error", http.StatusInternalServerError)
				handled = true
				return
			}
			_ = putF.Close()
			if err := h.commitPyPIRow(r, res, f.Parsed); err != nil {
				// Race defense-in-depth: even with the outer mutex the
				// writer-tx check can still fire if another process holds
				// the SQLite writer (it shouldn't in the single-writer
				// setup, but we code against the contract). Skip blob
				// Delete so we don't unlink winner bytes.
				if errors.Is(err, errPyPIFileExists) {
					slog.WarnContext(r.Context(), "pypi.pep694.dup_race_kept_winner_blob",
						slog.String("incident_id", chimw.GetReqID(r.Context())),
						slog.String("filename", f.Parsed.Filename),
					)
					h.auditEvent(r, audit.EvtPyPIUpload, f.Parsed.Filename, "rejected", map[string]any{
						"project":    res.project.Name,
						"repo":       res.repo.Name,
						"reason":     "file_exists",
						"session_id": sess.ID,
						"flow":       "pep694",
						"race":       true,
					})
					httperr.Write(w, r, httperr.Validation(
						"pypi.file_exists",
						"That filename already exists in this repo — delete it first if you need to replace it.",
						httperr.WithStatus(http.StatusConflict),
					))
					handled = true
					return
				}
				// Plain commit failure: roll back the on-disk artifact we
				// just Put. Safe because we hold the per-key mutex so no
				// other upload has raced a Put at this key.
				_ = h.pathStore.Delete(r.Context(), storageKey)
				slog.ErrorContext(r.Context(), "pypi.pep694.commit_failed",
					slog.String("incident_id", chimw.GetReqID(r.Context())),
					slog.String("filename", f.Parsed.Filename),
					slog.Any("err", err),
				)
				http.Error(w, "storage error", http.StatusInternalServerError)
				handled = true
				return
			}
		}()
		if handled {
			return
		}
		committed = append(committed, f.Parsed.Filename)
		h.auditEvent(r, audit.EvtPyPIUpload, f.Parsed.Filename, "ok", map[string]any{
			"project":            res.project.Name,
			"repo":               res.repo.Name,
			"project_normalized": f.Parsed.ProjectNormalized,
			"version":            f.Parsed.Version,
			"kind":               f.Parsed.Kind,
			"size_bytes":         f.Parsed.SizeBytes,
			"digest":             f.Parsed.Digest,
			"session_id":         sess.ID,
			"flow":               "pep694",
		})
	}

	if h.coalescer != nil {
		h.coalescer.Get(res.repo.ID).Kick()
	}

	// Discard the session + tmp files now that everything is on disk.
	h.pep694.Delete(sess.ID)

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	resp := map[string]any{
		"status":          "committed",
		"committed_files": committed,
		"session_id":      sess.ID,
	}
	_ = json.NewEncoder(w).Encode(resp)
}
