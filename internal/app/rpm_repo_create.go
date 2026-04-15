// Package app — RPM/DEB repo-create hook (Phase 3 Plan 04, D-02).
//
// At repo-create time for type ∈ {rpm, deb} we synchronously generate an
// RSA-4096 OpenPGP keypair via pgpsign.GenerateRepoKey and insert the
// signing_keys row in the SAME writer tx as the repos row. Both INSERTs
// commit atomically — a key-gen failure rolls back the repos INSERT
// (T-03-04-06).
package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	omrcrypto "github.com/dxc-internal/omnirepo/internal/crypto"
	"github.com/dxc-internal/omnirepo/internal/metadata"
)

// SigningKeyGenerator abstracts pgpsign.GenerateRepoKey so tests can inject
// a fake (deterministic key, error injection). Production uses
// pgpsign.GenerateRepoKey directly.
type SigningKeyGenerator func(uid string, bits int) (privArmored, pubArmored, fingerprint string, err error)

// DefaultSigningKeyGenerator is the production GenerateRepoKey wrapper.
var DefaultSigningKeyGenerator SigningKeyGenerator = omrcrypto.GenerateRepoKey

// CreateRPMRepoHook is invoked inside the repo-create writer tx for
// type ∈ {rpm, deb}. Generates the keypair synchronously and inserts the
// signing_keys row using the supplied tx so failure rolls back the repos
// INSERT. Returns the fingerprint for the API response and audit details.
//
// repoType outside {rpm, deb} → no-op return ("", nil).
//
// gen may be nil; defaults to DefaultSigningKeyGenerator.
func CreateRPMRepoHook(
	ctx context.Context,
	tx *sql.Tx,
	repoID int64,
	repoType, projectName, repoName string,
	signingKeys *metadata.SigningKeysRepo,
	gpgKeyBits int,
	gen SigningKeyGenerator,
) (fingerprint string, err error) {
	if repoType != "rpm" && repoType != "deb" {
		return "", nil
	}
	if signingKeys == nil {
		return "", errors.New("signing keys repo not configured")
	}
	if gen == nil {
		gen = DefaultSigningKeyGenerator
	}
	if gpgKeyBits <= 0 {
		gpgKeyBits = 4096
	}
	// OpenPGP UID parser rejects "/" — use "-" as the project/repo separator.
	uid := fmt.Sprintf("%s-%s-omnirepo", projectName, repoName)
	priv, pub, fp, err := gen(uid, gpgKeyBits)
	if err != nil {
		return "", fmt.Errorf("signing_key generate: %w", err)
	}
	if _, err := signingKeys.Insert(ctx, tx, repoID, pub, priv, fp); err != nil {
		return "", fmt.Errorf("signing_key insert: %w", err)
	}
	// Best-effort drop of the in-memory plaintext private key reference.
	priv = ""
	_ = priv
	return fp, nil
}
