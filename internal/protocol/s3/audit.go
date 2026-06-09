package s3

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/johannesboyne/gofakes3"

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/auth"
)

// AuditMiddleware records audit events for mutating S3-protocol requests,
// closing the gap where every other protocol audited uploads/deletes but
// S3 object and bucket mutations were invisible to the audit log.
//
// Mounted after SigV4Middleware (so the S3-key actor is on ctx) and before
// RequireBucketAccess (so 403 denials are recorded with outcome=denied,
// matching the git middleware's audit-the-denial behavior). Reads are not
// audited; neither are part uploads or multipart creation (high volume /
// already attributed — CompleteMultipartUpload is the event that
// materializes an object).
//
// Best-effort: a nil logger disables the middleware, and Record errors are
// swallowed (consistent with every other protocol's auditEvent helper).
func AuditMiddleware(logger audit.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if logger == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			kind, targetKind, ok := classifyS3Mutation(r)
			if !ok {
				next.ServeHTTP(w, r)
				return
			}

			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)

			bucket, key := bucketKeyFromPath(r.URL.Path)
			details := map[string]any{
				"bucket": bucket,
				"status": rec.status,
			}
			if key != "" {
				details["key"] = key
			}
			if targetKind == "s3_bucket" {
				// Distinguish protocol-path bucket events from the REST
				// provisioning endpoint, which emits the same kinds.
				details["source"] = "s3-api"
			}
			if r.Method == http.MethodPost && r.URL.Query().Has("delete") {
				details["batch"] = true
			}
			e := audit.Event{
				Kind:       kind,
				IP:         r.RemoteAddr,
				UserAgent:  r.Header.Get("User-Agent"),
				TargetKind: targetKind,
				TargetID:   strings.TrimSuffix(bucket+"/"+key, "/"),
				Outcome:    outcomeFromStatus(rec.status),
				Details:    details,
				OccurredAt: time.Now().UTC(),
			}
			if a, aok := auth.ActorFromContext(r.Context()); aok && a.Kind == auth.ActorKindS3Key && a.S3KeyID != nil {
				id := *a.S3KeyID
				e.ActorS3KeyID = &id
			}
			_ = logger.Record(r.Context(), e)
		})
	}
}

// classifyS3Mutation maps (method, path, query) onto the audit event kind.
// Returns ok=false for requests that must not produce an event (reads,
// part uploads, multipart creation).
func classifyS3Mutation(r *http.Request) (audit.EventKind, string, bool) {
	bucket, key := bucketKeyFromPath(r.URL.Path)
	if bucket == "" {
		return "", "", false
	}
	q := r.URL.Query()
	switch r.Method {
	case http.MethodPut:
		if key == "" {
			return audit.EvtS3BucketCreated, "s3_bucket", true
		}
		// UploadPart carries partNumber+uploadId; the complete event covers it.
		if q.Has("partNumber") || q.Has("uploadId") {
			return "", "", false
		}
		return audit.EvtS3ObjectPut, "s3_object", true
	case http.MethodDelete:
		if key == "" {
			return audit.EvtS3BucketDeleted, "s3_bucket", true
		}
		if q.Has("uploadId") {
			return audit.EvtS3MultipartAborted, "s3_multipart", true
		}
		return audit.EvtS3ObjectDelete, "s3_object", true
	case http.MethodPost:
		if key != "" && q.Has("uploadId") {
			return audit.EvtS3MultipartCompleted, "s3_multipart", true
		}
		// POST /<bucket>?delete — multi-object batch delete.
		if key == "" && q.Has("delete") {
			return audit.EvtS3ObjectDelete, "s3_object", true
		}
		return "", "", false
	default:
		return "", "", false
	}
}

// bucketKeyFromPath splits "/s3/<bucket>/<key...>" into (bucket, key).
// key is "" for bucket-level paths.
func bucketKeyFromPath(path string) (string, string) {
	trimmed := strings.TrimPrefix(path, "/s3/")
	if trimmed == "" || trimmed == path {
		return "", ""
	}
	if idx := strings.IndexByte(trimmed, '/'); idx >= 0 {
		return trimmed[:idx], trimmed[idx+1:]
	}
	return trimmed, ""
}

// outcomeFromStatus maps the response status onto the audit outcome
// vocabulary used by the other protocols.
func outcomeFromStatus(status int) string {
	switch {
	case status < 300:
		return "ok"
	case status == http.StatusForbidden:
		return "denied"
	default:
		return "failed"
	}
}

// statusRecorder captures the response status for post-response auditing.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// slogAdapter bridges gofakes3's Logger interface onto the process-wide
// slog logger, so S3 backend failures (DB errors, IO errors surfacing as
// 500 InternalError XML) stop being silently swallowed — every other
// protocol logs storage/DB errors via slog.
type slogAdapter struct{}

func (slogAdapter) Print(level gofakes3.LogLevel, v ...interface{}) {
	msg := strings.TrimSpace(fmt.Sprintln(v...))
	switch level {
	case gofakes3.LogErr:
		slog.Error("s3.gofakes3", slog.String("msg", msg))
	case gofakes3.LogWarn:
		slog.Warn("s3.gofakes3", slog.String("msg", msg))
	default:
		slog.Info("s3.gofakes3", slog.String("msg", msg))
	}
}
