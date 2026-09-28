package network

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Policy controls what the guest may reach. The defaults protect the host:
// the guest can reach the Internet (and LAN unless BlockPrivate) but never
// host-local services or cloud metadata endpoints.
type Policy struct {
	BlockLoopback  bool // 127.0.0.0/8, ::1 — host services
	BlockLinkLocal bool // 169.254.0.0/16 (incl. cloud metadata 169.254.169.254), fe80::/10
	BlockPrivate   bool // RFC1918 / ULA — host LAN
	BlockQUIC      bool // UDP/443: force apps onto inspectable TCP
	FilterAAAA     bool // answer AAAA queries with no records (guest has IPv4 only)
}

// DefaultPolicy is the production policy.
func DefaultPolicy() Policy {
	return Policy{BlockLoopback: true, BlockLinkLocal: true, BlockQUIC: true, FilterAAAA: true}
}

// ErrBlocked is returned for destinations denied by policy.
var ErrBlocked = errors.New("destination blocked by sandbox policy")

// BlockedError explains why a destination was blocked.
type BlockedError struct {
	Addr   netip.AddrPort
	Reason string
}

func (e *BlockedError) Error() string {
	return fmt.Sprintf("connection to %s blocked by sandbox policy: %s", e.Addr, e.Reason)
}
func (e *BlockedError) Unwrap() error { return ErrBlocked }

// Check evaluates the policy for a destination.
func (p Policy) Check(dst netip.AddrPort) error {
	ip := dst.Addr().Unmap()
	switch {
	case !ip.IsValid() || ip.IsUnspecified():
		return &BlockedError{dst, "invalid destination"}
	case p.BlockLoopback && ip.IsLoopback():
		return &BlockedError{dst, "the sandbox may not reach services on this computer (loopback)"}
	case p.BlockLinkLocal && (ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()):
		return &BlockedError{dst, "link-local and cloud metadata addresses are not reachable from the sandbox"}
	case p.BlockPrivate && ip.IsPrivate():
		return &BlockedError{dst, "private network (LAN) access is disabled in settings"}
	case ip.IsMulticast():
		return &BlockedError{dst, "multicast is not forwarded"}
	}
	return nil
}

// HostMapping redirects a hostname to another upstream address. It is an
// explicit, opt-in feature (used by the deterministic test server and for
// "map remote" debugging); mapped targets bypass the policy on purpose.
type HostMapping struct {
	Host   string // hostname the guest resolves, e.g. "test.apkinspector.internal"
	Target string // upstream host:port, e.g. "127.0.0.1:8443"; port "*" keeps the guest's port
}

// Upstream dials the real destinations of guest flows.
type Upstream struct {
	policy   Policy
	dialer   net.Dialer
	roots    *x509.CertPool // nil = system roots
	resolver Resolver

	mu      sync.Mutex
	vip     map[string]netip.Addr // host → virtual IP handed to the guest
	targets map[netip.Addr]string // virtual IP → target
	next    uint32
}

// Resolver resolves hostnames on the host (system resolver by default).
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// vipBase is the RFC 2544 benchmarking range; it never appears on the Internet,
// so virtual IPs can't collide with real destinations.
var vipBase = netip.MustParseAddr("198.18.0.0")

// NewUpstream creates the upstream dialer.
func NewUpstream(policy Policy, mappings []HostMapping, extraRoots []*x509.Certificate, resolver Resolver) (*Upstream, error) {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	u := &Upstream{
		policy:   policy,
		dialer:   net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second},
		resolver: resolver,
		vip:      map[string]netip.Addr{},
		targets:  map[netip.Addr]string{},
		next:     1,
	}
	if len(extraRoots) > 0 {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		for _, c := range extraRoots {
			pool.AddCert(c)
		}
		u.roots = pool
	}
	for _, m := range mappings {
		if err := u.addMapping(m); err != nil {
			return nil, err
		}
	}
	return u, nil
}

func (u *Upstream) addMapping(m HostMapping) error {
	host := strings.ToLower(strings.TrimSuffix(m.Host, "."))
	if host == "" {
		return fmt.Errorf("host mapping without host")
	}
	if _, _, err := net.SplitHostPort(m.Target); err != nil {
		return fmt.Errorf("host mapping %s: target %q must be host:port", m.Host, m.Target)
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	b := vipBase.As4()
	n := u.next
	u.next++
	b[2], b[3] = byte(n>>8), byte(n)
	ip := netip.AddrFrom4(b)
	u.vip[host] = ip
	u.targets[ip] = m.Target
	return nil
}

// Policy returns the active policy.
func (u *Upstream) Policy() Policy { return u.policy }

// RootCAs returns the pool used to verify upstream servers (nil = system).
func (u *Upstream) RootCAs() *x509.CertPool { return u.roots }

// MappedIP returns the virtual IP for a mapped host.
func (u *Upstream) MappedIP(host string) (netip.Addr, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	ip, ok := u.vip[strings.ToLower(strings.TrimSuffix(host, "."))]
	return ip, ok
}

// Resolve resolves a hostname to IPv4 addresses, honoring host mappings.
func (u *Upstream) Resolve(ctx context.Context, host string, network string) ([]netip.Addr, error) {
	if ip, ok := u.MappedIP(host); ok {
		if network == "ip6" {
			return nil, nil
		}
		return []netip.Addr{ip}, nil
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{ip}, nil
	}
	return u.resolver.LookupNetIP(ctx, network, host)
}

// Dial connects to a guest flow's original destination.
func (u *Upstream) Dial(ctx context.Context, dst netip.AddrPort) (net.Conn, error) {
	u.mu.Lock()
	target, mapped := u.targets[dst.Addr()]
	u.mu.Unlock()
	if mapped {
		h, p, _ := net.SplitHostPort(target)
		if p == "*" {
			p = strconv.Itoa(int(dst.Port()))
		}
		return u.dialer.DialContext(ctx, "tcp", net.JoinHostPort(h, p))
	}
	if err := u.policy.Check(dst); err != nil {
		return nil, err
	}
	return u.dialer.DialContext(ctx, "tcp", dst.String())
}

// DialHost resolves host and dials the first reachable address (used by replay).
func (u *Upstream) DialHost(ctx context.Context, host string, port int) (net.Conn, error) {
	ips, err := u.Resolve(ctx, host, "ip")
	if err != nil {
		return nil, fmt.Errorf("resolving %s: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("resolving %s: no addresses", host)
	}
	var lastErr error
	for _, ip := range ips {
		c, err := u.Dial(ctx, netip.AddrPortFrom(ip.Unmap(), uint16(port)))
		if err == nil {
			return c, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// TLSClientConfig returns the upstream TLS configuration for serverName.
func (u *Upstream) TLSClientConfig(serverName string, alpn []string) *tls.Config {
	return &tls.Config{
		ServerName: serverName,
		RootCAs:    u.roots,
		NextProtos: alpn,
		MinVersion: tls.VersionTLS10,
	}
}
