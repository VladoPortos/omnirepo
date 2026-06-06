package crypto

import (
	"crypto/sha256"
	"encoding/hex"
)

// SHA256Hex returns the lowercase hex SHA-256 digest of b. One canonical
// copy of the sum-then-encode two-liner previously duplicated across
// packages (TLS fingerprints, RPM repodata checksums).
func SHA256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
