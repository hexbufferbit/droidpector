package adb

import (
	"encoding/binary"
	"fmt"
	"io"
)

// ADB transport commands (system/core/adb/protocol.txt).
const (
	CmdSYNC uint32 = 0x434e5953
	CmdCNXN uint32 = 0x4e584e43
	CmdAUTH uint32 = 0x48545541
	CmdOPEN uint32 = 0x4e45504f
	CmdOKAY uint32 = 0x59414b4f
	CmdCLSE uint32 = 0x45534c43
	CmdWRTE uint32 = 0x45545257
	CmdSTLS uint32 = 0x534c5453
)

// AUTH message types (arg0).
const (
	AuthToken        uint32 = 1
	AuthSignature    uint32 = 2
	AuthRSAPublicKey uint32 = 3
)

const (
	// Version is the protocol version this client speaks (A_VERSION).
	Version uint32 = 0x01000001
	// versionSkipChecksum is the first version where data checksums are optional.
	versionSkipChecksum uint32 = 0x01000001
	// versionMin is the oldest protocol version (A_VERSION_MIN).
	versionMin uint32 = 0x01000000

	// DefaultMaxPayload is the payload size proposed in CNXN.
	DefaultMaxPayload uint32 = 256 * 1024
	// legacyMaxPayload is the payload limit of pre-N devices.
	legacyMaxPayload uint32 = 4096
	// maxAcceptedPayload caps incoming payloads regardless of negotiation
	// (current adbd's MAX_PAYLOAD is 1 MiB).
	maxAcceptedPayload uint32 = 1 << 20

	headerSize = 24
)

// Message is one ADB transport packet.
type Message struct {
	Command uint32
	Arg0    uint32
	Arg1    uint32
	Data    []byte
}

func (m Message) String() string {
	return fmt.Sprintf("%s(%#x, %#x, %d bytes)", commandName(m.Command), m.Arg0, m.Arg1, len(m.Data))
}

func commandName(c uint32) string {
	switch c {
	case CmdSYNC, CmdCNXN, CmdAUTH, CmdOPEN, CmdOKAY, CmdCLSE, CmdWRTE, CmdSTLS:
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], c)
		return string(b[:])
	}
	return fmt.Sprintf("0x%08x", c)
}

// checksum is the legacy ADB data checksum: the byte sum of the payload.
func checksum(b []byte) uint32 {
	var s uint32
	for _, c := range b {
		s += uint32(c)
	}
	return s
}

// marshal encodes the message; withChecksum fills in the data checksum.
func (m Message) marshal(withChecksum bool) []byte {
	b := make([]byte, headerSize+len(m.Data))
	binary.LittleEndian.PutUint32(b[0:], m.Command)
	binary.LittleEndian.PutUint32(b[4:], m.Arg0)
	binary.LittleEndian.PutUint32(b[8:], m.Arg1)
	binary.LittleEndian.PutUint32(b[12:], uint32(len(m.Data)))
	if withChecksum {
		binary.LittleEndian.PutUint32(b[16:], checksum(m.Data))
	}
	binary.LittleEndian.PutUint32(b[20:], m.Command^0xFFFFFFFF)
	copy(b[headerSize:], m.Data)
	return b
}

// checksumMode says how incoming checksums are validated.
type checksumMode int

const (
	checksumIfPresent checksumMode = iota // verify when non-zero (handshake)
	checksumRequired                      // legacy peers: always verify
	checksumIgnore                        // modern peers: never verify
)

// readMessage reads one packet, validating magic, payload size and checksum.
func readMessage(r io.Reader, maxPayload uint32, mode checksumMode) (Message, error) {
	var h [headerSize]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return Message{}, err
	}
	m := Message{
		Command: binary.LittleEndian.Uint32(h[0:]),
		Arg0:    binary.LittleEndian.Uint32(h[4:]),
		Arg1:    binary.LittleEndian.Uint32(h[8:]),
	}
	n := binary.LittleEndian.Uint32(h[12:])
	sum := binary.LittleEndian.Uint32(h[16:])
	if magic := binary.LittleEndian.Uint32(h[20:]); magic != m.Command^0xFFFFFFFF {
		return Message{}, fmt.Errorf("%w: bad magic %#08x for command %s", ErrProtocol, magic, commandName(m.Command))
	}
	if n > maxPayload {
		return Message{}, fmt.Errorf("%w: %s payload of %d bytes exceeds limit %d", ErrProtocol, commandName(m.Command), n, maxPayload)
	}
	if n > 0 {
		m.Data = make([]byte, n)
		if _, err := io.ReadFull(r, m.Data); err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return Message{}, err
		}
	}
	switch {
	case mode == checksumRequired, mode == checksumIfPresent && sum != 0:
		if got := checksum(m.Data); got != sum {
			return Message{}, fmt.Errorf("%w: %s checksum mismatch (got %#x, header %#x)", ErrProtocol, commandName(m.Command), got, sum)
		}
	}
	return m, nil
}
