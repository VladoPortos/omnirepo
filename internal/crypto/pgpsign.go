// Package crypto — pgpsign generates RSA-4096 OpenPGP keys and produces
// clearsigned / detached armored signatures for APT InRelease and RPM
// repomd.xml.asc. Private key material is accepted only as the armored
// string argument to ClearSign / DetachSign; callers must not log or
// persist that argument in plaintext — the caller's AEAD-at-rest posture
// (signing_keys.private_enc) is the source of truth.
//
// Error messages never echo key material; wrapping uses
// fmt.Errorf("pgpsign: <op>: %w", err).
package crypto

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/clearsign"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

// GenerateRepoKey creates a new RSA OpenPGP entity and returns:
//   - armored ASCII-private-key block (to be handed directly to
//     SigningKeysRepo.Insert — never log or persist as plaintext)
//   - armored ASCII-public-key block (served via /public-key.asc)
//   - canonical hex fingerprint (uppercase, no spaces)
//
// uid is the OpenPGP uid string (e.g. "<project>/<repo>
// <install-id>@omnirepo"). bits is the RSA size; callers pass
// config.Signing.GPGKeyBits (default 4096 per D-35). Values below 2048
// are rejected.
func GenerateRepoKey(uid string, bits int) (privArmored, pubArmored, fingerprint string, err error) {
	if bits < 2048 {
		return "", "", "", fmt.Errorf("pgpsign: rsa bits %d < 2048", bits)
	}
	if uid == "" {
		return "", "", "", errors.New("pgpsign: empty uid")
	}
	cfg := &packet.Config{RSABits: bits, Algorithm: packet.PubKeyAlgoRSA}
	entity, err := openpgp.NewEntity(uid, "omnirepo signing key", "", cfg)
	if err != nil {
		return "", "", "", fmt.Errorf("pgpsign: generate: %w", err)
	}

	var privBuf bytes.Buffer
	aw, err := armor.Encode(&privBuf, openpgp.PrivateKeyType, nil)
	if err != nil {
		return "", "", "", fmt.Errorf("pgpsign: private armor: %w", err)
	}
	if err := entity.SerializePrivate(aw, cfg); err != nil {
		return "", "", "", fmt.Errorf("pgpsign: serialize private: %w", err)
	}
	if err := aw.Close(); err != nil {
		return "", "", "", fmt.Errorf("pgpsign: close private armor: %w", err)
	}

	var pubBuf bytes.Buffer
	aw2, err := armor.Encode(&pubBuf, openpgp.PublicKeyType, nil)
	if err != nil {
		return "", "", "", fmt.Errorf("pgpsign: public armor: %w", err)
	}
	if err := entity.Serialize(aw2); err != nil {
		return "", "", "", fmt.Errorf("pgpsign: serialize public: %w", err)
	}
	if err := aw2.Close(); err != nil {
		return "", "", "", fmt.Errorf("pgpsign: close public armor: %w", err)
	}

	fp := strings.ToUpper(hex.EncodeToString(entity.PrimaryKey.Fingerprint[:]))
	return privBuf.String(), pubBuf.String(), fp, nil
}

// ClearSign wraps body in an OpenPGP clearsigned message suitable for
// APT InRelease. The returned bytes are the full clearsigned payload
// (BEGIN PGP SIGNED MESSAGE ... END PGP SIGNATURE) and can be served
// verbatim. privArmored must be the armored private key block returned
// from GenerateRepoKey (or LookupPrivate); it must not be encrypted by
// a passphrase in v1.
func ClearSign(privArmored string, body []byte) ([]byte, error) {
	entity, err := readArmoredEntity(privArmored)
	if err != nil {
		return nil, fmt.Errorf("pgpsign: clearsign read key: %w", err)
	}
	var out bytes.Buffer
	plaintext, err := clearsign.Encode(&out, entity.PrivateKey, nil)
	if err != nil {
		return nil, fmt.Errorf("pgpsign: clearsign encode: %w", err)
	}
	if _, err := plaintext.Write(body); err != nil {
		return nil, fmt.Errorf("pgpsign: clearsign write: %w", err)
	}
	if err := plaintext.Close(); err != nil {
		return nil, fmt.Errorf("pgpsign: clearsign close: %w", err)
	}
	return out.Bytes(), nil
}

// DetachSign returns an ASCII-armored detached signature over body
// (used for repomd.xml.asc and Release.gpg).
func DetachSign(privArmored string, body []byte) ([]byte, error) {
	entity, err := readArmoredEntity(privArmored)
	if err != nil {
		return nil, fmt.Errorf("pgpsign: detachsign read key: %w", err)
	}
	var out bytes.Buffer
	if err := openpgp.ArmoredDetachSign(&out, entity, bytes.NewReader(body), nil); err != nil {
		return nil, fmt.Errorf("pgpsign: detachsign: %w", err)
	}
	return out.Bytes(), nil
}

// readArmoredEntity parses an armored private key block and returns the
// first entity. Error messages never include key bytes.
func readArmoredEntity(privArmored string) (*openpgp.Entity, error) {
	el, err := openpgp.ReadArmoredKeyRing(strings.NewReader(privArmored))
	if err != nil {
		return nil, fmt.Errorf("read armored keyring: %w", err)
	}
	if len(el) == 0 {
		return nil, errors.New("no entities in armored block")
	}
	return el[0], nil
}
