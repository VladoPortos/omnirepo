// Package api — admin TLS certificate history endpoint (Phase 05-03, OPS-09).
//
// GET /api/v1/admin/tls/history  — list previously uploaded certificates
// GET /api/v1/admin/tls/current  — info about the currently active TLS cert
package api

import (
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/auth"
	authmw "github.com/dxc-internal/omnirepo/internal/auth/middleware"
)

// mountAdminTLSHistory installs TLS history endpoints on r.
func (d Deps) mountAdminTLSHistory(r chi.Router) {
	r.With(authmw.RequireCan(auth.ActionUploadTLSCert)).
		Get("/admin/tls/history", d.handleTLSHistory)
	r.With(authmw.RequireCan(auth.ActionUploadTLSCert)).
		Get("/admin/tls/current", d.handleTLSCurrent)
}

type tlsCertInfo struct {
	Filename  string `json:"filename"`
	Subject   string `json:"subject"`
	Issuer    string `json:"issuer"`
	NotBefore string `json:"not_before"`
	NotAfter  string `json:"not_after"`
	UploadedAt string `json:"uploaded_at,omitempty"`
}

func (d Deps) handleTLSHistory(w http.ResponseWriter, r *http.Request) {
	uploadDir := filepath.Join(d.DataRoot, "certs", "uploaded")
	entries, err := os.ReadDir(uploadDir)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusOK, map[string]any{"items": []any{}})
			return
		}
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
		return
	}

	var items []tlsCertInfo
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if filepath.Ext(name) != ".crt" && filepath.Ext(name) != ".pem" {
			continue
		}
		certPath := filepath.Join(uploadDir, name)
		raw, err := os.ReadFile(certPath)
		if err != nil {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		ci := parseCertInfo(raw)
		ci.Filename = name
		ci.UploadedAt = info.ModTime().UTC().Format(time.RFC3339)
		items = append(items, ci)
	}

	// Sort by upload time descending.
	sort.Slice(items, func(i, j int) bool {
		return items[i].UploadedAt > items[j].UploadedAt
	})

	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (d Deps) handleTLSCurrent(w http.ResponseWriter, r *http.Request) {
	if d.Holder == nil {
		writeJSONError(w, http.StatusNotFound, ErrNotFound, "no TLS holder configured")
		return
	}
	cert := d.Holder.Current()
	if cert == nil {
		writeJSONError(w, http.StatusNotFound, ErrNotFound, "no current certificate")
		return
	}
	// cert is *tls.Certificate — parse the leaf.
	if cert.Leaf == nil && len(cert.Certificate) > 0 {
		leaf, err := x509.ParseCertificate(cert.Certificate[0])
		if err == nil {
			cert.Leaf = leaf
		}
	}
	if cert.Leaf == nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "cannot parse leaf")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"subject":    cert.Leaf.Subject.CommonName,
		"issuer":     cert.Leaf.Issuer.CommonName,
		"not_before": cert.Leaf.NotBefore.UTC().Format(time.RFC3339),
		"not_after":  cert.Leaf.NotAfter.UTC().Format(time.RFC3339),
		"dns_names":  cert.Leaf.DNSNames,
	})
}

// parseCertInfo extracts subject/issuer/validity from PEM-encoded cert data.
// Returns a partial tlsCertInfo (Filename and UploadedAt set by caller).
func parseCertInfo(pemData []byte) tlsCertInfo {
	block, _ := pem.Decode(pemData)
	if block == nil {
		return tlsCertInfo{Subject: "unknown", Issuer: "unknown"}
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return tlsCertInfo{Subject: "parse error", Issuer: "parse error"}
	}
	return tlsCertInfo{
		Subject:   cert.Subject.CommonName,
		Issuer:    cert.Issuer.CommonName,
		NotBefore: cert.NotBefore.UTC().Format(time.RFC3339),
		NotAfter:  cert.NotAfter.UTC().Format(time.RFC3339),
	}
}
