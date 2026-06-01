package jobs

import "io"

// CountingReader wraps an io.Reader and fires OnRead(n) for every NON-empty
// read. 0-byte reads (which can occur alongside io.EOF or transient states
// on HTTP response bodies and tar/gzip tail readers) explicitly do NOT fire
// the callback — this prevents spurious progress.Set(...) emits from the
// hot-loop wrapping in protocol sync handlers.
//
// Concurrency: single-goroutine. Callers should wrap one body per
// goroutine. Thread-unsafe by design (matches protocol handler shape:
// each per-artifact download in the APT/RPM/PyPI/Helm/OCI sync loop owns
// one reader).
type CountingReader struct {
	R      io.Reader
	OnRead func(n int)
}

// Read implements io.Reader. OnRead fires only when n > 0 — even if err
// is non-nil (io.Copy treats (n>0, err) as a valid partial read so we
// must still account for those bytes).
func (c *CountingReader) Read(p []byte) (int, error) {
	n, err := c.R.Read(p)
	if n > 0 && c.OnRead != nil {
		c.OnRead(n)
	}
	return n, err
}
