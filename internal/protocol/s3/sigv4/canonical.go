// Package sigv4 implements AWS Signature V4 verification as used by S3 clients.
//
// This package provides the math building blocks (canonical request,
// string-to-sign, signing-key derivation) used by Verify() and the chunked
// STREAMING-AWS4-HMAC-SHA256-PAYLOAD body parser. No network calls; no
// dependency on aws-sdk-go.
//
// Reference: https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_sigv4-create-canonical-request.html
package sigv4

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// canonicalRequest builds the AWS4 canonical-request string.
//
// Format (LF-joined, no trailing newline):
//
//	<HTTP-Method>\n
//	<CanonicalURI>\n
//	<CanonicalQueryString>\n
//	<CanonicalHeaders>\n\n
//	<SignedHeaders>\n
//	<PayloadHashString>
//
// signedHeaders MUST be a lowercase, sorted list of header names; bodyHash is
// either a hex(sha256(payload)) string OR one of the AWS magic strings
// ("UNSIGNED-PAYLOAD", "STREAMING-AWS4-HMAC-SHA256-PAYLOAD").
func canonicalRequest(method, rawPath, rawQuery string, h http.Header, signedHeaders []string, bodyHash string) string {
	var b strings.Builder
	b.WriteString(strings.ToUpper(method))
	b.WriteByte('\n')
	b.WriteString(encodePath(rawPath))
	b.WriteByte('\n')
	b.WriteString(encodeQuery(rawQuery))
	b.WriteByte('\n')
	block, signedList := canonicalHeaders(h, signedHeaders)
	b.WriteString(block)
	b.WriteByte('\n') // extra LF terminating the header block
	b.WriteString(signedList)
	b.WriteByte('\n')
	b.WriteString(bodyHash)
	return b.String()
}

// stringToSign produces the AWS4-HMAC-SHA256 string-to-sign.
//
//	"AWS4-HMAC-SHA256\n<amzDate>\n<scope>\n<hex(sha256(canonReq))>"
func stringToSign(amzDate, scope, canonReq string) string {
	sum := sha256.Sum256([]byte(canonReq))
	var b strings.Builder
	b.WriteString("AWS4-HMAC-SHA256\n")
	b.WriteString(amzDate)
	b.WriteByte('\n')
	b.WriteString(scope)
	b.WriteByte('\n')
	b.WriteString(hex.EncodeToString(sum[:]))
	return b.String()
}

// deriveKey implements the four-step HMAC chain:
//
//	kDate    = HMAC("AWS4"+secret, date)
//	kRegion  = HMAC(kDate,    region)
//	kService = HMAC(kRegion,  service)
//	kSigning = HMAC(kService, "aws4_request")
func deriveKey(secret, date, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), []byte(date))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(service))
	return hmacSHA256(kService, []byte("aws4_request"))
}

// hmacSHA256 returns HMAC-SHA256(key, data).
func hmacSHA256(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}

// hmacSHA256Hex is the hex-encoded variant.
func hmacSHA256Hex(key, data []byte) string {
	return hex.EncodeToString(hmacSHA256(key, data))
}

// encodePath percent-encodes each path segment per RFC 3986 but leaves '/'
// literal. For s3 service, NO double-encoding (AWS SigV4 spec for s3). Empty
// path normalizes to "/".
func encodePath(p string) string {
	if p == "" {
		return "/"
	}
	// Split on '/' and re-encode each segment using RFC-3986 unreserved rules.
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = rfc3986Escape(s, false)
	}
	return strings.Join(segs, "/")
}

// encodeQuery canonicalizes a query string: parse, re-encode keys and values
// per RFC 3986, sort by (encoded key, encoded value), rejoin with '&'. Empty
// returns empty. Does not rely on url.QueryEscape (which emits '+' for space;
// canonical rule is '%20').
func encodeQuery(raw string) string {
	if raw == "" {
		return ""
	}
	type kv struct{ k, v string }
	pairs := make([]kv, 0, 8)
	for _, p := range strings.Split(raw, "&") {
		if p == "" {
			continue
		}
		eq := strings.IndexByte(p, '=')
		var k, v string
		if eq < 0 {
			k = p
		} else {
			k = p[:eq]
			v = p[eq+1:]
		}
		// Parse existing percent-encoding so canonicalization is idempotent.
		kDec, err := url.QueryUnescape(k)
		if err != nil {
			kDec = k
		}
		vDec, err := url.QueryUnescape(v)
		if err != nil {
			vDec = v
		}
		pairs = append(pairs, kv{
			k: rfc3986Escape(kDec, true),
			v: rfc3986Escape(vDec, true),
		})
	}
	sort.SliceStable(pairs, func(i, j int) bool {
		if pairs[i].k != pairs[j].k {
			return pairs[i].k < pairs[j].k
		}
		return pairs[i].v < pairs[j].v
	})
	var b strings.Builder
	for i, p := range pairs {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(p.k)
		b.WriteByte('=')
		b.WriteString(p.v)
	}
	return b.String()
}

// rfc3986Escape encodes a string per RFC 3986 unreserved rules:
//
//	unreserved = ALPHA / DIGIT / "-" / "." / "_" / "~"
//
// Everything else is %-encoded as upper-hex. For canonical-query mode this is
// the exact rule. For path mode this is also the rule (per-segment), since
// '/' is already stripped by the caller before calling us.
func rfc3986Escape(s string, _ bool) string {
	// The _ parameter is reserved for a future "double-encode" flag; s3 uses
	// false, but other services pass true. Kept for API stability.
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isUnreserved(c) {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexChar(c >> 4))
		b.WriteByte(hexChar(c & 0xF))
	}
	return b.String()
}

func isUnreserved(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
		c == '-' || c == '.' || c == '_' || c == '~'
}

func hexChar(n byte) byte {
	if n < 10 {
		return '0' + n
	}
	return 'A' + (n - 10)
}

// canonicalHeaders returns (block, signedList).
//
// block:       one "<lcname>:<trimmed-value>\n" line per signed header, in
//
//	ascending lowercase-name order, with multi-value headers joined
//	by "," (no space) and internal whitespace runs (outside quoted
//	strings) collapsed to a single space.
//
// signedList:  ";"-joined lowercase header names in the same order.
//
// If signed is nil/empty, ALL request headers are used. The caller normally
// passes the "SignedHeaders=" list parsed from the Authorization header.
func canonicalHeaders(h http.Header, signed []string) (block, signedList string) {
	names := make([]string, 0, len(signed))
	for _, n := range signed {
		names = append(names, strings.ToLower(n))
	}
	sort.Strings(names)

	// Build canonical map lookup (lowercase → joined value).
	lc := make(map[string]string, len(h))
	for k, vs := range h {
		lk := strings.ToLower(k)
		joined := make([]string, len(vs))
		for i, v := range vs {
			joined[i] = trimCollapseWS(v)
		}
		lc[lk] = strings.Join(joined, ",")
	}

	var b strings.Builder
	for _, n := range names {
		b.WriteString(n)
		b.WriteByte(':')
		b.WriteString(lc[n])
		b.WriteByte('\n')
	}
	return b.String(), strings.Join(names, ";")
}

// trimCollapseWS trims leading/trailing ASCII whitespace and collapses
// internal runs of spaces/tabs to a single space — EXCEPT inside
// double-quoted substrings, which are preserved verbatim.
func trimCollapseWS(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	inQuote := false
	lastSpace := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '"' {
			inQuote = !inQuote
			b.WriteByte(c)
			lastSpace = false
			continue
		}
		if inQuote {
			b.WriteByte(c)
			lastSpace = false
			continue
		}
		if c == ' ' || c == '\t' {
			if !lastSpace {
				b.WriteByte(' ')
				lastSpace = true
			}
			continue
		}
		b.WriteByte(c)
		lastSpace = false
	}
	return b.String()
}
