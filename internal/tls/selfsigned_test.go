package tls

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net"
	"testing"
	"time"
)

func parseCert(t *testing.T, pemBytes []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		t.Fatalf("pem decode: nil block")
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	return c
}

func TestGenerateSelfSigned_BasicFields(t *testing.T) {
	certPEM, keyPEM, err := GenerateSelfSigned(
		[]string{"omnirepo.example.com", "artifacts.example.com", "myhost", "localhost"},
		2*365*24*time.Hour,
		2048, // keep tests fast; bit-size is asserted elsewhere separately
	)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(certPEM) == 0 || len(keyPEM) == 0 {
		t.Fatalf("empty PEM output")
	}
	c := parseCert(t, certPEM)
	if c.IsCA {
		t.Fatalf("expected IsCA=false")
	}
	if c.Subject.CommonName == "" {
		t.Fatalf("empty CN")
	}
	expect := time.Now().Add(2 * 365 * 24 * time.Hour)
	diff := c.NotAfter.Sub(expect)
	if diff < -24*time.Hour || diff > 24*time.Hour {
		t.Fatalf("NotAfter off by %v", diff)
	}
}

func TestGenerateSelfSigned_RSABitsHonored(t *testing.T) {
	_, keyPEM, err := GenerateSelfSigned([]string{"h"}, time.Hour, 4096)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		t.Fatalf("decode key")
	}
	priv, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		t.Fatalf("parse key: %v", err)
	}
	if _, ok := any(priv).(*rsa.PrivateKey); !ok {
		t.Fatalf("expected RSA key")
	}
	if priv.N.BitLen() != 4096 {
		t.Fatalf("expected 4096 bits, got %d", priv.N.BitLen())
	}
}

func TestSelfSignedSANContainsLocalhostAndIPs(t *testing.T) {
	certPEM, _, err := GenerateSelfSigned(
		[]string{"omnirepo.example.com", "artifacts.example.com", "myhost"},
		time.Hour, 2048,
	)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	c := parseCert(t, certPEM)

	dnsExpected := map[string]bool{
		"omnirepo.example.com":  false,
		"artifacts.example.com": false,
		"myhost":                false,
		"localhost":             false,
	}
	for _, d := range c.DNSNames {
		if _, ok := dnsExpected[d]; ok {
			dnsExpected[d] = true
		}
	}
	for d, ok := range dnsExpected {
		if !ok {
			t.Errorf("missing DNS SAN: %s (got %v)", d, c.DNSNames)
		}
	}
	haveV4, haveV6 := false, false
	for _, ip := range c.IPAddresses {
		if ip.Equal(net.IPv4(127, 0, 0, 1)) {
			haveV4 = true
		}
		if ip.Equal(net.ParseIP("::1")) {
			haveV6 = true
		}
	}
	if !haveV4 {
		t.Errorf("missing 127.0.0.1 in IPAddresses: %v", c.IPAddresses)
	}
	if !haveV6 {
		t.Errorf("missing ::1 in IPAddresses: %v", c.IPAddresses)
	}
}
