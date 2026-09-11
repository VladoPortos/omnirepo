package httpx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

var ErrOutboundAddressBlocked = errors.New("outbound destination is not publicly routable")

type lookupIPAddrFunc func(context.Context, string) ([]net.IPAddr, error)

type safeRoundTripper struct {
	base   http.RoundTripper
	lookup lookupIPAddrFunc
}

// NewSafeTransport clones base (or http.DefaultTransport) and returns a
// RoundTripper that validates every request and connects only to the validated
// IP address. This closes redirect and DNS-rebinding paths to private services.
func NewSafeTransport(base *http.Transport) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport.(*http.Transport)
	}
	transport := base.Clone()
	lookup := net.DefaultResolver.LookupIPAddr
	originalDial := transport.DialContext
	if originalDial == nil {
		originalDial = (&net.Dialer{}).DialContext
	}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("safe transport: split destination: %w", err)
		}
		ips, err := resolvePublicIPs(ctx, host, lookup)
		if err != nil {
			return nil, err
		}
		return originalDial(ctx, network, net.JoinHostPort(ips[0].String(), port))
	}
	return newSafeRoundTripper(transport, lookup)
}

func newSafeRoundTripper(base http.RoundTripper, lookup lookupIPAddrFunc) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	if lookup == nil {
		lookup = net.DefaultResolver.LookupIPAddr
	}
	return &safeRoundTripper{base: base, lookup: lookup}
}

func (t *safeRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil || req.URL == nil {
		return nil, errors.New("safe transport: request URL is required")
	}
	host := req.URL.Hostname()
	if host == "" {
		return nil, errors.New("safe transport: destination host is required")
	}
	if _, err := resolvePublicIPs(req.Context(), host, t.lookup); err != nil {
		return nil, err
	}
	return t.base.RoundTrip(req)
}

// ValidateOutboundHost rejects prohibited IP literals. DNS names are checked
// by NewSafeTransport at request and connection time.
func ValidateOutboundHost(host string) error {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" {
		return errors.New("outbound destination host is empty")
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return nil
	}
	return validateOutboundAddr(addr)
}

func resolvePublicIPs(ctx context.Context, host string, lookup lookupIPAddrFunc) ([]netip.Addr, error) {
	if err := ValidateOutboundHost(host); err != nil {
		return nil, err
	}
	if literal, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		return []netip.Addr{literal.Unmap()}, nil
	}
	resolved, err := lookup(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("safe transport: resolve destination: %w", err)
	}
	if len(resolved) == 0 {
		return nil, errors.New("safe transport: destination resolved to no addresses")
	}
	addrs := make([]netip.Addr, 0, len(resolved))
	for _, ip := range resolved {
		addr, ok := netip.AddrFromSlice(ip.IP)
		if !ok {
			return nil, ErrOutboundAddressBlocked
		}
		addr = addr.Unmap()
		if err := validateOutboundAddr(addr); err != nil {
			return nil, err
		}
		addrs = append(addrs, addr)
	}
	return addrs, nil
}

func validateOutboundAddr(addr netip.Addr) error {
	if !addr.IsValid() || !addr.IsGlobalUnicast() || addr.IsPrivate() ||
		addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() ||
		addr.IsMulticast() || addr.IsUnspecified() || isSpecialUseAddr(addr) {
		return ErrOutboundAddressBlocked
	}
	return nil
}

func isSpecialUseAddr(addr netip.Addr) bool {
	if !addr.Is4() {
		return false
	}
	return netip.MustParsePrefix("100.64.0.0/10").Contains(addr) ||
		netip.MustParsePrefix("198.18.0.0/15").Contains(addr)
}
