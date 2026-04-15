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

// rotate closes the current file, shifts .N -> .N+1, moves the base file to
// .1, and opens a fresh base. Caller must hold w.mu.
func (w *Writer) rotate() error {
	if err := w.f.Close(); err != nil {
		return err
	}
	// Drop oldest, if present.
	if w.keep > 0 {
		_ = os.Remove(w.path + "." + strconv.Itoa(w.keep))
	}
	// Shift .N -> .N+1 for N = keep-1 .. 1
	for i := w.keep - 1; i >= 1; i-- {
		from := w.path + "." + strconv.Itoa(i)
		to := w.path + "." + strconv.Itoa(i+1)
		_ = os.Rename(from, to) // missing file is fine
	}
	// Move current base to .1 (only if keep > 0).
	if w.keep > 0 {
		if err := os.Rename(w.path, w.path+".1"); err != nil && !os.IsNotExist(err) {
			return err
		}
	} else {
		// keep=0: truncate current by removing.
		_ = os.Remove(w.path)
	}
	f, err := os.OpenFile(w.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	w.f = f
	w.currentSz = 0
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
