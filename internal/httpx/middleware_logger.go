package httpx

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/vladoportos/omnirepo/internal/config"
)

// LoginBox is a mutable holder for the authenticated login, threaded
// through the request context so outer middleware can read what inner
// middleware set. httpx imports no auth types; cmd/omnirepo wires
// auth.LoginBox into this interface-compatible shape.
type LoginBox interface {
	// GetLogin returns the current login, empty if unauthenticated.
	GetLogin() string
}

// LoginBoxSeeder is the hook StructuredLogger calls once per request to
// create an empty box, stash it on ctx (via whatever key auth uses), and
// return both the updated context and the box. cmd/omnirepo passes an
// adapter around auth.WithLoginBox + &auth.LoginBox{}.
//
// httpx never imports auth, so this callback is the only way the logger
// can coordinate with auth's WithActor-time update without creating an
// import cycle (auth → httpx via validate.go).
type LoginBoxSeeder func(context.Context) (context.Context, LoginBox)

// StructuredLogger returns a chi middleware that emits one slog record per
// request with the baseline attributes (request_id, actor_id, route).
// When seeder is non-nil the middleware seeds a login box on the request
// context; auth middlewares downstream populate it via their WithActor
// helpers, and the log record reads the final value. When seeder is nil
// (tests) actor_id logs as empty.
func StructuredLogger(cfg config.Config, seeder LoginBoxSeeder) func(next http.Handler) http.Handler {
	logger := newLogger(cfg)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			var box LoginBox
			if seeder != nil {
				var ctx context.Context
				ctx, box = seeder(r.Context())
				r = r.WithContext(ctx)
			}
			next.ServeHTTP(ww, r)
			actorID := ""
			if box != nil {
				actorID = box.GetLogin()
			}
			logger.LogAttrs(r.Context(), slog.LevelInfo, "http.request",
				slog.String("request_id", middleware.GetReqID(r.Context())),
				slog.String("actor_id", actorID),
				slog.String("route", r.URL.Path),
				slog.String("method", r.Method),
				slog.Int("status", ww.Status()),
				slog.Int("bytes", ww.BytesWritten()),
				slog.Duration("latency", time.Since(start)),
			)
		})
	}
}

func newLogger(cfg config.Config) *slog.Logger {
	lvl := parseLevel(cfg.Log.Level)
	hopts := &slog.HandlerOptions{Level: lvl}
	if strings.EqualFold(cfg.Log.Format, "text") {
		return slog.New(slog.NewTextHandler(os.Stderr, hopts))
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, hopts))
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
