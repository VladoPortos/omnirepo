package middleware

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/vladoportos/omnirepo/internal/auth"
	"github.com/vladoportos/omnirepo/internal/httperr"
)

type attemptPermitContextKey struct{}

// PasswordAttemptThrottle rate-limits Basic password requests before the
// expensive password verifier. API-key-shaped Basic credentials bypass it.
func PasswordAttemptThrottle(limiter *auth.AttemptLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			login, password, ok := r.BasicAuth()
			if !ok || isBasicAPIKey(login, password) {
				next.ServeHTTP(w, r)
				return
			}
			permit, retry, allowed := limiter.Acquire(r.RemoteAddr)
			if !allowed {
				writeAuthRateLimited(w, r, retry)
				return
			}
			defer permit.Release()
			ctx := context.WithValue(r.Context(), attemptPermitContextKey{}, permit)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func isBasicAPIKey(login, password string) bool {
	if auth.APIKeyRegex.MatchString(password) {
		return true
	}
	if login != "project" {
		return false
	}
	parts := strings.SplitN(password, ":", 2)
	return len(parts) == 2 && parts[0] != "" && auth.APIKeyRegex.MatchString(parts[1])
}

func releaseAttemptPermit(ctx context.Context) {
	if permit, ok := ctx.Value(attemptPermitContextKey{}).(*auth.AttemptPermit); ok {
		permit.Release()
	}
}

// WriteAuthRateLimited emits the canonical REST envelope used by login and
// the global Basic-auth throttle.
func WriteAuthRateLimited(w http.ResponseWriter, r *http.Request, retry time.Duration) {
	writeAuthRateLimited(w, r, retry)
}

func writeAuthRateLimited(w http.ResponseWriter, r *http.Request, retry time.Duration) {
	seconds := int((retry + time.Second - 1) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	httperr.Write(w, r, httperr.Transient(
		"auth.rate_limited",
		"Too many authentication attempts. Try again later.",
		seconds*1000,
		httperr.WithStatus(http.StatusTooManyRequests),
	))
}
