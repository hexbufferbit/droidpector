package network

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/droidpector/apkinspector/src/model"
)

func prefixes(s ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(s))
	for i, v := range s {
		out[i] = netip.MustParsePrefix(v)
	}
	return out
}

func TestAddressingFor(t *testing.T) {
	a, err := AddressingFor(netip.MustParsePrefix("172.31.254.77/24"))
	if err != nil {
		t.Fatal(err)
	}
	if a.Subnet.String() != "172.31.254.0/24" || a.Gateway.String() != "172.31.254.2" ||
		a.DNS.String() != "172.31.254.3" || a.Guest.String() != "172.31.254.15" {
		t.Fatalf("unexpected plan %+v", a)
	}
	if a.MTU == 0 || len(a.GatewayMAC) != 6 {
		t.Fatal("defaults must be kept")
	}
	for _, bad := range []string{"10.0.0.0/8", "10.0.0.0/30", "fd00::/64", "198.18.5.0/24"} {
		if _, err := AddressingFor(netip.MustParsePrefix(bad)); err == nil {
			t.Errorf("%s must be rejected", bad)
		}
	}
	if DefaultAddressing().Subnet.String() != "10.0.2.0/24" {
		t.Fatal("default plan changed")
	}
}

func TestChooseAddressingAvoidsVPNRoutes(t *testing.T) {
	cases := []struct {
		name   string
		routes []netip.Prefix
		want   string
	}{
		{"no conflicts keeps the QEMU default", prefixes("0.0.0.0/0", "192.168.1.0/24", "127.0.0.0/8"), "10.0.2.0/24"},
		{"full-tunnel VPN halves are not conflicts", prefixes("0.0.0.0/1", "128.0.0.0/1", "10.8.0.0/24"), "10.0.2.0/24"},
		{"corporate 10/8 over the VPN", prefixes("0.0.0.0/0", "10.0.0.0/8"), "172.31.254.0/24"},
		{"everything private routed", prefixes("10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"), "100.127.254.0/24"},
		{"host route inside the default", prefixes("10.0.2.50/32"), "172.31.254.0/24"},
	}
	for _, c := range cases {
		ch, err := ChooseAddressing("auto", c.routes)
		if err != nil {
			t.Fatal(err)
		}
		if got := ch.Addressing.Subnet.String(); got != c.want || len(ch.Conflicts) != 0 {
			t.Errorf("%s: got %s conflicts %v, want %s", c.name, got, ch.Conflicts, c.want)
		}
	}
}

func TestChooseAddressingReportsUnavoidableConflicts(t *testing.T) {
	all := prefixes("10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10")
	ch, err := ChooseAddressing("", all)
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.Conflicts) != 1 || !strings.Contains(ch.Warning(), "sandboxSubnet") {
		t.Fatalf("expected one reported conflict, got %v (%q)", ch.Conflicts, ch.Warning())
	}
}

func TestChooseAddressingConfigured(t *testing.T) {
	ch, err := ChooseAddressing(" 192.168.77.0/24 ", prefixes("192.168.77.0/24"))
	if err != nil {
		t.Fatal(err)
	}
	if ch.Addressing.Guest.String() != "192.168.77.15" || len(ch.Conflicts) != 1 {
		t.Fatalf("configured subnet: %+v", ch)
	}
	if _, err := ChooseAddressing("not-a-net", nil); err == nil {
		t.Fatal("invalid subnet must be an error")
	}
	if ch, _ := ChooseAddressing("auto", nil); ch.Warning() != "" {
		t.Fatal("no conflicts, no warning")
	}
}

func TestHostRoutesReadable(t *testing.T) {
	if _, err := HostRoutes(); err != nil {
		t.Fatal(err)
	}
}

func TestRouteEgressFindsLocalInterface(t *testing.T) {
	local := routeEgress(netip.MustParseAddrPort("127.0.0.1:9"))
	if local == nil {
		t.Fatal("no route to loopback")
	}
	var c model.Conn
	setEgress(&c, local)
	if !strings.HasPrefix(c.LocalAddr, "127.0.0.1:") || c.Interface == "" {
		t.Fatalf("egress %+v", c)
	}
	if routeEgress(netip.MustParseAddrPort("198.18.0.7:443")) != nil {
		t.Fatal("virtual (host-mapped) addresses have no real route")
	}
}
