package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

// Writer is a size-rotating NDJSON writer. When the active file (at path)
// would exceed maxBytes after a write, the writer rolls:
//   - drops audit.log.<keep> (if any),
//   - shifts audit.log.<N> -> audit.log.<N+1> for N in keep-1..1,
//   - renames audit.log -> audit.log.1,
//   - opens a new audit.log and resumes.
//
// WriteJSON appends one JSON-encoded line under a mutex so concurrent callers
// are safe. All writes go to the currently-open os.File; rotation swaps the
// underlying FD atomically under the same lock.
type Writer struct {
	mu        sync.Mutex
	path      string
	maxBytes  int64
	keep      int
	currentSz int64
	f         *os.File
}

// NewWriter opens (or creates) path as the active audit log. maxSizeMiB sets
// the per-file size cap; keep is the number of historical .1..N files to
// retain before dropping the oldest.
func NewWriter(path string, maxSizeMiB, keep int) (*Writer, error) {
	return newWriterWithMaxBytes(path, int64(maxSizeMiB)*1024*1024, keep)
}

func newWriterWithMaxBytes(path string, maxBytes int64, keep int) (*Writer, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("audit ndjson: mkdir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return nil, fmt.Errorf("audit ndjson: open: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("audit ndjson: stat: %w", err)
	}
	if maxBytes <= 0 {
		maxBytes = 100 * 1024 * 1024
	}
	if keep < 0 {
		keep = 0
	}
	return &Writer{
		path:      path,
		maxBytes:  maxBytes,
		keep:      keep,
		currentSz: info.Size(),
		f:         f,
	}, nil
}

// WriteJSON encodes v and appends one line (with trailing newline). Rotation
// happens before the append when the additional bytes would exceed maxBytes.
func (w *Writer) WriteJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("audit ndjson: marshal: %w", err)
	}
	b = append(b, '\n')

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.currentSz > 0 && w.currentSz+int64(len(b)) > w.maxBytes {
		if err := w.rotate(); err != nil {
			return fmt.Errorf("audit ndjson: rotate: %w", err)
		}
	}
	n, err := w.f.Write(b)
	if err != nil {
		return fmt.Errorf("audit ndjson: write: %w", err)
	}
	w.currentSz += int64(n)
	return nil
}

// rotate shifts history files, moves the base file to .1, and opens a fresh
// base. Caller must hold w.mu.
//
// Crash-safety: the NEW base file is opened to a temp path FIRST so a
// failure anywhere in the shift/rename sequence leaves w.f pointing at the
// still-valid OLD file handle. Only after the new file is successfully in
// place does rotate swap w.f and close the old one. On any error the old
// file stays active and the writer is never stuck in a "file already
// closed" state.
func (w *Writer) rotate() error {
	// 1. Open the NEW base file to a temp path first. Failure here leaves
	// w.f (the old file) untouched.
	tmpNew := w.path + ".rotating"
	// Pre-clean any leftover from a previous crashed rotate.
	_ = os.Remove(tmpNew)
	newF, err := os.OpenFile(tmpNew, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}

	// 2. Best-effort shift of history files (.N -> .N+1).
	if w.keep > 0 {
		_ = os.Remove(w.path + "." + strconv.Itoa(w.keep))
	}
	for i := w.keep - 1; i >= 1; i-- {
		from := w.path + "." + strconv.Itoa(i)
		to := w.path + "." + strconv.Itoa(i+1)
		_ = os.Rename(from, to) // missing file is fine
	}

	// 3. Move the current base aside (to .1) or drop it when keep=0.
	if w.keep > 0 {
		if err := os.Rename(w.path, w.path+".1"); err != nil && !os.IsNotExist(err) {
			// Rollback: drop the fresh-new file and leave the old w.f
			// active (it is still open for writes).
			_ = newF.Close()
			_ = os.Remove(tmpNew)
			return err
		}
	} else {
		// keep=0: remove the old base so the rename below can take its place.
		_ = os.Remove(w.path)
	}

	// 4. Promote the new file into place.
	if err := os.Rename(tmpNew, w.path); err != nil {
		// Very unlikely: we own the parent dir. If it happens, try to
		// restore the base from .1 so audit keeps working.
		_ = newF.Close()
		_ = os.Remove(tmpNew)
		if w.keep > 0 {
			_ = os.Rename(w.path+".1", w.path)
		}
		return err
	}

	// 5. Swap pointer, close old FD. Old writes already queued on oldF
	// flush naturally when we close it; any concurrent WriteJSON caller is
	// blocked on w.mu so no write races the swap.
	oldF := w.f
	w.f = newF
	w.currentSz = 0
	if oldF != nil {
		_ = oldF.Close()
	}
	return nil
}

// Close flushes and closes the underlying file.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}
