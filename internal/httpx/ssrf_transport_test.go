package httpx

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
)

func TestValidateOutboundHost_RejectsNonPublicAddressLiterals(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "10.0.0.8", "172.16.1.1", "192.168.1.1",
		"169.254.169.254", "0.0.0.0", "224.0.0.1", "::1", "fc00::1", "fe80::1",
	}
	for _, host := range blocked {
		if err := ValidateOutboundHost(host); !errors.Is(err, ErrOutboundAddressBlocked) {
			t.Errorf("ValidateOutboundHost(%q)=%v want ErrOutboundAddressBlocked", host, err)
		}
	}
	for _, host := range []string{"93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946", "registry.example.com"} {
		if err := ValidateOutboundHost(host); err != nil {
			t.Errorf("ValidateOutboundHost(%q)=%v want nil", host, err)
		}
	}
}

func TestSafeTransport_BlocksPrivateDNSBeforeRoundTrip(t *testing.T) {
	baseCalled := false
	base := roundTripperFunc(func(*http.Request) (*http.Response, error) {
		baseCalled = true
		return nil, errors.New("base called")
	})
	transport := newSafeRoundTripper(base, func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("10.20.30.40")}}, nil
	})
	req, err := http.NewRequest(http.MethodGet, "https://registry.internal.example/v2/", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = transport.RoundTrip(req)
	if !errors.Is(err, ErrOutboundAddressBlocked) {
		t.Fatalf("RoundTrip error=%v want ErrOutboundAddressBlocked", err)
	}
	if baseCalled {
		t.Fatal("base transport called for blocked destination")
	}
}

func TestSafeTransport_BlocksLoopbackLiteralWithoutDialing(t *testing.T) {
	transport := NewSafeTransport(nil)
	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:8080/private", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = transport.RoundTrip(req)
	if !errors.Is(err, ErrOutboundAddressBlocked) {
		t.Fatalf("RoundTrip error=%v want ErrOutboundAddressBlocked", err)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
