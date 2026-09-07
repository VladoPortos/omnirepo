package s3

import (
	"net/http"
	"strings"

	"github.com/vladoportos/omnirepo/internal/protocol/s3/backend"
)

// gofakes3 represents validators as MD5 bytes. Preserve the backend's opaque
// validator at the HTTP boundary, including multipart suffixes and conditions.
func opaqueETags(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		rw := &etagWriter{ResponseWriter: w, match: r.Header.Get("If-Match"), none: r.Header.Get("If-None-Match"), head: r.Method == http.MethodHead}
		r = r.Clone(r.Context())
		r.Header.Del("If-Match")
		r.Header.Del("If-None-Match")
		next.ServeHTTP(rw, r)
		if !rw.wrote {
			rw.WriteHeader(http.StatusOK)
		}
	})
}

type etagWriter struct {
	http.ResponseWriter
	match, none     string
	wrote, suppress bool
	head            bool
}

func matchesETag(list, etag string) bool {
	for _, v := range strings.Split(list, ",") {
		v = strings.TrimSpace(v)
		if v == "*" || v == etag {
			return true
		}
	}
	return false
}

func (w *etagWriter) WriteHeader(status int) {
	if w.wrote {
		return
	}
	w.wrote = true
	etag := w.Header().Get(backend.OpaqueETagHeader)
	w.Header().Del(backend.OpaqueETagHeader)
	if etag != "" && (status == http.StatusOK || status == http.StatusPartialContent || status == http.StatusNotModified) {
		w.Header().Set("ETag", etag)
		if w.match != "" && !matchesETag(w.match, etag) {
			status = http.StatusPreconditionFailed
			w.suppress = true
		} else if w.none != "" && matchesETag(w.none, etag) {
			status = http.StatusNotModified
			w.suppress = true
		}
		if w.suppress {
			w.Header().Del("Content-Length")
			w.Header().Del("Content-Range")
		}
	}
	if w.suppress && status == http.StatusPreconditionFailed {
		w.Header().Set("Content-Type", "application/xml")
		w.Header().Del("Content-Encoding")
	}
	w.ResponseWriter.WriteHeader(status)
	if w.suppress && status == http.StatusPreconditionFailed && !w.head {
		_, _ = w.ResponseWriter.Write([]byte(`<Error><Code>PreconditionFailed</Code><Message>At least one condition was not met</Message></Error>`))
	}
}

func (w *etagWriter) Write(p []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	if w.suppress {
		return len(p), nil
	}
	return w.ResponseWriter.Write(p)
}
