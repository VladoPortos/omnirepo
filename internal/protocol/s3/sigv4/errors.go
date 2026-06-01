package sigv4

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// Sentinel errors returned by Verify(). Callers render these into AWS-shape
// XML error bodies via WriteError(). Keeping plain sentinels (not typed
// structs) means "missing" and "revoked" access-keys collapse to the same
// ErrInvalidAccessKeyId value — removing the timing/error oracle.
var (
	ErrMalformed          = errors.New("sigv4: authorization header malformed")
	ErrInvalidAccessKeyId = errors.New("sigv4: invalid access key id")
	ErrSignatureMismatch  = errors.New("sigv4: signature mismatch")
	ErrInvalidRequest     = errors.New("sigv4: invalid request")

	// ErrContentSHA256Mismatch is returned by the chi-side PutObject
	// intercept when the streamed body's sha256 differs from the value the
	// client signed in x-amz-content-sha256.
	// Maps to AWS S3's `XAmzContentSHA256Mismatch` 400 envelope.
	ErrContentSHA256Mismatch = errors.New("sigv4: x-amz-content-sha256 mismatch")
)

// ErrSkew is returned when the request's x-amz-date is farther from server
// time than the allowed skew window (±15 min for S3). WriteError()
// renders it as a RequestTimeTooSkewed XML response echoing ServerTime in the
// 20060102T150405Z literal format.
type ErrSkew struct {
	RequestTime    time.Time
	ServerTime     time.Time
	MaxAllowedSkew time.Duration
}

func (e *ErrSkew) Error() string {
	return fmt.Sprintf("sigv4: request time skew exceeds %s (request=%s, server=%s)",
		e.MaxAllowedSkew, e.RequestTime.UTC().Format(amzTimeFmt),
		e.ServerTime.UTC().Format(amzTimeFmt))
}

// amzTimeFmt is the AWS SigV4 wire timestamp format.
const amzTimeFmt = "20060102T150405Z"

// awsError mirrors the AWS S3 XML error envelope. Fields marked `omitempty`
// are only serialized when the underlying error carries them (e.g.
// ErrSkew times).
type awsError struct {
	XMLName             xml.Name `xml:"Error"`
	Code                string   `xml:"Code"`
	Message             string   `xml:"Message"`
	RequestID           string   `xml:"RequestId,omitempty"`
	RequestTime         string   `xml:"RequestTime,omitempty"`
	ServerTime          string   `xml:"ServerTime,omitempty"`
	MaxAllowedSkewMilli int64    `xml:"MaxAllowedSkewMilliseconds,omitempty"`
}

// WriteError writes an AWS-shape XML error body for err. The dispatch:
//
//	*ErrSkew              → 403 RequestTimeTooSkewed (+RequestTime/ServerTime/MaxAllowedSkewMilliseconds)
//	ErrInvalidAccessKeyId → 403 InvalidAccessKeyId
//	ErrSignatureMismatch  → 403 SignatureDoesNotMatch
//	ErrMalformed          → 400 AuthorizationHeaderMalformed
//	ErrInvalidRequest     → 400 InvalidRequest
//	<anything else>       → 500 InternalError (does NOT leak err.Error())
//
// Always writes the XML prolog and sets Content-Type: application/xml.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, msg := mapError(err)

	body := awsError{
		Code:      code,
		Message:   msg,
		RequestID: newRequestID(),
	}

	var se *ErrSkew
	if errors.As(err, &se) {
		body.RequestTime = se.RequestTime.UTC().Format(amzTimeFmt)
		body.ServerTime = se.ServerTime.UTC().Format(amzTimeFmt)
		skew := se.MaxAllowedSkew
		if skew == 0 {
			skew = 15 * time.Minute
		}
		body.MaxAllowedSkewMilli = skew.Milliseconds()
	}

	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("x-amz-request-id", body.RequestID)
	w.WriteHeader(status)
	_, _ = w.Write([]byte(xml.Header))
	enc := xml.NewEncoder(w)
	_ = enc.Encode(body)
	_ = enc.Flush()
}

func mapError(err error) (status int, code, msg string) {
	var se *ErrSkew
	switch {
	case errors.As(err, &se):
		return http.StatusForbidden, "RequestTimeTooSkewed",
			"The difference between the request time and the current time is too large."
	case errors.Is(err, ErrInvalidAccessKeyId):
		return http.StatusForbidden, "InvalidAccessKeyId",
			"The AWS access key Id you provided does not exist in our records."
	case errors.Is(err, ErrSignatureMismatch):
		return http.StatusForbidden, "SignatureDoesNotMatch",
			"The request signature we calculated does not match the signature you provided."
	case errors.Is(err, ErrMalformed):
		return http.StatusBadRequest, "AuthorizationHeaderMalformed",
			"The authorization header provided is not valid."
	case errors.Is(err, ErrInvalidRequest):
		return http.StatusBadRequest, "InvalidRequest",
			"The request is invalid for this service."
	case errors.Is(err, ErrContentSHA256Mismatch):
		return http.StatusBadRequest, "XAmzContentSHA256Mismatch",
			"The provided 'x-amz-content-sha256' header does not match what was computed."
	default:
		return http.StatusInternalServerError, "InternalError",
			"We encountered an internal error. Please try again."
	}
}

// newRequestID returns 16 hex characters from crypto/rand. Opaque to clients;
// included for operator log-correlation.
func newRequestID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
