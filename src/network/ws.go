package network

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"io"
	"strings"
	"time"

	"github.com/droidpector/apkinspector/src/model"
)

// wsParser incrementally decodes RFC 6455 frames from one direction of a
// relayed WebSocket stream. It only observes: bytes are forwarded unchanged
// by the relay whatever the parser does, and a protocol error merely stops
// frame capture for that direction.
type wsParser struct {
	outgoing bool
	maxData  int
	deflate  bool // permessage-deflate negotiated
	emit     func(model.WSFrame)
	now      func() time.Time

	buf    []byte
	failed bool

	// message reassembly for permessage-deflate (compressed messages may span frames)
	msgCompressed bool
	msgOpcode     int
	msgData       []byte
	msgLen        int64
	msgTruncated  bool
	window        []byte // last 32 KiB of inflated output (context takeover)
}

func newWSParser(outgoing bool, maxData int, deflate bool, emit func(model.WSFrame)) *wsParser {
	return &wsParser{outgoing: outgoing, maxData: maxData, deflate: deflate, emit: emit, now: time.Now}
}

// feed consumes a chunk of the stream.
func (p *wsParser) feed(b []byte) {
	if p.failed {
		return
	}
	p.buf = append(p.buf, b...)
	for !p.failed {
		n := p.parseOne()
		if n == 0 {
			break
		}
		p.buf = p.buf[n:]
	}
	// Bound memory: a frame larger than the capture limit is consumed by
	// parseOne progressively via skip; buf only ever holds headers + capped data.
	if len(p.buf) == 0 {
		p.buf = nil
	}
}

// parseOne parses a complete frame at the head of buf and returns bytes
// consumed, or 0 if more data is needed.
func (p *wsParser) parseOne() int {
	b := p.buf
	if len(b) < 2 {
		return 0
	}
	fin := b[0]&0x80 != 0
	rsv1 := b[0]&0x40 != 0
	opcode := int(b[0] & 0x0f)
	masked := b[1]&0x80 != 0
	plen := int64(b[1] & 0x7f)
	hdr := 2
	switch plen {
	case 126:
		if len(b) < 4 {
			return 0
		}
		plen = int64(binary.BigEndian.Uint16(b[2:4]))
		hdr = 4
	case 127:
		if len(b) < 10 {
			return 0
		}
		plen = int64(binary.BigEndian.Uint64(b[2:10]))
		hdr = 10
		if plen < 0 {
			p.failed = true
			return 0
		}
	}
	var mask [4]byte
	if masked {
		if len(b) < hdr+4 {
			return 0
		}
		copy(mask[:], b[hdr:hdr+4])
		hdr += 4
	}
	if opcode > 2 && opcode < 8 || opcode > 10 {
		p.failed = true // reserved opcode: not a WebSocket stream we understand
		return 0
	}
	// Frames larger than the capture limit: keep only the prefix. We need the
	// whole frame in buf to advance, so for huge frames switch to skip mode.
	if int64(len(b)-hdr) < plen {
		if plen > int64(p.maxData)+1<<20 { // avoid buffering huge frames
			p.startSkip(hdr, plen, opcode, fin, rsv1, masked, mask)
			return 0
		}
		return 0
	}
	data := b[hdr : hdr+int(plen)]
	if masked {
		unmasked := make([]byte, len(data))
		for i := range data {
			unmasked[i] = data[i] ^ mask[i%4]
		}
		data = unmasked
	}
	p.frame(opcode, fin, rsv1, data, plen)
	return hdr + int(plen)
}

// startSkip handles an oversized frame: capture the available prefix, then
// discard the rest as it streams by.
func (p *wsParser) startSkip(hdr int, plen int64, opcode int, fin, rsv1, masked bool, mask [4]byte) {
	avail := p.buf[hdr:]
	keep := min(len(avail), p.maxData)
	data := append([]byte(nil), avail[:keep]...)
	if masked {
		for i := range data {
			data[i] ^= mask[i%4]
		}
	}
	remaining := plen - int64(len(avail))
	p.buf = nil
	f := model.WSFrame{Time: p.now(), Outgoing: p.outgoing, Opcode: opcode, Length: plen, Data: data, Truncated: true}
	if rsv1 && p.deflate {
		f.Data = nil // cannot inflate a partial message
	}
	p.emit(f)
	p.failed = remaining > 0 // simplest correct behaviour: stop parsing this direction
}

func (p *wsParser) frame(opcode int, fin, rsv1 bool, data []byte, plen int64) {
	isControl := opcode >= 8
	if isControl || !p.deflate {
		p.emitFrame(opcode, data, plen, false)
		return
	}
	// Data frames with permessage-deflate: reassemble the message when compressed.
	if opcode != 0 { // first frame of a message
		p.msgCompressed, p.msgOpcode, p.msgData, p.msgLen, p.msgTruncated = rsv1, opcode, nil, 0, false
	}
	if !p.msgCompressed {
		p.emitFrame(opcode, data, plen, false)
		return
	}
	p.msgLen += plen
	if len(p.msgData)+len(data) > 4*p.maxData+1<<20 {
		p.msgTruncated = true
	} else {
		p.msgData = append(p.msgData, data...)
	}
	if !fin {
		return
	}
	var out []byte
	if !p.msgTruncated {
		out = p.inflate(p.msgData)
	}
	p.emitFrame(p.msgOpcode, out, p.msgLen, p.msgTruncated || out == nil)
	p.msgData = nil
}

func (p *wsParser) inflate(msg []byte) []byte {
	src := io.MultiReader(bytes.NewReader(msg), bytes.NewReader([]byte{0x00, 0x00, 0xff, 0xff}))
	r := flate.NewReaderDict(src, p.window)
	out, err := io.ReadAll(io.LimitReader(r, int64(p.maxData)*16))
	if err != nil && err != io.ErrUnexpectedEOF {
		return nil
	}
	w := append(p.window, out...)
	if len(w) > 32<<10 {
		w = w[len(w)-32<<10:]
	}
	p.window = append([]byte(nil), w...)
	return out
}

func (p *wsParser) emitFrame(opcode int, data []byte, length int64, truncated bool) {
	f := model.WSFrame{Time: p.now(), Outgoing: p.outgoing, Opcode: opcode, Length: length, Truncated: truncated}
	if len(data) > p.maxData {
		data, f.Truncated = data[:p.maxData], true
	}
	f.Data = append([]byte(nil), data...)
	p.emit(f)
}

// wsDeflateNegotiated reports whether the handshake response enabled
// permessage-deflate.
func wsDeflateNegotiated(extHeader []string) bool {
	for _, h := range extHeader {
		for _, ext := range strings.Split(h, ",") {
			if strings.EqualFold(strings.TrimSpace(strings.SplitN(ext, ";", 2)[0]), "permessage-deflate") {
				return true
			}
		}
	}
	return false
}

// tapReader feeds everything read through it to a parser.
type tapReader struct {
	r io.Reader
	p *wsParser
}

func (t *tapReader) Read(b []byte) (int, error) {
	n, err := t.r.Read(b)
	if n > 0 {
		t.p.feed(b[:n])
	}
	return n, err
}
