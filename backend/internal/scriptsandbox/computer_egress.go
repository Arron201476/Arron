package scriptsandbox

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"time"
)

// Conservative exclusions from IANA special-purpose registries. This also
// denies some globally reachable special-purpose services, not ordinary web hosts.
// https://www.iana.org/assignments/iana-ipv4-special-registry/
// https://www.iana.org/assignments/iana-ipv6-special-registry/
var computerSpecialRanges = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
}
var computerIPv6Global = netip.MustParsePrefix("2000::/3")

func computerPublicAddress(address netip.Addr) bool {
	if !address.IsValid() || address.Zone() != "" {
		return false
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() || address.IsPrivate() || (address.Is6() && !computerIPv6Global.Contains(address)) {
		return false
	}
	for _, prefix := range computerSpecialRanges {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

type computerDNSResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

// This dial policy must sit behind an authenticated per-session proxy. It is
// not, by itself, container isolation, TLS hostname enforcement or user approval.
type computerEgressPolicy struct {
	hosts    map[string]bool
	resolver computerDNSResolver
	dial     func(context.Context, string, string) (net.Conn, error)
}

func canonicalComputerHost(host string) (string, error) {
	if host == "" || len(host) > 253 || strings.TrimSpace(host) != host {
		return "", errors.New("invalid computer destination")
	}
	if address, err := netip.ParseAddr(host); err == nil {
		if !computerPublicAddress(address) {
			return "", errors.New("computer destination is not public")
		}
		return address.Unmap().String(), nil
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if !strings.Contains(host, ".") {
		return "", errors.New("computer destination requires an exact hostname")
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("invalid computer destination label")
		}
		for _, char := range label {
			if !(char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '-') {
				return "", errors.New("computer destination must use ASCII DNS labels")
			}
		}
	}
	return host, nil
}

func newComputerEgressPolicy(hosts []string) (*computerEgressPolicy, error) {
	if len(hosts) == 0 || len(hosts) > 256 {
		return nil, errors.New("computer egress requires an explicit bounded destination list")
	}
	policy := &computerEgressPolicy{hosts: map[string]bool{}, resolver: net.DefaultResolver,
		dial: (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext}
	for _, value := range hosts {
		host, err := canonicalComputerHost(value)
		if err != nil || policy.hosts[host] {
			return nil, errors.New("invalid or duplicate computer destination")
		}
		policy.hosts[host] = true
	}
	return policy, nil
}

func (policy *computerEgressPolicy) DialContext(ctx context.Context, network, authority string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	host, port, err := net.SplitHostPort(authority)
	if err != nil || network != "tcp" || (port != "80" && port != "443") {
		return nil, errors.New("computer egress only allows configured HTTP(S) targets")
	}
	host, err = canonicalComputerHost(host)
	if err != nil || !policy.hosts[host] {
		return nil, errors.New("computer destination is not granted")
	}
	var addresses []netip.Addr
	if literal, parseErr := netip.ParseAddr(host); parseErr == nil {
		addresses = []netip.Addr{literal}
	} else {
		addresses, err = policy.resolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, errors.New("computer destination resolution failed")
		}
	}
	if len(addresses) == 0 || len(addresses) > 64 {
		return nil, errors.New("invalid computer destination resolution")
	}
	for _, address := range addresses {
		if !computerPublicAddress(address) {
			return nil, errors.New("computer destination resolves to a restricted address")
		}
	}
	// Never pass the hostname to the dialer: DNS is not repeated after validation.
	return policy.dial(ctx, "tcp", net.JoinHostPort(addresses[0].Unmap().String(), port))
}
