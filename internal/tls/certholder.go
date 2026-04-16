package tls

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"sync/atomic"
)

// CertHolder owns the currently-live TLS certificate. A tls.Config exposes
// Get as GetCertificate; every handshake loads the atomic pointer — so
// Swap takes effect on the NEXT handshake with zero listener restart and
// zero mutex contention on the fast path.
//
// The zero value is NOT ready: call NewCertHolder. Get returns an explicit
// "no certificate loaded" error when nothing has been swapped in yet; the
// app.Run startup path guarantees an initial Swap before binding listeners.
type CertHolder struct {
	p atomic.Pointer[tls.Certificate]
}

// NewCertHolder returns an empty holder. The first Swap loads a cert; until
// then Get returns an error.
func NewCertHolder() *CertHolder {
	return &CertHolder{}
}

// Get satisfies tls.Config.GetCertificate. The *tls.ClientHelloInfo is
// ignored — OmniRepo does not support SNI-based cert selection; one cert
// covers every hostname in its SAN list.
func (h *CertHolder) Get(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
	c := h.p.Load()
	if c == nil {
		return nil, errors.New("tls: no certificate loaded")
	}
	return c, nil
}

// Swap parses (certPEM, keyPEM), verifies the key matches the leaf cert,
// and atomically stores the parsed pair. Returns an error without mutating
// the holder if either PEM is malformed or the public keys do not match.
func (h *CertHolder) Swap(certPEM, keyPEM []byte) error {
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		// tls.X509KeyPair already reports "public key mismatch" when the
		// cert and key disagree; wrap so callers see the tls: prefix.
		return fmt.Errorf("tls: parse pair: %w", err)
	}
	if len(pair.Certificate) == 0 {
		return errors.New("tls: parse pair: no certificate in chain")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return fmt.Errorf("tls: parse leaf: %w", err)
	}
	pair.Leaf = leaf
	h.p.Store(&pair)
	return nil
}

// Current returns the current certificate or nil if none is loaded. Used by
// the admin TLS status endpoint (Phase 05-03, OPS-09).
func (h *CertHolder) Current() *tls.Certificate {
	return h.p.Load()
}

// Loaded reports whether a certificate is currently live. Used by /readyz.
func (h *CertHolder) Loaded() bool {
	return h.p.Load() != nil
}
