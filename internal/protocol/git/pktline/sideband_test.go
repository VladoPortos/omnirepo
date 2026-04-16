package pktline_test

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/protocol/git/pktline"
)

func TestWriteSidebandError(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	msg := "error: push exceeds repo limit of 500 MiB; contact a project admin"
	n, err := pktline.WriteSidebandError(&buf, msg)
	if err != nil {
		t.Fatalf("WriteSidebandError: %v", err)
	}
	// Expected wire format: "<4-hex-len>\x03<msg>"
	payloadLen := 1 + len(msg) // band byte + msg bytes
	hdr := fmt.Sprintf("%04x", 4+payloadLen)
	expected := append([]byte(hdr), 0x03)
	expected = append(expected, []byte(msg)...)
	if !bytes.Equal(buf.Bytes(), expected) {
		t.Fatalf("sideband bytes mismatch:\n got  %q\n want %q", buf.Bytes(), expected)
	}
	if n != len(expected) {
		t.Fatalf("n = %d, want %d", n, len(expected))
	}
	// Sanity: band byte MUST be 0x03 at offset 4.
	if buf.Bytes()[4] != 0x03 {
		t.Fatalf("band byte at offset 4 = 0x%02x, want 0x03", buf.Bytes()[4])
	}
}

func TestWriteSidebandErrorShort(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if _, err := pktline.WriteSidebandError(&buf, "x"); err != nil {
		t.Fatal(err)
	}
	// 4-hex-len = 0006 ("0006\x03x" is 6 bytes total)
	if got := buf.String(); got != "0006\x03x" {
		t.Fatalf("got %q, want %q", got, "0006\x03x")
	}
}
