package network

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/droidpector/apkinspector/src/model"
)

// dnsCache remembers which hostname the guest resolved to which IP so that
// connections can be labeled with a host even without SNI/Host headers, and
// so DNS time can be attributed to the following request.
type dnsCache struct {
	mu      sync.Mutex
	byIP    map[netip.Addr]dnsEntry
	maxSize int
}

type dnsEntry struct {
	host     string
	resolved time.Time
	tookMs   float64
}

func newDNSCache() *dnsCache { return &dnsCache{byIP: map[netip.Addr]dnsEntry{}, maxSize: 4096} }

func (c *dnsCache) put(ip netip.Addr, host string, took time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.byIP) >= c.maxSize {
		for k := range c.byIP { // evict arbitrary entries; this is only a hint cache
			delete(c.byIP, k)
			if len(c.byIP) < c.maxSize/2 {
				break
			}
		}
	}
	c.byIP[ip] = dnsEntry{host: host, resolved: time.Now(), tookMs: float64(took.Microseconds()) / 1000}
}

func (c *dnsCache) lookup(ip netip.Addr) (dnsEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.byIP[ip]
	return e, ok
}

// dnsAnswerer resolves guest DNS queries on the host.
type dnsAnswerer struct {
	up     *Upstream
	policy Policy
	cache  *dnsCache
}

type dnsResult struct {
	resp   []byte
	info   model.DNSInfo
	took   time.Duration
	err    error
	parsed bool
}

var typeNames = map[dnsmessage.Type]string{
	dnsmessage.TypeA: "A", dnsmessage.TypeAAAA: "AAAA", dnsmessage.TypeCNAME: "CNAME", dnsmessage.TypeMX: "MX",
	dnsmessage.TypeTXT: "TXT", dnsmessage.TypeSRV: "SRV", dnsmessage.TypePTR: "PTR", dnsmessage.TypeNS: "NS",
	dnsmessage.TypeSOA: "SOA", dnsmessage.Type(65): "HTTPS", dnsmessage.Type(64): "SVCB",
}

func typeName(t dnsmessage.Type) string {
	if n, ok := typeNames[t]; ok {
		return n
	}
	return fmt.Sprintf("TYPE%d", t)
}

func rcodeName(r dnsmessage.RCode) string {
	switch r {
	case dnsmessage.RCodeSuccess:
		return "NOERROR"
	case dnsmessage.RCodeNameError:
		return "NXDOMAIN"
	case dnsmessage.RCodeServerFailure:
		return "SERVFAIL"
	case dnsmessage.RCodeRefused:
		return "REFUSED"
	case dnsmessage.RCodeFormatError:
		return "FORMERR"
	case dnsmessage.RCodeNotImplemented:
		return "NOTIMP"
	}
	return fmt.Sprintf("RCODE%d", r)
}

// answer resolves one query message and builds the response.
func (a *dnsAnswerer) answer(ctx context.Context, query []byte) dnsResult {
	start := time.Now()
	var p dnsmessage.Parser
	h, err := p.Start(query)
	if err != nil {
		return dnsResult{err: fmt.Errorf("malformed DNS query: %w", err)}
	}
	q, err := p.Question()
	if err != nil {
		return dnsResult{err: fmt.Errorf("malformed DNS question: %w", err)}
	}
	name := strings.TrimSuffix(q.Name.String(), ".")
	res := dnsResult{parsed: true, info: model.DNSInfo{Question: name, QType: typeName(q.Type)}}

	rh := dnsmessage.Header{ID: h.ID, Response: true, OpCode: h.OpCode, RecursionDesired: h.RecursionDesired, RecursionAvailable: true}
	var answers []dnsmessage.Resource
	ttl := uint32(60)
	rr := func(body dnsmessage.ResourceBody, data string) {
		answers = append(answers, dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: q.Name, Class: dnsmessage.ClassINET, TTL: ttl}, Body: body})
		res.info.Answers = append(res.info.Answers, model.DNSAnswer{Name: name, Type: typeName(q.Type), TTL: ttl, Data: data})
	}
	lookupErr := func(err error) {
		var de *net.DNSError
		switch {
		case errors.As(err, &de) && de.IsNotFound:
			rh.RCode = dnsmessage.RCodeNameError
		default:
			rh.RCode = dnsmessage.RCodeServerFailure
			res.err = err
		}
	}
	if h.OpCode != 0 || q.Class != dnsmessage.ClassINET {
		rh.RCode = dnsmessage.RCodeNotImplemented
	} else {
		switch q.Type {
		case dnsmessage.TypeA, dnsmessage.TypeAAAA:
			network := "ip4"
			if q.Type == dnsmessage.TypeAAAA {
				network = "ip6"
			}
			if q.Type == dnsmessage.TypeAAAA && a.policy.FilterAAAA {
				break // NOERROR/NODATA: the sandbox network is IPv4-only
			}
			ips, err := a.up.Resolve(ctx, name, network)
			if err != nil {
				lookupErr(err)
				break
			}
			for _, ip := range ips {
				ip = ip.Unmap()
				if ip.Is4() && q.Type == dnsmessage.TypeA {
					rr(&dnsmessage.AResource{A: ip.As4()}, ip.String())
					a.cache.put(ip, name, time.Since(start))
				} else if ip.Is6() && q.Type == dnsmessage.TypeAAAA {
					rr(&dnsmessage.AAAAResource{AAAA: ip.As16()}, ip.String())
				}
			}
		case dnsmessage.TypeTXT:
			txts, err := net.DefaultResolver.LookupTXT(ctx, name)
			if err != nil {
				lookupErr(err)
				break
			}
			for _, t := range txts {
				rr(&dnsmessage.TXTResource{TXT: splitTXT(t)}, t)
			}
		case dnsmessage.TypeMX:
			mxs, err := net.DefaultResolver.LookupMX(ctx, name)
			if err != nil {
				lookupErr(err)
				break
			}
			for _, mx := range mxs {
				if n, err := dnsmessage.NewName(dotted(mx.Host)); err == nil {
					rr(&dnsmessage.MXResource{Pref: mx.Pref, MX: n}, fmt.Sprintf("%d %s", mx.Pref, mx.Host))
				}
			}
		case dnsmessage.TypeSRV:
			_, srvs, err := net.DefaultResolver.LookupSRV(ctx, "", "", name)
			if err != nil {
				lookupErr(err)
				break
			}
			for _, s := range srvs {
				if n, err := dnsmessage.NewName(dotted(s.Target)); err == nil {
					rr(&dnsmessage.SRVResource{Priority: s.Priority, Weight: s.Weight, Port: s.Port, Target: n},
						fmt.Sprintf("%d %d %d %s", s.Priority, s.Weight, s.Port, s.Target))
				}
			}
		case dnsmessage.TypeCNAME:
			cname, err := net.DefaultResolver.LookupCNAME(ctx, name)
			if err != nil {
				lookupErr(err)
				break
			}
			if n, err := dnsmessage.NewName(dotted(cname)); err == nil && !strings.EqualFold(dotted(cname), dotted(name)) {
				rr(&dnsmessage.CNAMEResource{CNAME: n}, cname)
			}
		default:
			// HTTPS/SVCB, PTR and other types: answer NODATA so clients fall back
			// to A lookups promptly instead of timing out.
		}
	}
	res.info.RCode = rcodeName(rh.RCode)
	b := dnsmessage.NewBuilder(make([]byte, 0, 512), rh)
	b.EnableCompression()
	if err := b.StartQuestions(); err == nil {
		b.Question(q)
	}
	if err := b.StartAnswers(); err == nil {
		for _, ans := range answers {
			switch body := ans.Body.(type) {
			case *dnsmessage.AResource:
				b.AResource(ans.Header, *body)
			case *dnsmessage.AAAAResource:
				b.AAAAResource(ans.Header, *body)
			case *dnsmessage.TXTResource:
				b.TXTResource(ans.Header, *body)
			case *dnsmessage.MXResource:
				b.MXResource(ans.Header, *body)
			case *dnsmessage.SRVResource:
				b.SRVResource(ans.Header, *body)
			case *dnsmessage.CNAMEResource:
				b.CNAMEResource(ans.Header, *body)
			}
		}
	}
	msg, err := b.Finish()
	if err != nil {
		res.err = fmt.Errorf("building DNS response: %w", err)
		return res
	}
	res.resp = msg
	res.took = time.Since(start)
	return res
}

func dotted(n string) string {
	if strings.HasSuffix(n, ".") {
		return n
	}
	return n + "."
}

func splitTXT(s string) []string {
	var out []string
	for len(s) > 255 {
		out = append(out, s[:255])
		s = s[255:]
	}
	return append(out, s)
}

// dnsEvent converts a result into a network event.
func (g *Gateway) dnsEvent(res dnsResult, started time.Time, client, server netip.AddrPort, proto string) {
	if !res.parsed {
		return
	}
	e := &model.Event{
		ID: model.NewID(), Kind: model.KindDNS, Category: model.CatDNS, State: model.StateComplete,
		Initiator: model.InitiatorGuest, StartedAt: started, DurationMs: ms(res.took), Protocol: proto,
		Host: res.info.Question, Method: res.info.QType, DNS: &res.info,
		Conn: &model.Conn{ClientAddr: client.String(), ServerAddr: server.String()},
	}
	if res.err != nil {
		e.State, e.Error = model.StateError, res.err.Error()
	} else if res.info.RCode != "NOERROR" {
		e.Error = res.info.RCode
	}
	g.emit(e)
}

// serveDNSUDP answers queries arriving on one guest UDP flow.
func (g *Gateway) serveDNSUDP(ctx context.Context, conn net.Conn, client, server netip.AddrPort) {
	defer conn.Close()
	buf := make([]byte, 4096)
	for {
		conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		n, err := conn.Read(buf)
		if err != nil {
			return
		}
		started := time.Now()
		qctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		res := g.dns.answer(qctx, buf[:n])
		cancel()
		if res.resp != nil {
			if len(res.resp) > 512 { // no EDNS negotiation: signal truncation for TCP retry
				res.resp = truncateDNS(res.resp)
			}
			conn.Write(res.resp)
		}
		g.dnsEvent(res, started, client, server, "DNS")
	}
}

// serveDNSTCP answers DNS-over-TCP (RFC 7766) on a guest connection.
func (g *Gateway) serveDNSTCP(ctx context.Context, conn net.Conn, client, server netip.AddrPort) {
	defer conn.Close()
	var lenBuf [2]byte
	for {
		conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		if _, err := io.ReadFull(conn, lenBuf[:]); err != nil {
			return
		}
		q := make([]byte, binary.BigEndian.Uint16(lenBuf[:]))
		if _, err := io.ReadFull(conn, q); err != nil {
			return
		}
		started := time.Now()
		qctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		res := g.dns.answer(qctx, q)
		cancel()
		if res.resp != nil {
			binary.BigEndian.PutUint16(lenBuf[:], uint16(len(res.resp)))
			if _, err := conn.Write(append(lenBuf[:], res.resp...)); err != nil {
				return
			}
		}
		g.dnsEvent(res, started, client, server, "DNS/TCP")
	}
}

// truncateDNS returns the header+question of a response with the TC bit set.
func truncateDNS(resp []byte) []byte {
	var p dnsmessage.Parser
	h, err := p.Start(resp)
	if err != nil {
		return resp
	}
	q, err := p.Question()
	if err != nil {
		return resp
	}
	h.Truncated = true
	b := dnsmessage.NewBuilder(nil, h)
	b.StartQuestions()
	b.Question(q)
	out, err := b.Finish()
	if err != nil {
		return resp
	}
	return out
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
