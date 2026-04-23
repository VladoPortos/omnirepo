// Package api — admin TLS certificate history endpoint (Phase 05-03, OPS-09).
//
// GET /api/v1/admin/tls/history  — list previously uploaded certificates
// GET /api/v1/admin/tls/current  — info about the currently active TLS cert
package api

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
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

// tlsHistoryEntry matches the frontend TLSHistoryEntry shape (ME-07).
type tlsHistoryEntry struct {
	UploadedAt        string `json:"uploaded_at"`
	UploadedBy        string `json:"uploaded_by"`
	Subject           string `json:"subject"`
	FingerprintSHA256 string `json:"fingerprint_sha256"`
}

// handleTLSHistory recurses into certs/uploaded/<timestamp>/ subdirectories —
// the layout actually produced by tls.ApplyUpload. The previous flat-scan
// version always returned an empty list (ME-07).
func (d Deps) handleTLSHistory(w http.ResponseWriter, r *http.Request) {
	uploadDir := filepath.Join(d.DataRoot, "certs", "uploaded")
	entries, err := os.ReadDir(uploadDir)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusOK, map[string]any{"items": []tlsHistoryEntry{}})
			return
		}
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}

	items := make([]tlsHistoryEntry, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		certPath := filepath.Join(uploadDir, entry.Name(), "server.crt")
		raw, err := os.ReadFile(certPath)
		if err != nil {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		leaf := parseLeaf(raw)
		if leaf == nil {
			continue
		}
		uploadedBy := ""
		if sidecar, err := os.ReadFile(filepath.Join(uploadDir, entry.Name(), "uploaded_by")); err == nil {
			uploadedBy = string(sidecar)
		}
		items = append(items, tlsHistoryEntry{
			UploadedAt:        info.ModTime().UTC().Format(time.RFC3339),
			UploadedBy:        uploadedBy,
			Subject:           leaf.Subject.CommonName,
			FingerprintSHA256: sha256Hex(leaf.Raw),
		})
	}

	// Sort by upload time descending (newest first).
	sort.Slice(items, func(i, j int) bool {
		return items[i].UploadedAt > items[j].UploadedAt
	})

	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// handleTLSCurrent returns the full TLSCertInfo shape the frontend expects —
// including fingerprint_sha256 and source (ME-08).
func (d Deps) handleTLSCurrent(w http.ResponseWriter, r *http.Request) {
	if d.Holder == nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "no TLS holder configured")
		return
	}
	cert := d.Holder.Current()
	if cert == nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "no current certificate")
		return
	}
	if cert.Leaf == nil && len(cert.Certificate) > 0 {
		leaf, err := x509.ParseCertificate(cert.Certificate[0])
		if err == nil {
			cert.Leaf = leaf
		}
	}
	if cert.Leaf == nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "cannot parse leaf")
		return
	}
	// "source" is derived: if an uploaded server.crt exists, the live cert
	// is uploaded; otherwise it's the self-signed bootstrap cert.
	source := "self-signed"
	if _, err := os.Stat(filepath.Join(d.DataRoot, "certs", "server.crt")); err == nil {
		uploadDir := filepath.Join(d.DataRoot, "certs", "uploaded")
		if entries, err := os.ReadDir(uploadDir); err == nil {
			for _, e := range entries {
				if e.IsDir() {
					source = "uploaded"
					break
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"subject":            cert.Leaf.Subject.CommonName,
		"issuer":             cert.Leaf.Issuer.CommonName,
		"not_before":         cert.Leaf.NotBefore.UTC().Format(time.RFC3339),
		"not_after":          cert.Leaf.NotAfter.UTC().Format(time.RFC3339),
		"dns_names":          cert.Leaf.DNSNames,
		"serial":             fmt.Sprintf("%x", cert.Leaf.SerialNumber),
		"fingerprint_sha256": sha256Hex(cert.Leaf.Raw),
		"source":             source,
	})
}

// parseLeaf extracts the first x509 certificate from a PEM block, or nil.
func parseLeaf(pemData []byte) *x509.Certificate {
	block, _ := pem.Decode(pemData)
	if block == nil {
		return nil
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil
	}
	return cert
}

// sha256Hex returns the lowercase-hex SHA-256 digest of raw.
func sha256Hex(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
