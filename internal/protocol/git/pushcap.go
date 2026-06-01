package git

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"

	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/protocol/git/pktline"
)

// PushSizeLimit returns a chi middleware that wraps r.Body with
// http.MaxBytesReader BEFORE any gzip decompression (cap on
// wire bytes, not decoded bytes). This is the zip-bomb defense: even if the
// decoded payload is orders of magnitude larger, the wire-byte cap triggers
// first.
//
// On *http.MaxBytesError the middleware writes a sideband band-3 error
// packet with the literal limit message and returns HTTP 413.
//
// Read operations (upload-pack, info/refs) bypass the cap entirely.
func PushSizeLimit(resolveCap func(repo *metadata.Repo) int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !isReceivePackWrite(r) {
				next.ServeHTTP(w, r)
				return
			}

			repo := RepoFromContext(r.Context())
			if repo == nil {
				next.ServeHTTP(w, r)
				return
			}

			capBytes := resolveCap(repo)

			// Wrap r.Body with MaxBytesReader BEFORE any gzip decompression.
			// The gogit handler's gzip.NewReader wraps r.Body AFTER this
			// middleware, so the cap applies to wire bytes.
			limited := http.MaxBytesReader(w, r.Body, capBytes)
			cr := &capturingReader{ReadCloser: limited}
			r.Body = cr

			// Use a buffered writer that delays header writes so we can
			// intercept MaxBytesError before the response is committed.
			bw := &bufferingWriter{ResponseWriter: w}
			next.ServeHTTP(bw, r)

			// After the handler returns, check if MaxBytesError was hit.
			if cr.hitMaxBytes.Load() && !bw.committed {
				mib := capBytes / (1024 * 1024)
				msg := fmt.Sprintf("error: push exceeds repo limit of %d MiB; contact a project admin\n", mib)
				w.Header().Set("Content-Type", "application/x-git-receive-pack-result")
				w.WriteHeader(http.StatusRequestEntityTooLarge)
				_, _ = pktline.WriteSidebandError(w, msg)
				return
			}

			// Flush any buffered response.
			bw.flush()
		})
	}
}

// capturingReader wraps an io.ReadCloser and records whether a
// *http.MaxBytesError was encountered during Read.
type capturingReader struct {
	io.ReadCloser
	hitMaxBytes atomic.Bool
}

func (r *capturingReader) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			r.hitMaxBytes.Store(true)
		}
	}
	return n, err
}

// bufferingWriter delays writing headers until flush() is called or a
// non-error status is explicitly written. This gives the pushcap middleware
// a window to detect MaxBytesError after the handler returns but before
// the response is sent.
// maxBufferedResponse caps the buffered response at 1 MiB. Git receive-pack
// responses are typically a few KB; if we exceed this, flush early (losing
// the MaxBytesError intercept window but preventing unbounded memory use).
const maxBufferedResponse = 1 << 20

type bufferingWriter struct {
	http.ResponseWriter
	committed    bool
	pendingCode  int
	pendingBytes []byte
}

func (w *bufferingWriter) WriteHeader(code int) {
	if w.committed {
		return
	}
	w.pendingCode = code
}

func (w *bufferingWriter) Write(b []byte) (int, error) {
	if w.committed {
		return w.ResponseWriter.Write(b)
	}
	w.pendingBytes = append(w.pendingBytes, b...)
	if len(w.pendingBytes) > maxBufferedResponse {
		w.flush()
		return len(b), nil
	}
	return len(b), nil
}

// Flush implements http.Flusher. Required because gitkit's newWriteFlusher
// asserts the writer has a Flush method.
func (w *bufferingWriter) Flush() {
	w.flush()
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *bufferingWriter) flush() {
	if w.committed {
		return
	}
	w.committed = true
	if w.pendingCode > 0 {
		w.ResponseWriter.WriteHeader(w.pendingCode)
	}
	if len(w.pendingBytes) > 0 {
		_, _ = w.ResponseWriter.Write(w.pendingBytes)
	}
}

// ResolveMaxPushBytes returns a cap-resolution function that checks the
// per-repo override first, then falls back to the global config value.
// Precedence: per-repo override > global cfg.Repos.Git.MaxPushBytes > 500 MiB.
func ResolveMaxPushBytes(globalCap int64) func(*metadata.Repo) int64 {
	return func(repo *metadata.Repo) int64 {
		if repo.GitMaxPushBytes != nil && *repo.GitMaxPushBytes > 0 {
			return *repo.GitMaxPushBytes
		}
		return globalCap
	}
}
