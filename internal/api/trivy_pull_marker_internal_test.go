package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTrivyPullStatusPreservesAttemptTimestamp(t *testing.T) {
	started := time.Date(2026, 9, 7, 10, 0, 0, 123456789, time.UTC)
	pullJob.mu.Lock()
	old := pullJob.startedAt
	pullJob.startedAt = started
	pullJob.mu.Unlock()
	defer func() { pullJob.mu.Lock(); pullJob.startedAt = old; pullJob.mu.Unlock() }()
	w := httptest.NewRecorder()
	(Deps{}).handleTrivyDBPullStatus(w, httptest.NewRequest("GET", "/", nil))
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["started_at"] != started.Format(time.RFC3339Nano) {
		t.Fatalf("attempt identity lost precision: %v", body["started_at"])
	}
}
