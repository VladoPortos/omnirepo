//go:build conformance

package s3conf

// aws-sdk-go-v2 conformance smoke for multipart upload list pagination.
//
// Strategy: create N=7 in-progress multipart uploads in the fixture bucket,
// then drive aws-sdk-go-v2's ListMultipartUploadsPaginator with MaxUploads=3.
// Iterate every page, collect upload IDs into a set, and assert:
//
//   - Total returned upload IDs == 7, with no duplicates across pages.
//   - len(page.Uploads) <= 3 on every page (no oversized response).
//   - At least 3 pages emitted (ceil(7/3) = 3).
//
// The paginator drives KeyMarker + UploadIDMarker round-tripping internally
// (see vendor/.../s3/handwritten_paginators.go:174) — so this is a real
// end-to-end check of the AWS-spec wire markers (NextKeyMarker /
// NextUploadIdMarker / IsTruncated) the server now sets in
// internal/protocol/s3/backend/multipart.go.
//
// Cleanup: every created upload is aborted in t.Cleanup so the fixture
// leaves no orphan multipart rows.

import (
	"context"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestS3MultipartListPagination_Conformance(t *testing.T) {
	fx := bootAppWithS3Bucket(t)
	// retryMax=3 (default) — positive case, normal SDK retry semantics.
	client := NewClient(t, fx.s3Endpoint, fx.akid, fx.secret, true, 3)

	ctx := context.Background()
	const total = 7
	const pageSize int32 = 3

	uploadIDs := make([]string, 0, total)
	for i := 1; i <= total; i++ {
		key := fmt.Sprintf("paginate/k%02d", i)
		out, err := client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
			Bucket: &fx.bucketName,
			Key:    aws.String(key),
		})
		if err != nil {
			t.Fatalf("CreateMultipartUpload k%02d: %v", i, err)
		}
		uploadIDs = append(uploadIDs, aws.ToString(out.UploadId))
	}

	// Cleanup: abort every upload at end of test so we don't leak
	// in-progress multipart rows or staging dirs into other tests.
	t.Cleanup(func() {
		for i, id := range uploadIDs {
			_, _ = client.AbortMultipartUpload(context.Background(), &s3.AbortMultipartUploadInput{
				Bucket:   &fx.bucketName,
				Key:      aws.String(fmt.Sprintf("paginate/k%02d", i+1)),
				UploadId: aws.String(id),
			})
		}
	})

	pag := s3.NewListMultipartUploadsPaginator(client, &s3.ListMultipartUploadsInput{
		Bucket:     &fx.bucketName,
		MaxUploads: aws.Int32(pageSize),
		Prefix:     aws.String("paginate/"),
	})

	seen := make(map[string]bool)
	pages := 0
	for pag.HasMorePages() {
		page, err := pag.NextPage(ctx)
		if err != nil {
			t.Fatalf("NextPage page %d: %v", pages+1, err)
		}
		pages++
		if int32(len(page.Uploads)) > pageSize {
			t.Fatalf("page %d: oversized response — len(Uploads)=%d, want <= %d",
				pages, len(page.Uploads), pageSize)
		}
		for _, u := range page.Uploads {
			id := aws.ToString(u.UploadId)
			if seen[id] {
				t.Fatalf("duplicate UploadId across pages: %s (page %d)", id, pages)
			}
			seen[id] = true
		}
	}
	if len(seen) != total {
		t.Fatalf("expected %d uploads across all pages, got %d", total, len(seen))
	}
	// ceil(7/3) = 3 pages minimum (3, 3, 1).
	if pages < 3 {
		t.Fatalf("expected at least 3 pages for %d uploads at pageSize=%d, got %d",
			total, pageSize, pages)
	}
}
