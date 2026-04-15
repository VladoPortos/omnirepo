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
// ActorUserID and ActorAPIKeyID are pointers so unauthenticated events (e.g.
// auth.login.failure) can omit them — they land as NULL in the audit_log
// table, which is the documented behaviour (D-33).
type Event struct {
	OccurredAt    time.Time      `json:"occurred_at"`
	ActorUserID   *int64         `json:"actor_user_id,omitempty"`
	ActorAPIKeyID *int64         `json:"actor_api_key_id,omitempty"`
	IP            string         `json:"ip,omitempty"`
	UserAgent     string         `json:"user_agent,omitempty"`
	Kind          EventKind      `json:"kind"`
	TargetKind    string         `json:"target_kind,omitempty"`
	TargetID      string         `json:"target_id,omitempty"`
	Outcome       string         `json:"outcome,omitempty"`
	Details       map[string]any `json:"details,omitempty"`
}

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
		_, exErr := tx.ExecContext(ctx, `
			INSERT INTO audit_log(
				occurred_at, actor_user_id, actor_api_key_id, ip, user_agent,
				event_kind, target_kind, target_id, outcome, details_json
			) VALUES (?,?,?,?,?,?,?,?,?,?)
		`,
			e.OccurredAt,
			nullableInt64(e.ActorUserID),
			nullableInt64(e.ActorAPIKeyID),
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
