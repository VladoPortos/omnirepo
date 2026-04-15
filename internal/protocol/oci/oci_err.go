package oci

import (
	"encoding/json"
	"net/http"
)

// OCI Distribution error codes (spec §errors). The subset below covers
// everything the skeleton (plan 02-05) and the downstream 02-06/02-07
// handlers emit.
const (
	ErrCodeUnauthorized     = "UNAUTHORIZED"
	ErrCodeDenied           = "DENIED"
	ErrCodeUnsupported      = "UNSUPPORTED"
	ErrCodeUnknown          = "UNKNOWN"
	ErrCodeNameUnknown      = "NAME_UNKNOWN"
	ErrCodeManifestUnk      = "MANIFEST_UNKNOWN"
	ErrCodeManifestInvalid  = "MANIFEST_INVALID"
	ErrCodeDigestInvalid    = "DIGEST_INVALID"
	ErrCodeBlobUnknown      = "BLOB_UNKNOWN"
	ErrCodeNameInvalid      = "NAME_INVALID"
	ErrCodeSizeInvalid      = "SIZE_INVALID"
	ErrCodeTagInvalid       = "TAG_INVALID"
	// ErrCodeBlobUploadInvalid is returned when a client-supplied upload
	// session identifier is malformed (e.g., not a UUID; WR-02).
	ErrCodeBlobUploadInvalid = "BLOB_UPLOAD_INVALID"
)

// ociError mirrors the spec envelope.
type ociError struct {
	Errors []ociErrorDetail `json:"errors"`
}

type ociErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
}

// writeOCIErr writes an OCI-spec error envelope with the given HTTP status.
// err may be nil; when non-nil, its Error() text goes in "detail". Message
// is a short human-readable paraphrase of the code. Never logs the incoming
// Authorization header value or the JWT bytes.
func writeOCIErr(w http.ResponseWriter, status int, code string, err error) {
	msg := code
	switch code {
	case ErrCodeUnauthorized:
		msg = "authentication required"
	case ErrCodeDenied:
		msg = "requested access to the resource is denied"
	case ErrCodeUnsupported:
		msg = "the operation is unsupported"
	case ErrCodeManifestUnk:
		msg = "manifest unknown"
	case ErrCodeDigestInvalid:
		msg = "provided digest did not match uploaded content"
	case ErrCodeBlobUnknown:
		msg = "blob unknown to registry"
	case ErrCodeNameUnknown:
		msg = "repository name not known to registry"
	case ErrCodeNameInvalid:
		msg = "invalid repository name"
	case ErrCodeSizeInvalid:
		msg = "provided length did not match content length"
	case ErrCodeManifestInvalid:
		msg = "manifest invalid"
	case ErrCodeTagInvalid:
		msg = "manifest tag did not match URI"
	}
	detail := ""
	if err != nil {
		detail = err.Error()
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ociError{
		Errors: []ociErrorDetail{{Code: code, Message: msg, Detail: detail}},
	})
}
