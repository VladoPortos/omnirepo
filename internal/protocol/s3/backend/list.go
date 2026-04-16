// Package backend — ListBuckets + ListBucket with AWS-style prefix/delimiter
// pagination. See backend.go for the package doc.
package backend

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/johannesboyne/gofakes3"
)

// ListBuckets returns every non-deleted bucket in the installation.
func (b *Backend) ListBuckets() ([]gofakes3.BucketInfo, error) {
	ctx := context.Background()
	rows, err := b.DB.Reader.QueryContext(ctx,
		`SELECT name, created_at FROM s3_buckets WHERE deleted_at IS NULL ORDER BY name ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("backend: list buckets: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []gofakes3.BucketInfo
	for rows.Next() {
		var name, createdStr string
		if err := rows.Scan(&name, &createdStr); err != nil {
			return nil, fmt.Errorf("backend: scan bucket: %w", err)
		}
		// s3_buckets.created_at is stored as CURRENT_TIMESTAMP ("YYYY-MM-DD HH:MM:SS").
		created, perr := time.Parse("2006-01-02 15:04:05", createdStr)
		if perr != nil {
			// Fallback to RFC3339-ish if a later migration switches format.
			created, _ = time.Parse(time.RFC3339, createdStr)
		}
		out = append(out, gofakes3.BucketInfo{
			Name:         name,
			CreationDate: newContentTime(created),
		})
	}
	return out, rows.Err()
}

// ListBucket paginates objects under a bucket with optional prefix + delimiter.
//
// Strategy: query with prefix + marker (decoded from page.Marker or the
// prior NextContinuationToken) for maxKeys+1 rows. Post-process in memory
// to collapse common prefixes when delimiter is set.
//
// NextContinuationToken round-trip: gofakes3 synthesizes a V2
// NextContinuationToken by base64(URL)-encoding our NextMarker (see
// gofakes3.go line ~292). We therefore set NextMarker to the raw last-key
// and the HTTP layer takes care of the token shape — clients pass it back
// as page.Marker on the next call.
func (b *Backend) ListBucket(name string, prefix *gofakes3.Prefix, page gofakes3.ListBucketPage) (*gofakes3.ObjectList, error) {
	ctx := context.Background()
	id, ok, err := b.findBucketID(ctx, name)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, gofakes3.BucketNotFound(name)
	}

	p := gofakes3.Prefix{}
	if prefix != nil {
		p = *prefix
	}

	maxKeys := int(page.MaxKeys)
	if maxKeys <= 0 || maxKeys > 1000 {
		maxKeys = 1000
	}

	// Decode marker — accept both the raw key form (AWS Marker) and our
	// base64-encoded NextContinuationToken form.
	marker := page.Marker
	if decoded, err := base64.RawURLEncoding.DecodeString(marker); err == nil && len(decoded) > 0 {
		marker = string(decoded)
	}

	out := gofakes3.NewObjectList()

	// We loop — when delimiter collapse reduces user-visible entries, we may
	// need additional DB pages to reach maxKeys.
	pageSize := maxKeys + 1
	added := 0
	lastSeenKey := ""

	for {
		listPage, err := b.Objects.ListByBucket(ctx, id, p.Prefix, marker, pageSize)
		if err != nil {
			return nil, fmt.Errorf("backend: list page: %w", err)
		}
		if len(listPage.Objects) == 0 {
			break
		}
		for _, obj := range listPage.Objects {
			lastSeenKey = obj.Key
			// Apply prefix.Match so delimiter collapsing kicks in.
			var m gofakes3.PrefixMatch
			if !p.Match(obj.Key, &m) {
				continue
			}
			if m.CommonPrefix {
				before := len(out.CommonPrefixes)
				out.AddPrefix(m.MatchedPart)
				if len(out.CommonPrefixes) > before {
					added++
				}
				if added >= maxKeys {
					break
				}
				continue
			}
			hash, _ := hexDecodeETag(obj.ETag)
			_ = hash
			out.Add(&gofakes3.Content{
				Key:          obj.Key,
				LastModified: newContentTime(obj.CreatedAt),
				ETag:         `"` + obj.ETag + `"`,
				Size:         obj.SizeBytes,
				StorageClass: gofakes3.StorageStandard,
			})
			added++
			if added >= maxKeys {
				break
			}
		}
		if added >= maxKeys {
			// Detect whether more remains past our current cursor.
			remaining := listPage.IsTruncated
			if !remaining {
				// There were untouched objects still in this page (e.g.
				// collapsed into prefixes that already existed).
				for i := range listPage.Objects {
					if listPage.Objects[i].Key > lastSeenKey {
						remaining = true
						break
					}
				}
			}
			if remaining || listPage.IsTruncated {
				out.IsTruncated = true
				out.NextMarker = base64.RawURLEncoding.EncodeToString([]byte(lastSeenKey))
			}
			break
		}
		if !listPage.IsTruncated {
			break
		}
		// More pages needed — advance the DB cursor.
		marker = listPage.NextToken
	}

	_ = strings.Compare // keep import used
	return out, nil
}

func hexDecodeETag(etag string) ([]byte, error) {
	// retained for future use; currently unused in hot path.
	if etag == "" {
		return nil, nil
	}
	return nil, nil
}
