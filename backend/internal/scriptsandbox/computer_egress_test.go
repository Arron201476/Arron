package scriptsandbox

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
)

type computerDNSFixture struct {
	addresses []netip.Addr
	calls     int
}

func (f *computerDNSFixture) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	f.calls++
	return f.addresses, nil
}

func TestComputerEgressRejectsPrivateAndSpecialAddresses(t *testing.T) {
	for _, address := range []string{"0.0.0.0", "10.1.2.3", "100.64.0.1", "127.0.0.1", "169.254.169.254", "172.16.0.1", "192.168.1.1", "192.0.2.1", "198.18.0.1", "198.51.100.1", "203.0.113.1", "224.0.0.1", "240.0.0.1", "::1", "::ffff:127.0.0.1", "64:ff9b::a00:1", "fe80::1", "fc00::1", "2002:a00:1::", "2001:db8::1", "3fff::1"} {
		if computerPublicAddress(netip.MustParseAddr(address)) {
			t.Fatalf("restricted address accepted: %s", address)
		}
	}
	for _, address := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111", "::ffff:8.8.8.8"} {
		if !computerPublicAddress(netip.MustParseAddr(address)) {
			t.Fatalf("ordinary public address rejected: %s", address)
		}
	}
}

func TestComputerEgressPinsValidatedIPWithoutSecondDNSLookup(t *testing.T) {
	policy, err := newComputerEgressPolicy([]string{"EXAMPLE.test."})
	if err != nil {
		t.Fatal(err)
	}
	dns := &computerDNSFixture{addresses: []netip.Addr{netip.MustParseAddr("8.8.8.8")}}
	policy.resolver = dns
	var target string
	policy.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		target = address
		return nil, errors.New("isolated dial fixture")
	}
	_, _ = policy.DialContext(context.Background(), "tcp", "example.test:443")
	if target != "8.8.8.8:443" || dns.calls != 1 {
		t.Fatalf("destination not pinned: %s (%d lookups)", target, dns.calls)
	}
	target = ""
	dns.addresses = append(dns.addresses, netip.MustParseAddr("10.0.0.1"))
	if _, err := policy.DialContext(context.Background(), "tcp", "example.test:443"); err == nil || target != "" {
		t.Fatal("mixed public/private DNS response was dialled")
	}
}

func TestComputerEgressDeniesUnlistedHostsAndPortsBeforeDNS(t *testing.T) {
	policy, err := newComputerEgressPolicy([]string{"example.test"})
	if err != nil {
		t.Fatal(err)
	}
	dns := &computerDNSFixture{}
	policy.resolver = dns
	for _, authority := range []string{"other.test:443", "example.test:22", "localhost:80", "127.0.0.1:80", "example.test:0443"} {
		if _, err := policy.DialContext(context.Background(), "tcp", authority); err == nil {
			t.Fatal("ungranted target accepted")
		}
	}
	if dns.calls != 0 {
		t.Fatal("ungranted target reached resolver")
	}
	for _, hosts := range [][]string{nil, {"*.example.test"}, {"example.test", "EXAMPLE.test."}, {"127.0.0.1"}, {"user@example.test"}} {
		if _, err := newComputerEgressPolicy(hosts); err == nil {
			t.Fatal("ambiguous policy accepted")
		}
	}
}
