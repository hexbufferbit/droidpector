package network

import (
	"errors"
	"net/netip"
	"testing"
)

func TestPolicyCheck(t *testing.T) {
	p := DefaultPolicy()
	blocked := []string{"127.0.0.1:80", "[::1]:80", "169.254.169.254:80", "[fe80::1]:443", "0.0.0.0:80", "224.0.0.1:5353"}
	for _, s := range blocked {
		if err := p.Check(netip.MustParseAddrPort(s)); !errors.Is(err, ErrBlocked) {
			t.Errorf("%s should be blocked, got %v", s, err)
		}
	}
	for _, s := range []string{"93.184.216.34:443", "192.168.1.10:80", "10.1.2.3:8080", "[2606:4700::1]:443"} {
		if err := p.Check(netip.MustParseAddrPort(s)); err != nil {
			t.Errorf("%s should be allowed: %v", s, err)
		}
	}
	p.BlockPrivate = true
	if err := p.Check(netip.MustParseAddrPort("192.168.1.10:80")); !errors.Is(err, ErrBlocked) {
		t.Error("private network should be blocked when BlockPrivate is set")
	}
}
