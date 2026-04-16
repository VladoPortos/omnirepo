// Package pktline implements the minimum Git pkt-line helpers OmniRepo needs
// outside the go-git transport primitives — specifically, the band-3 (error)
// sideband-64k packet used by the push-size-cap middleware (Plan 10, D-34).
//
// References:
//   - Git Documentation/technical/protocol-capabilities.txt §side-band,side-band-64k
//   - Git Documentation/technical/pack-protocol.txt §Packet Line Format
package pktline

import (
	"fmt"
	"io"
)

// WriteSidebandError emits a band-3 (error) sideband-64k packet.
//
// Wire format: "<4-hex-len>\x03<msg>" where <4-hex-len> is the total packet
// length (including the 4-byte header and the 1-byte band marker) as a
// lowercase 4-digit hex ASCII number.
//
// Intentionally emits NO trailing newline — callers can embed one in msg if
// their message format requires it (git CLI prints the message as-is with
// "remote: " prepended).
func WriteSidebandError(w io.Writer, msg string) (int, error) {
	payload := make([]byte, 0, 1+len(msg))
	payload = append(payload, 0x03) // band 3 = error
	payload = append(payload, msg...)

	hdr := fmt.Sprintf("%04x", 4+len(payload))
	n1, err := io.WriteString(w, hdr)
	if err != nil {
		return n1, err
	}
	n2, err := w.Write(payload)
	return n1 + n2, err
}
