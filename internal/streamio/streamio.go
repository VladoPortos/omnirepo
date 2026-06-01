// Package streamio provides bounded-read helpers that fail explicitly on
// over-limit upstream input rather than silently truncating (the bug class
// this package was created to close — audit findings #4 and #5).
//
// The previous idiom across mirror-sync handlers and upstream-parse
// helpers was:
//
//	body, err := io.ReadAll(io.LimitReader(r, max))
//
// which silently returns exactly `max` bytes whenever the upstream
// produced max+N bytes — the truncation is undetectable by the caller.
// ReadAllLimited reads max+1 bytes via io.LimitReader and converts the
// over-cap signal into an explicit error sentinel.
package streamio

import (
	"errors"
	"fmt"
	"io"
)

// ErrArtifactTooLarge is the canonical sentinel for upstream artifact
// bodies that exceed the configured cap. Mirror-sync handlers in
// rpm/deb/pypi/helm pass this to ReadAllLimited.
var ErrArtifactTooLarge = errors.New("streamio: artifact exceeds configured size limit")

// ErrMetadataTooLarge is the canonical sentinel for upstream metadata
// (repomd.xml, primary.xml, Packages, simple/index pages, helm
// index.yaml) that exceeds the configured cap.
var ErrMetadataTooLarge = errors.New("streamio: metadata exceeds configured size limit")

// ReadAllLimited reads up to max bytes from r. If r yields more than
// max bytes, it returns overLimitErr (typically ErrArtifactTooLarge or
// ErrMetadataTooLarge — callers may also pass a wrapped sentinel).
//
// Contract:
//   - max must be > 0; max <= 0 returns an error (defense-in-depth gate
//     so the helper never silently allows unlimited reads).
//   - The implementation reads max+1 bytes via io.LimitReader. If the
//     buffer length exceeds max after the read, the upstream had at
//     least max+1 bytes total and overLimitErr is returned.
//   - On any other I/O error from r, returns nil and the wrapped error.
//   - Successful reads (<=max bytes followed by EOF) return the data
//     and nil.
//   - If overLimitErr is nil, ErrArtifactTooLarge is used as the
//     default (callers should pass an explicit sentinel; this is a
//     last-line safety net so a forgotten arg never turns into a
//     silently-allowed over-limit body).
func ReadAllLimited(r io.Reader, max int64, overLimitErr error) ([]byte, error) {
	if max <= 0 {
		return nil, fmt.Errorf("streamio: max must be > 0 (got %d)", max)
	}
	if overLimitErr == nil {
		overLimitErr = ErrArtifactTooLarge
	}
	// Read max+1 bytes. If the (max+1)-th byte arrives, the upstream
	// exceeded the cap; LimitReader stops there so we never drain
	// arbitrary bytes from a malicious or runaway upstream.
	//
	// Hand-rolled read loop (not io.ReadAll) so we can detect a hostile
	// reader that returns (0, nil) repeatedly. Per io.Reader contract,
	// (0, nil) is legal but discouraged — io.ReadAll would spin on
	// such a reader. We give up after maxNoProgressReads consecutive
	// zero-byte non-EOF reads.
	const maxNoProgressReads = 16
	lr := io.LimitReader(r, max+1)
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	noProgress := 0
	for {
		n, rerr := lr.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			noProgress = 0
		} else if rerr == nil {
			noProgress++
			if noProgress >= maxNoProgressReads {
				return nil, fmt.Errorf("streamio: reader stalled (returned 0,nil %d times)", maxNoProgressReads)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return nil, fmt.Errorf("streamio: read: %w", rerr)
		}
	}
	if int64(len(buf)) > max {
		return nil, overLimitErr
	}
	return buf, nil
}
