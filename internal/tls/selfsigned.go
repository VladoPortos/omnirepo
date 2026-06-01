// Package tls holds OmniRepo's TLS substrate: first-boot self-signed cert
// generation, the atomic-pointer-backed CertHolder used by tls.Config's
// GetCertificate hook for hot-reload, and the admin-upload application
// helper that writes a new cert pair to disk and swaps the holder in one
// call.
//
// Hot-reload is implemented by passing CertHolder.Get as
// tls.Config.GetCertificate; every TLS handshake dereferences the
// atomic.Pointer so an admin upload appears on the NEXT handshake without
// restarting the listener. In-flight connections keep their old cert.
package tls

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"time"
)

// GenerateSelfSigned returns a fresh RSA self-signed cert/key PEM pair for
// use as OmniRepo's first-boot TLS identity. The SAN list always
// contains localhost, 127.0.0.1, and ::1 in addition to the caller-supplied
// hostnames; duplicates are folded and IP-shaped entries land in
// IPAddresses rather than DNSNames.
//
// validity is the NotAfter offset from time.Now(); bits is the RSA modulus
// size (2048 and 4096 are the realistic choices; 4096 is used for
// first boot).
func GenerateSelfSigned(hostnames []string, validity time.Duration, bits int) (certPEM, keyPEM []byte, err error) {
	priv, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		return nil, nil, fmt.Errorf("tls: generate rsa key (%d bits): %w", bits, err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, fmt.Errorf("tls: serial: %w", err)
	}
	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "omnirepo"},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(validity),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  false,
	}

	// Build a merged host list that always includes localhost + loopback IPs.
	seenDNS := map[string]struct{}{}
	seenIP := map[string]struct{}{}
	addHost := func(h string) {
		if h == "" {
			return
		}
		if ip := net.ParseIP(h); ip != nil {
			k := ip.String()
			if _, ok := seenIP[k]; ok {
				return
			}
			seenIP[k] = struct{}{}
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
			return
		}
		if _, ok := seenDNS[h]; ok {
			return
		}
		seenDNS[h] = struct{}{}
		tmpl.DNSNames = append(tmpl.DNSNames, h)
	}
	for _, h := range hostnames {
		addHost(h)
	}
	addHost("localhost")
	addHost("127.0.0.1")
	addHost("::1")

	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		return nil, nil, fmt.Errorf("tls: create certificate: %w", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)})
	return certPEM, keyPEM, nil
}

// Hostname returns os.Hostname() or "localhost" on error. Exposed for the
// app orchestrator which folds the machine hostname into the first-boot SAN
// list.
func Hostname() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "localhost"
}
