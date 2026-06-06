package sigv4

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// maxChunkSize caps the declared size of a single STREAMING chunk.
// DoS mitigation — rejects chunks with absurd headers.
const maxChunkSize = 64 * 1024 * 1024 // 64 MiB

// emptySHA256Hex = hex(sha256("")) — referenced by AWS streaming spec as
// the literal "empty hash" line of every per-chunk string-to-sign.
const emptySHA256Hex = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// NewChunkedReader wraps body with a reader that parses AWS
// STREAMING-AWS4-HMAC-SHA256-PAYLOAD chunks, verifies each chunk's signature
// against the chain (seed = Authorization Signature= value), and yields only
// the chunk-data bytes to callers. The terminal zero-size chunk MUST also
// verify successfully; missing or tampered terminal chunks return an error
// from Read (preventing truncation).
//
// The reader is deliberately single-pass and non-seekable.
func NewChunkedReader(body io.Reader, seedSig, scope, amzDate string, kSigning []byte) io.ReadCloser {
	rc, ok := body.(io.ReadCloser)
	if !ok {
		rc = io.NopCloser(body)
	}
	return &chunkedReader{
		src:      bufio.NewReader(body),
		upstream: rc,
		prevSig:  seedSig,
		scope:    scope,
		amzDate:  amzDate,
		kSigning: kSigning,
	}
}

type chunkedReader struct {
	src      *bufio.Reader
	upstream io.ReadCloser // underlying for Close()
	prevSig  string
	scope    string
	amzDate  string
	kSigning []byte

	// pending buffers the current chunk's verified bytes waiting to be
	// emitted to the caller's Read.
	pending bytes.Buffer
	// finished = terminal zero-size chunk consumed and verified.
	finished bool
}

func (c *chunkedReader) Read(p []byte) (int, error) {
	// Drain pending buffer first.
	if c.pending.Len() > 0 {
		return c.pending.Read(p)
	}
	if c.finished {
		return 0, io.EOF
	}
	if err := c.readNextChunk(); err != nil {
		return 0, err
	}
	if c.finished && c.pending.Len() == 0 {
		return 0, io.EOF
	}
	return c.pending.Read(p)
}

func (c *chunkedReader) Close() error { return c.upstream.Close() }

// readNextChunk parses one chunk (header + data + CRLF), verifies signature,
// stashes data into c.pending. A zero-size chunk marks c.finished=true — but
// only after its signature (over the empty body) is verified.
func (c *chunkedReader) readNextChunk() error {
	header, err := readHeaderLine(c.src)
	if err != nil {
		return err
	}
	sizeHex, sigHex, err := parseChunkHeader(header)
	if err != nil {
		return err
	}
	size, err := strconv.ParseInt(sizeHex, 16, 64)
	if err != nil || size < 0 {
		return ErrMalformed
	}
	if size > maxChunkSize {
		return ErrMalformed
	}
	var data []byte
	if size > 0 {
		data = make([]byte, size)
		if _, err := io.ReadFull(c.src, data); err != nil {
			return err
		}
	}
	// Every chunk is followed by \r\n.
	if err := expectCRLF(c.src); err != nil {
		return err
	}

	// Verify per-chunk signature. STS format:
	//   AWS4-HMAC-SHA256-PAYLOAD\n
	//   <amzDate>\n
	//   <scope>\n
	//   <prev-sig>\n
	//   <hex(sha256(""))>\n
	//   <hex(sha256(chunk_bytes))>
	dataHash := sha256.Sum256(data)
	sts := "AWS4-HMAC-SHA256-PAYLOAD\n" +
		c.amzDate + "\n" +
		c.scope + "\n" +
		c.prevSig + "\n" +
		emptySHA256Hex + "\n" +
		hex.EncodeToString(dataHash[:])
	expected := hmacSHA256Hex(c.kSigning, []byte(sts))
	if !constEq(expected, sigHex) {
		return ErrSignatureMismatch
	}
	c.prevSig = sigHex
	if size == 0 {
		// Terminal chunk verified — consume any trailing CRLF tolerantly
		// (RFC says only one \r\n after the zero chunk, but some clients
		// emit an extra).
		swallowOptionalCRLF(c.src)
		c.finished = true
		return nil
	}
	c.pending.Write(data)
	return nil
}

// readHeaderLine reads up to "\r\n" and returns the line without the CRLF.
// Returns io.ErrUnexpectedEOF on truncation.
func readHeaderLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		if err == io.EOF && line == "" {
			return "", io.ErrUnexpectedEOF
		}
		if err != io.EOF {
			return "", err
		}
	}
	line = strings.TrimRight(line, "\n")
	line = strings.TrimRight(line, "\r")
	return line, nil
}

// parseChunkHeader splits "<hex-size>;chunk-signature=<hex>" into its parts.
func parseChunkHeader(line string) (sizeHex, sigHex string, err error) {
	semi := strings.IndexByte(line, ';')
	if semi < 0 {
		return "", "", ErrMalformed
	}
	sizeHex = line[:semi]
	rest := line[semi+1:]
	if !strings.HasPrefix(rest, "chunk-signature=") {
		return "", "", ErrMalformed
	}
	sigHex = strings.TrimPrefix(rest, "chunk-signature=")
	if len(sigHex) != 64 {
		return "", "", ErrMalformed
	}
	return sizeHex, sigHex, nil
}

// expectCRLF reads exactly "\r\n" from r; any deviation returns ErrMalformed.
func expectCRLF(r *bufio.Reader) error {
	b := make([]byte, 2)
	if _, err := io.ReadFull(r, b); err != nil {
		return fmt.Errorf("chunk trailing CRLF: %w", err)
	}
	if b[0] != '\r' || b[1] != '\n' {
		return ErrMalformed
	}
	return nil
}

// swallowOptionalCRLF best-effort: discards a CRLF pair if present.
func swallowOptionalCRLF(r *bufio.Reader) {
	peek, err := r.Peek(2)
	if err != nil {
		return
	}
	if len(peek) == 2 && peek[0] == '\r' && peek[1] == '\n' {
		_, _ = r.Discard(2)
	}
}

// constEq is a constant-time string equality check. Uses ConstantTimeEq for
// length comparison to avoid leaking length information via timing.
func constEq(a, b string) bool {
	if subtle.ConstantTimeEq(int32(len(a)), int32(len(b))) == 0 {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
