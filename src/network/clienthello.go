package network

import (
	"encoding/binary"
	"errors"
)

// ClientHello holds what the gateway needs from a TLS ClientHello before
// deciding between interception and passthrough.
type ClientHello struct {
	ServerName string
	ALPN       []string
	Versions   []uint16 // supported_versions extension (TLS 1.3 clients)
}

var (
	errNotTLS       = errors.New("not a TLS handshake")
	errNeedMoreData = errors.New("incomplete ClientHello")
	errBadHello     = errors.New("malformed ClientHello")
)

const maxHelloSize = 64 << 10

// parseClientHello parses a ClientHello from the start of a TLS stream. It
// reassembles handshake messages fragmented over several records. It returns
// errNeedMoreData if b does not yet contain the complete message.
func parseClientHello(b []byte) (*ClientHello, error) {
	var hs []byte
	for len(b) > 0 {
		if len(b) < 5 {
			return nil, errNeedMoreData
		}
		if b[0] != 0x16 || b[1] != 0x03 {
			if len(hs) == 0 {
				return nil, errNotTLS
			}
			return nil, errBadHello
		}
		l := int(binary.BigEndian.Uint16(b[3:5]))
		if l == 0 || l > 1<<14+2048 {
			return nil, errBadHello
		}
		if len(b) < 5+l {
			return nil, errNeedMoreData
		}
		hs = append(hs, b[5:5+l]...)
		b = b[5+l:]
		if len(hs) >= 4 {
			if hs[0] != 1 {
				return nil, errBadHello
			}
			ml := int(hs[1])<<16 | int(hs[2])<<8 | int(hs[3])
			if ml > maxHelloSize {
				return nil, errBadHello
			}
			if len(hs) >= 4+ml {
				return parseHelloBody(hs[4 : 4+ml])
			}
		}
	}
	return nil, errNeedMoreData
}

type reader struct {
	b   []byte
	bad bool
}

func (r *reader) u8() int {
	if len(r.b) < 1 {
		r.bad = true
		return 0
	}
	v := r.b[0]
	r.b = r.b[1:]
	return int(v)
}

func (r *reader) u16() int {
	if len(r.b) < 2 {
		r.bad = true
		return 0
	}
	v := binary.BigEndian.Uint16(r.b)
	r.b = r.b[2:]
	return int(v)
}

func (r *reader) bytes(n int) []byte {
	if n < 0 || len(r.b) < n {
		r.bad = true
		return nil
	}
	v := r.b[:n]
	r.b = r.b[n:]
	return v
}

func parseHelloBody(body []byte) (*ClientHello, error) {
	r := &reader{b: body}
	r.bytes(2)       // legacy_version
	r.bytes(32)      // random
	r.bytes(r.u8())  // session id
	r.bytes(r.u16()) // cipher suites
	r.bytes(r.u8())  // compression methods
	if r.bad {
		return nil, errBadHello
	}
	ch := &ClientHello{}
	if len(r.b) == 0 {
		return ch, nil // no extensions (very old clients)
	}
	ext := &reader{b: r.bytes(r.u16())}
	if r.bad {
		return nil, errBadHello
	}
	for len(ext.b) > 0 {
		typ := ext.u16()
		data := &reader{b: ext.bytes(ext.u16())}
		if ext.bad {
			return nil, errBadHello
		}
		switch typ {
		case 0: // server_name
			list := &reader{b: data.bytes(data.u16())}
			for len(list.b) > 0 && !list.bad {
				nameType := list.u8()
				name := list.bytes(list.u16())
				if nameType == 0 && ch.ServerName == "" {
					ch.ServerName = string(name)
				}
			}
			if list.bad || data.bad {
				return nil, errBadHello
			}
		case 16: // application_layer_protocol_negotiation
			list := &reader{b: data.bytes(data.u16())}
			for len(list.b) > 0 && !list.bad {
				if p := list.bytes(list.u8()); len(p) > 0 {
					ch.ALPN = append(ch.ALPN, string(p))
				}
			}
			if list.bad || data.bad {
				return nil, errBadHello
			}
		case 43: // supported_versions
			list := &reader{b: data.bytes(data.u8())}
			for len(list.b) >= 2 {
				ch.Versions = append(ch.Versions, uint16(list.u16()))
			}
		}
	}
	return ch, nil
}
