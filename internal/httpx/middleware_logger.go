package httpx

import (
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/dxc-internal/omnirepo/internal/config"
)

// StructuredLogger returns a chi middleware that emits one slog record per
// request with the D-43 baseline attributes (request_id, actor_id, route).
// Phase 1 leaves actor_id empty; Phase 2 auth middleware fills it in.
func StructuredLogger(cfg config.Config) func(next http.Handler) http.Handler {
	logger := newLogger(cfg)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			logger.LogAttrs(r.Context(), slog.LevelInfo, "http.request",
				slog.String("request_id", middleware.GetReqID(r.Context())),
				slog.String("actor_id", ""),
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
