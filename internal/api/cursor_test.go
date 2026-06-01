package api

import (
	"net/http"
	"net/url"
	"testing"
)

func TestCursor_RoundTrip(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		c    Cursor
	}{
		{"zero", Cursor{}},
		{"basic", Cursor{ID: 42, SortValue: "2026-01-01"}},
		{"negative_id", Cursor{ID: -1, SortValue: "abc"}},
		{"large_id", Cursor{ID: 1<<52 - 1, SortValue: "xyz"}},
		{"empty_sort", Cursor{ID: 99, SortValue: ""}},
		{"unicode_sort", Cursor{ID: 7, SortValue: "Bonjour, le monde"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			encoded := EncodeCursor(tt.c)
			decoded, err := DecodeCursor(encoded)
			if err != nil {
				t.Fatalf("decode error: %v", err)
			}
			if decoded.ID != tt.c.ID {
				t.Fatalf("ID: got %d want %d", decoded.ID, tt.c.ID)
			}
			if decoded.SortValue != tt.c.SortValue {
				t.Fatalf("SortValue: got %q want %q", decoded.SortValue, tt.c.SortValue)
			}
		})
	}
}

func TestDecodeCursor_InvalidBase64(t *testing.T) {
	t.Parallel()
	_, err := DecodeCursor("!!!not-base64!!!")
	if err == nil {
		t.Fatal("expected error for invalid base64")
	}
}

func TestDecodeCursor_InvalidJSON(t *testing.T) {
	t.Parallel()
	// Valid base64 but not valid JSON.
	_, err := DecodeCursor("bm90LWpzb24=") // "not-json" in base64
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestParsePaginationParams_Defaults(t *testing.T) {
	t.Parallel()
	r := &http.Request{URL: &url.URL{RawQuery: ""}}
	p := ParsePaginationParams(r)
	if p.Limit != DefaultPageLimit {
		t.Fatalf("limit: got %d want %d", p.Limit, DefaultPageLimit)
	}
	if p.Cursor != nil {
		t.Fatal("cursor should be nil for empty query")
	}
}

func TestParsePaginationParams_LimitClamping(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		query     string
		wantLimit int
	}{
		{"valid", "limit=10", 10},
		{"zero_falls_to_default", "limit=0", DefaultPageLimit},
		{"negative_falls_to_default", "limit=-5", DefaultPageLimit},
		{"over_max_falls_to_default", "limit=999", DefaultPageLimit},
		{"at_max", "limit=200", 200},
		{"at_one", "limit=1", 1},
		{"non_numeric_falls_to_default", "limit=abc", DefaultPageLimit},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := &http.Request{URL: &url.URL{RawQuery: tt.query}}
			p := ParsePaginationParams(r)
			if p.Limit != tt.wantLimit {
				t.Fatalf("limit: got %d want %d", p.Limit, tt.wantLimit)
			}
		})
	}
}

func TestParsePaginationParams_CursorParsing(t *testing.T) {
	t.Parallel()
	// Encode a valid cursor, put it in the query string.
	c := Cursor{ID: 77, SortValue: "2026-04-16T00:00:00Z"}
	tok := EncodeCursor(c)
	r := &http.Request{URL: &url.URL{RawQuery: "cursor=" + tok}}
	p := ParsePaginationParams(r)
	if p.Cursor == nil {
		t.Fatal("cursor should not be nil")
	}
	if p.Cursor.ID != 77 {
		t.Fatalf("cursor ID: got %d want 77", p.Cursor.ID)
	}
	if p.Cursor.SortValue != "2026-04-16T00:00:00Z" {
		t.Fatalf("cursor SortValue: got %q", p.Cursor.SortValue)
	}
}

func TestParsePaginationParams_BadCursorIgnored(t *testing.T) {
	t.Parallel()
	r := &http.Request{URL: &url.URL{RawQuery: "cursor=INVALID"}}
	p := ParsePaginationParams(r)
	if p.Cursor != nil {
		t.Fatal("bad cursor should be silently ignored")
	}
}
