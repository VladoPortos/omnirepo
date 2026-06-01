// Package api — cursor-based pagination primitives.
//
// All list endpoints use opaque cursor tokens. The cursor encodes a
// (ID, SortValue) tuple as base64-URL JSON so clients never need to
// know the internal sort key. Limit is clamped to [1, MaxPageLimit].
package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
)

// Cursor encodes the last-seen row for keyset pagination. Fields are
// short-named to keep the token compact after base64 encoding.
type Cursor struct {
	ID        int64  `json:"i"`
	SortValue string `json:"s"`
}

// EncodeCursor serialises c as a URL-safe base64 string.
func EncodeCursor(c Cursor) string {
	b, _ := json.Marshal(c)
	return base64.URLEncoding.EncodeToString(b)
}

// DecodeCursor parses an opaque cursor token. Returns an error when the
// token is malformed.
func DecodeCursor(s string) (Cursor, error) {
	b, err := base64.URLEncoding.DecodeString(s)
	if err != nil {
		return Cursor{}, err
	}
	var c Cursor
	return c, json.Unmarshal(b, &c)
}

// PaginationParams captures the (limit, cursor?) pair parsed from the
// request query string.
type PaginationParams struct {
	Limit  int
	Cursor *Cursor
}

const (
	// DefaultPageLimit is the page size when ?limit is absent.
	DefaultPageLimit = 50
	// MaxPageLimit caps the page size to avoid unbounded result sets.
	MaxPageLimit = 200
)

// ParsePaginationParams extracts limit and cursor from r's query string.
// Invalid values are silently replaced with defaults.
func ParsePaginationParams(r *http.Request) PaginationParams {
	p := PaginationParams{Limit: DefaultPageLimit}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= MaxPageLimit {
			p.Limit = n
		}
	}
	if v := r.URL.Query().Get("cursor"); v != "" {
		if c, err := DecodeCursor(v); err == nil {
			p.Cursor = &c
		}
	}
	return p
}
