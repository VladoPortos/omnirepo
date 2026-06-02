package main

import (
	"errors"
	"testing"
)

// TestClassifyTxErr pins the bench gate contract: SQLITE_BUSY / "database is
// locked" is always a hard failure (even at shutdown); any other non-busy
// error after the deadline fires is a benign shutdown-race artifact that must
// NOT fail the gate; non-busy errors during the active run still count.
//
// Regression guard for the false-positive that turned CI red: a bare
// "interrupted (9)" SQLite interrupt on an in-flight statement when the 30s
// deadline expired was previously counted as a real error because the old
// guard only matched the substring "context".
func TestClassifyTxErr(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		deadline  bool
		wantCount bool
		wantBusy  bool
	}{
		{"nil during run", nil, false, false, false},
		{"nil at shutdown", nil, true, false, false},
		{"busy during run", errors.New("SQLITE_BUSY: database is busy"), false, true, true},
		{"busy at shutdown still fails", errors.New("metadata: commit: SQLITE_BUSY"), true, true, true},
		{"database-is-locked at shutdown still fails", errors.New("database is locked"), true, true, true},
		{"interrupted(9) at shutdown ignored", errors.New("interrupted (9)"), true, false, false},
		{"context-canceled at shutdown ignored", errors.New("context canceled"), true, false, false},
		{"interrupted(9) during run counts", errors.New("interrupted (9)"), false, true, false},
		{"generic error during run counts", errors.New("constraint failed"), false, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCount, gotBusy := classifyTxErr(tt.err, tt.deadline)
			if gotCount != tt.wantCount || gotBusy != tt.wantBusy {
				t.Errorf("classifyTxErr(%v, deadlinePassed=%v) = (count=%v, busy=%v), want (count=%v, busy=%v)",
					tt.err, tt.deadline, gotCount, gotBusy, tt.wantCount, tt.wantBusy)
			}
		})
	}
}
