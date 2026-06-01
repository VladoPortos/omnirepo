// Package s3keys wires the project-scoped S3 access-key row onto the SigV4
// verifier and the auth.Can dispatch.
//
//   - GenerateS3AccessKey mints an AWS-compatible AKID ("AKIA" + 16 base32 chars)
//     and a 40-char base64url secret (30 random bytes).
//   - Service.Lookup satisfies sigv4.SecretLookup: it decrypts the AEAD-sealed
//     secret and collapses missing/revoked/undecryptable-rows into
//     sigv4.ErrInvalidAccessKeyId (no-oracle).
//   - Service.Lookup fires TouchLastUsed in a non-blocking goroutine.
//   - Service.ResolveProject returns the project_id pinned to an AKID so
//     callers can cross-check against bucket.project_id.
//
// This package's public surface is stable and intentionally narrow: the
// SigV4 middleware just wires `s3keys.Service.Lookup` into the sigv4 verifier
// and maps the returned project id + AKID onto an auth.Actor.
package s3keys

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"log/slog"
	"time"

	omrcrypto "github.com/vladoportos/omnirepo/internal/crypto"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/protocol/s3/sigv4"
)

// touchLastUsedTimeout caps the background TouchLastUsed write so it cannot
// outlive the request that spawned it by more than this budget.
const touchLastUsedTimeout = 2 * time.Second

// GenerateS3AccessKey returns a new AKID/secret pair.
//
//	akid   = "AKIA" + 16 upper-case base32 chars (matches AWS ^AKIA[A-Z0-9]{16}$
//	         after a-z→A-Z — we pick base32's A-Z2-7 alphabet which is a strict
//	         subset, so SDKs that validate the AWS regex accept it unchanged).
//	secret = 40 base64url chars, 30 random bytes encoded with
//	         base64.RawURLEncoding (no padding).
//
// Two successive calls are guaranteed distinct with overwhelming probability
// (80 bits of AKID entropy, 240 bits of secret entropy).
func GenerateS3AccessKey() (akid, secret string, err error) {
	var raw [10]byte // 10*8 = 80 bits → 16 base32 chars without padding
	if _, err = rand.Read(raw[:]); err != nil {
		return "", "", err
	}
	akid = "AKIA" + base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw[:])

	var sec [30]byte // 30*8 = 240 bits → 40 base64url chars without padding
	if _, err = rand.Read(sec[:]); err != nil {
		return "", "", err
	}
	secret = base64.RawURLEncoding.EncodeToString(sec[:])
	return akid, secret, nil
}

// Service is the SigV4 bridge: given an AKID it returns the plaintext secret
// and (via ResolveProject) the pinned project_id. Use NewService to construct;
// the zero value is not usable.
type Service struct {
	Repo *metadata.S3KeysRepo
	AEAD *omrcrypto.AEAD
}

// NewService constructs a Service. repo and aead MUST be non-nil.
func NewService(repo *metadata.S3KeysRepo, aead *omrcrypto.AEAD) *Service {
	return &Service{Repo: repo, AEAD: aead}
}

// LookupResult holds the AKID's row id, secret, and project_id from a single
// DB lookup so callers don't need to query twice for the same AKID. ID is the
// s3_access_keys.id primary key — the multipart-upload path reads it for
// attribution (replaces the hardcoded user-id 1 fallback).
type LookupResult struct {
	ID        int64
	Secret    string
	ProjectID int64
}

// LookupFull retrieves the secret and project_id for an AKID in one DB query.
// On success it fires a goroutine to bump last_used_at.
func (s *Service) LookupFull(akid string) (*LookupResult, error) {
	ctx := context.Background()
	row, err := s.Repo.FindByAKID(ctx, akid)
	if err != nil {
		if !errors.Is(err, metadata.ErrS3AccessKeyNotFound) {
			slog.Warn("s3keys.lookup: driver error", "err", err)
		}
		return nil, sigv4.ErrInvalidAccessKeyId
	}

	plaintext, derr := s.AEAD.Decrypt(string(row.SecretEnc))
	if derr != nil {
		slog.Warn("s3keys.lookup: aead decrypt failed",
			"akid", akid, "err", derr)
		return nil, sigv4.ErrInvalidAccessKeyId
	}

	// Bound the fire-and-forget touch with a short timeout so it cannot
	// accumulate forever under load or hang on shutdown.
	go func(akid string) {
		tctx, cancel := context.WithTimeout(context.Background(), touchLastUsedTimeout)
		defer cancel()
		if err := s.Repo.TouchLastUsed(tctx, akid); err != nil {
			slog.Warn("s3keys.touch_last_used", "akid", akid, "err", err)
		}
	}(akid)

	return &LookupResult{
		ID:        row.ID,
		Secret:    string(plaintext),
		ProjectID: row.ProjectID,
	}, nil
}

// Lookup satisfies sigv4.SecretLookup — returns only the secret.
func (s *Service) Lookup(akid string) (string, error) {
	r, err := s.LookupFull(akid)
	if err != nil {
		return "", err
	}
	return r.Secret, nil
}

// ResolveProject returns the project_id an AKID is pinned to.
// Uses a fresh DB query (needed when called separately from Lookup).
func (s *Service) ResolveProject(akid string) (int64, error) {
	r, err := s.LookupFull(akid)
	if err != nil {
		return 0, err
	}
	return r.ProjectID, nil
}

// Compile-time assertion: Service.Lookup matches sigv4.SecretLookup.
var _ sigv4.SecretLookup = (*Service)(nil).Lookup
