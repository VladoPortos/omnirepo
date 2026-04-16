// Package s3 wires the S3-compatible HTTP surface: virtual-host rewrite,
// SigV4 verification middleware, auth.Can bucket access checks, and the
// gofakes3 Server mount. Plan 04-07.
package s3

import (
	"context"
	"net"
	"net/http"
	"strings"
)

// originalPathKey stashes the pre-rewrite URL path so SigV4 verification
// computes the canonical request against the path the client actually signed
// (virtual-host clients sign "/" + key, not "/s3/<bucket>/" + key).
type originalPathKey struct{}

// OriginalPath returns the path the request had before VHostRewrite. If the
// request was never rewritten, it returns r.URL.Path unchanged.
func OriginalPath(r *http.Request) string {
	if v, ok := r.Context().Value(originalPathKey{}).(string); ok {
		return v
	}
	return r.URL.Path
}

// VHostRewrite returns chi middleware that rewrites virtual-host-style S3
// requests into path-style. If Host is "<bucket>.<external-hostname>", the
// URL path is rewritten to "/s3/<bucket>/<original-path>". Requests that
// already have a "/s3/" prefix or whose Host is an IP address are left
// untouched.
//
// hostnames is the list of configured external hostnames (e.g.
// ["omnirepo.corp.example"]). Each is lowercased and prefixed with "." for
// suffix matching.
func VHostRewrite(hostnames []string) func(http.Handler) http.Handler {
	suffixes := make([]string, len(hostnames))
	for i, h := range hostnames {
		suffixes[i] = "." + strings.ToLower(h)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Path-style requests already carry /s3/ — skip rewrite.
			if strings.HasPrefix(r.URL.Path, "/s3/") {
				next.ServeHTTP(w, r)
				return
			}

			host, _, err := net.SplitHostPort(r.Host)
			if err != nil {
				// No port in Host header.
				host = r.Host
			}
			host = strings.ToLower(host)

			// IPv4/v6 literals are never bucket names.
			if net.ParseIP(host) != nil {
				next.ServeHTTP(w, r)
				return
			}

			for _, sfx := range suffixes {
				if strings.HasSuffix(host, sfx) {
					bucket := strings.TrimSuffix(host, sfx)
					if isValidBucketName(bucket) {
						// Save original path so SigV4 verifier uses the
						// path the client actually signed.
						ctx := context.WithValue(r.Context(), originalPathKey{}, r.URL.Path)
						r = r.WithContext(ctx)
						r.URL.Path = "/s3/" + bucket + r.URL.Path
					}
					break
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// isValidBucketName is a lightweight check for the VHost rewrite. It
// validates per D-06 (AWS-compatible subset). The canonical validator
// lives in backend.validateBucketName; this is a shared helper to avoid
// an import cycle.
func isValidBucketName(name string) bool {
	if len(name) < 3 || len(name) > 63 {
		return false
	}
	for i, c := range name {
		switch {
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '.' || c == '-':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	// No consecutive dots.
	if strings.Contains(name, "..") {
		return false
	}
	// Not an IPv4 literal.
	if net.ParseIP(name) != nil {
		return false
	}
	return true
}
