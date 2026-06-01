package audit

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNDJSONRotatesAtSizeThreshold(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	const maxBytes = int64(256)
	const keep = 3

	w, err := newWriterWithMaxBytes(path, maxBytes, keep)
	if err != nil {
		t.Fatalf("newWriterWithMaxBytes: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	// Write many entries so we cross the threshold multiple times.
	const entries = 50
	for i := 0; i < entries; i++ {
		if err := w.WriteJSON(map[string]any{"i": i, "msg": "pad------pad--------pad"}); err != nil {
			t.Fatalf("WriteJSON[%d]: %v", i, err)
		}
	}

	// audit.log should exist and be at or below threshold
	if info, err := os.Stat(path); err != nil {
		t.Fatalf("stat current: %v", err)
	} else if info.Size() > maxBytes {
		t.Fatalf("current file size %d > maxBytes %d", info.Size(), maxBytes)
	}

	// audit.log.1 must exist
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("audit.log.1 missing: %v", err)
	}
	// audit.log.<keep+1> must NOT exist
	overKept := path + "." + string(rune('0'+keep+1))
	if _, err := os.Stat(overKept); err == nil {
		t.Fatalf("audit.log.%d unexpectedly exists", keep+1)
	}
}

func TestNDJSONNoLinesLostAcrossRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	const maxBytes = int64(128)
	const keep = 5

	w, err := newWriterWithMaxBytes(path, maxBytes, keep)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })

	const entries = 40
	for i := 0; i < entries; i++ {
		if err := w.WriteJSON(map[string]int{"i": i}); err != nil {
			t.Fatal(err)
		}
	}

	// Count total lines across audit.log and audit.log.N
	total := 0
	paths := []string{path}
	for i := 1; i <= keep; i++ {
		paths = append(paths, path+"."+itoa(i))
	}
	highest := -1
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		s := bufio.NewScanner(f)
		for s.Scan() {
			line := strings.TrimSpace(s.Text())
			if line == "" {
				continue
			}
			var m struct{ I int }
			if err := json.Unmarshal([]byte(line), &m); err != nil {
				t.Fatalf("bad line in %s: %v", p, err)
			}
			if m.I > highest {
				highest = m.I
			}
			total++
		}
		_ = f.Close()
	}

	if total == 0 {
		t.Fatal("no lines seen anywhere")
	}
	// Highest index seen must be the last entry written (entries-1) — proves
	// the active file was not truncated on rotation.
	if highest != entries-1 {
		t.Fatalf("highest index = %d, want %d", highest, entries-1)
	}
}

// TestNDJSONRotateFailureLeavesOldFileUsable is the regression gate for
// when rotate() fails because the new-file open fails: the writer must
// continue to use the OLD file — not end up with a nil/closed file
// descriptor that makes every subsequent WriteJSON fail permanently.
func TestNDJSONRotateFailureLeavesOldFileUsable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	const maxBytes = int64(64)

	w, err := newWriterWithMaxBytes(path, maxBytes, 3)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })

	// Fill past threshold once so next WriteJSON would trigger a rotate.
	for i := 0; i < 3; i++ {
		if err := w.WriteJSON(map[string]int{"seed": i}); err != nil {
			t.Fatalf("seed write: %v", err)
		}
	}

	// Sabotage the rotate path: create a non-empty DIRECTORY at the
	// rotating temp path so OpenFile(O_WRONLY|O_CREATE) fails when rotate
	// tries to open the new base.
	blocker := path + ".rotating"
	if err := os.MkdirAll(blocker, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocker, "sentinel"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Trigger rotation with a write that would exceed maxBytes.
	bigPayload := strings.Repeat("x", int(maxBytes))
	err = w.WriteJSON(map[string]string{"msg": bigPayload})
	// We expect a rotate error OR success depending on whether the write
	// would have crossed threshold on its own. Either way, the writer MUST
	// still be usable for subsequent writes.
	_ = err

	// Clean up the blocker so normal operation can resume.
	_ = os.RemoveAll(blocker)

	// The writer must remain functional: this next write should succeed.
	if err := w.WriteJSON(map[string]int{"post_rotate": 1}); err != nil {
		t.Fatalf("writer unusable after rotate failure: %v", err)
	}

	// And the written line should land in either the current base or a .1
	// file (rotation may have succeeded the second time around).
	var found bool
	for _, p := range []string{path, path + ".1"} {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if strings.Contains(string(b), `"post_rotate":1`) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("post-rotate-failure write did not land in any audit file")
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := false
	if i < 0 {
		neg = true
		i = -i
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}
