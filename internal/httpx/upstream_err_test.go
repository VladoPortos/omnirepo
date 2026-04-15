package httpx_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/httpx"
)

func TestSanitizeUpstreamErr_Nil(t *testing.T) {
	if got := httpx.SanitizeUpstreamErr(nil); got != nil {
		t.Fatalf("nil in -> nil out, got %v", got)
	}
}

func TestSanitizeUpstreamErr_RedactsBasic(t *testing.T) {
	in := errors.New(`request failed: GET https://example.com Authorization: Basic abc==zzz: 401`)
	out := httpx.SanitizeUpstreamErr(in)
	if out == nil {
		t.Fatal("expected non-nil")
	}
	msg := out.Error()
	if strings.Contains(msg, "abc==") {
		t.Fatalf("credential bytes leaked: %s", msg)
	}
	if !strings.Contains(msg, "Authorization: REDACTED") {
		t.Fatalf("expected REDACTED marker, got: %s", msg)
	}
}

func TestSanitizeUpstreamErr_RedactsBearer(t *testing.T) {
	in := errors.New(`upstream: dial: header "Authorization: Bearer ya29.secret-token" rejected`)
	out := httpx.SanitizeUpstreamErr(in)
	msg := out.Error()
	if strings.Contains(msg, "ya29.secret-token") {
		t.Fatalf("token leaked: %s", msg)
	}
}

func TestSanitizeUpstreamErr_PreservesOtherText(t *testing.T) {
	in := errors.New(`network unreachable: dial tcp 10.0.0.1:443: i/o timeout`)
	out := httpx.SanitizeUpstreamErr(in)
	if out.Error() != in.Error() {
		t.Fatalf("non-auth content was modified: %s", out.Error())
	}
}
