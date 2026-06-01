package migrations

import (
	"context"
	"database/sql"
	"io/fs"
)

// ApplyFSForTest exposes the unexported applyFS core so runner_test.go can
// drive the runner with synthetic migration sets (fstest.MapFS). Test-only.
func ApplyFSForTest(ctx context.Context, writer *sql.DB, source fs.FS) ([]string, error) {
	return applyFS(ctx, writer, source)
}
