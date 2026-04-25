package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/dxc-internal/omnirepo/internal/metadata"
)

// Event is the in-memory shape audit callers construct and hand to Record.
// ActorUserID, ActorAPIKeyID, and ActorS3KeyID are pointers so unauthenticated
// events (e.g. auth.login.failure) can omit them — they land as NULL in the
// audit_log table, which is the documented behaviour (D-33). Exactly one of
// the three is non-nil for a successfully authenticated actor; all three are
// nil for anonymous events.
type Event struct {
	OccurredAt    time.Time      `json:"occurred_at"`
	ActorUserID   *int64         `json:"actor_user_id,omitempty"`
	ActorAPIKeyID *int64         `json:"actor_api_key_id,omitempty"`
	ActorS3KeyID  *int64         `json:"actor_s3_key_id,omitempty"`
	IP            string         `json:"ip,omitempty"`
	UserAgent     string         `json:"user_agent,omitempty"`
	Kind          EventKind      `json:"kind"`
	TargetKind    string         `json:"target_kind,omitempty"`
	TargetID      string         `json:"target_id,omitempty"`
	Outcome       string         `json:"outcome,omitempty"`
	Details       map[string]any `json:"details,omitempty"`
}

// DBTimestampLayout is the canonical on-disk representation for any
// time-valued column where the application writes via bound parameters
// (as opposed to SQLite's CURRENT_TIMESTAMP default). Fixed-width,
// UTC, with 9-digit nanoseconds — chosen so that:
//   - SQLite's datetime() / strftime() parse it natively.
//   - Lexicographic ordering (as used by ORDER BY, keyset cursors, and
//     range filters) matches chronological ordering byte-for-byte.
//   - Scan(*time.Time) round-trips via modernc.org/sqlite's parser.
//
// See F-04.2: time.RFC3339Nano strips trailing zeros → variable width
// → "Z" (0x5A) > "." (0x2E) for a zero-ns row vs a sub-second row in
// the same second, breaking lex order. Fixed width avoids that.
const DBTimestampLayout = "2006-01-02T15:04:05.000000000Z07:00"

// Logger writes audit events to both the audit_log DB table and the NDJSON
// mirror file. The DB write is strict (errors bubble); the NDJSON write is
// best-effort (logs slog.Warn on failure). See OQ-9.
type Logger interface {
	Record(ctx context.Context, e Event) error
}

// New constructs a Logger backed by db and an NDJSON Writer at ndjsonPath with
// the given per-file size cap (MiB) and history-file count.
func New(db *metadata.DB, ndjsonPath string, maxSizeMiB, keep int) (Logger, error) {
	w, err := NewWriter(ndjsonPath, maxSizeMiB, keep)
	if err != nil {
		return nil, err
	}
	return &logger{db: db, writer: w}, nil
}

type logger struct {
	db     *metadata.DB
	writer *Writer
}

// Record persists e. OQ-9 semantics:
//   - DB insert is strict — any driver error is returned to the caller.
//   - NDJSON mirror is best-effort — encode/write errors are logged at WARN
//     and swallowed so a disk-full on the log file cannot mask a successful
//     state change.
func (l *logger) Record(ctx context.Context, e Event) error {
	if e.OccurredAt.IsZero() {
		e.OccurredAt = time.Now().UTC()
	}
	details, err := json.Marshal(e.Details)
	if err != nil || len(details) == 0 {
		details = []byte("{}")
	}
	// Strict DB insert.
	if err := l.db.WriteTx(ctx, func(tx *sql.Tx) error {
		// Format occurred_at as fixed-width ISO-8601 UTC with 9-digit
		// nanoseconds so lex order == chronological order. modernc.org/sqlite's
		// default time.Time path stores Go's .String() format which SQLite
		// can't parse; time.RFC3339Nano is SQLite-parseable but strips
		// trailing zeros (variable width), so "...:05Z" lex-sorts AFTER
		// "...:05.1Z" (Z=0x5A > .=0x2E). Fixed-width avoids both traps.
		// Admin audit filter + keyset pagination bind in the SAME format
		// (see internal/api/admin_audit.go).
		_, exErr := tx.ExecContext(ctx, `
			INSERT INTO audit_log(
				occurred_at, actor_user_id, actor_api_key_id, actor_s3_key_id,
				ip, user_agent,
				event_kind, target_kind, target_id, outcome, details_json
			) VALUES (?,?,?,?,?,?,?,?,?,?,?)
		`,
			e.OccurredAt.UTC().Format(DBTimestampLayout),
			nullableInt64(e.ActorUserID),
			nullableInt64(e.ActorAPIKeyID),
			nullableInt64(e.ActorS3KeyID),
			e.IP,
			e.UserAgent,
			string(e.Kind),
			e.TargetKind,
			e.TargetID,
			e.Outcome,
			string(details),
		)
		return exErr
	}); err != nil {
		return fmt.Errorf("audit: db insert: %w", err)
	}
	// Best-effort NDJSON mirror.
	if err := l.writer.WriteJSON(e); err != nil {
		slog.WarnContext(ctx, "audit ndjson write failed", "err", err, "kind", string(e.Kind))
	}
	return nil
}

func nullableInt64(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}
