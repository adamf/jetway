// Package matip implements MATIP, the mapping of airline traffic over IP
// defined by RFC 2351.
//
// MATIP is how Type B messaging reaches a carrier over IP rather than over a
// legacy telecom circuit. It adds a four-byte header and a small session
// handshake on top of TCP, and carriers' front ends speak it directly.
//
// RFC 2351 is an open IETF document, so unlike the reservation message
// grammars above it, this layer is implemented from the authoritative source
// and can be exact. Where the RFC contradicts itself the code says so and
// follows the reading that is internally consistent.
//
// Type B is what this package targets. Type A -- interactive terminal traffic
// on port 350 -- shares the header format and is not implemented.
package matip

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// IANA-allocated ports. The traffic type is selected by the port.
const (
	PortTypeA = 350
	PortTypeB = 351
)

// Version is the only valid value of the header's version field. A packet
// carrying anything else is invalid.
const Version = 1

// HeaderLen is the fixed header size in bytes.
const HeaderLen = 4

// MaxPacket is the largest packet the 16-bit length field can describe, and
// MaxPayload the largest Type B message that fits in one data packet.
const (
	MaxPacket  = 0xFFFF
	MaxPayload = MaxPacket - HeaderLen
)

// Type B command codes, carried in the header's seven-bit command field.
const (
	// CmdData marks a data packet; the control flag is clear.
	CmdData uint8 = 0x00
	// CmdSessionOpen opens a session.
	CmdSessionOpen uint8 = 0x7E // 1111110
	// CmdOpenConfirm answers a session open, accepting or refusing.
	CmdOpenConfirm uint8 = 0x7D // 1111101
	// CmdSessionClose closes an open session.
	CmdSessionClose uint8 = 0x7C // 1111100
)

// Coding identifies the character coding of the traffic on a session.
type Coding uint8

const (
	CodingBaudot Coding = 0b000 // 5 bits, padded baudot
	CodingIPARS  Coding = 0b010 // 6 bits
	CodingASCII  Coding = 0b100 // 7 bits
	CodingEBCDIC Coding = 0b110 // 8 bits
)

func (c Coding) String() string {
	switch c {
	case CodingBaudot:
		return "baudot"
	case CodingIPARS:
		return "ipars"
	case CodingASCII:
		return "ascii"
	case CodingEBCDIC:
		return "ebcdic"
	}
	return fmt.Sprintf("coding(%03b)", uint8(c))
}

// ProtectionBATAP identifies BATAP as the end-to-end messaging responsibility
// transfer protocol. All other values are unassigned.
const ProtectionBATAP uint8 = 0b0010

// Errors returned when decoding.
var (
	ErrShortPacket   = errors.New("matip: packet shorter than its header")
	ErrBadVersion    = errors.New("matip: unsupported version")
	ErrBadLength     = errors.New("matip: declared length is shorter than the header")
	ErrPayloadTooBig = errors.New("matip: payload exceeds what the length field can describe")
)

// Packet is one MATIP packet.
type Packet struct {
	// Control distinguishes a control packet from a data packet.
	Control bool
	// Cmd is the seven-bit command, meaningful when Control is set.
	Cmd uint8
	// Payload is everything after the header. For a data packet this is the
	// Type B message.
	Payload []byte
}

// IsData reports whether this packet carries traffic rather than session
// control.
func (p Packet) IsData() bool { return !p.Control && p.Cmd == CmdData }

func (p Packet) String() string {
	if !p.Control {
		return fmt.Sprintf("DATA(%d bytes)", len(p.Payload))
	}
	switch p.Cmd {
	case CmdSessionOpen:
		return "SO"
	case CmdOpenConfirm:
		return "OC"
	case CmdSessionClose:
		return "SC"
	}
	return fmt.Sprintf("CONTROL(0x%02X)", p.Cmd)
}

// MarshalBinary renders the packet, header included.
func (p Packet) MarshalBinary() ([]byte, error) {
	if len(p.Payload) > MaxPayload {
		return nil, fmt.Errorf("%w: %d > %d", ErrPayloadTooBig, len(p.Payload), MaxPayload)
	}
	out := make([]byte, HeaderLen+len(p.Payload))
	// Five zero bits then a three-bit version.
	out[0] = Version & 0x07
	out[1] = p.Cmd & 0x7F
	if p.Control {
		out[1] |= 0x80
	}
	// The length counts the whole packet, header included.
	binary.BigEndian.PutUint16(out[2:4], uint16(HeaderLen+len(p.Payload)))
	copy(out[HeaderLen:], p.Payload)
	return out, nil
}

// ParseHeader decodes a four-byte header, returning the packet shape and the
// total packet length.
func ParseHeader(b []byte) (control bool, cmd uint8, total int, err error) {
	if len(b) < HeaderLen {
		return false, 0, 0, ErrShortPacket
	}
	if b[0]&0xF8 != 0 {
		return false, 0, 0, fmt.Errorf("%w: reserved bits set in 0x%02X", ErrBadVersion, b[0])
	}
	if v := b[0] & 0x07; v != Version {
		return false, 0, 0, fmt.Errorf("%w: version %d, want %d", ErrBadVersion, v, Version)
	}
	control = b[1]&0x80 != 0
	cmd = b[1] & 0x7F
	total = int(binary.BigEndian.Uint16(b[2:4]))
	if total < HeaderLen {
		return false, 0, 0, fmt.Errorf("%w: %d", ErrBadLength, total)
	}
	return control, cmd, total, nil
}

// ReadPacket reads exactly one packet.
//
// The length field bounds the read, so a peer cannot make this allocate more
// than 64KB however malformed the stream is.
func ReadPacket(r io.Reader) (Packet, error) {
	var hdr [HeaderLen]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return Packet{}, err
	}
	control, cmd, total, err := ParseHeader(hdr[:])
	if err != nil {
		return Packet{}, err
	}
	p := Packet{Control: control, Cmd: cmd}
	if n := total - HeaderLen; n > 0 {
		p.Payload = make([]byte, n)
		if _, err := io.ReadFull(r, p.Payload); err != nil {
			return Packet{}, fmt.Errorf("matip: reading %d-byte body: %w", n, err)
		}
	}
	return p, nil
}

// WritePacket writes one packet.
func WritePacket(w io.Writer, p Packet) error {
	b, err := p.MarshalBinary()
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// DataPacket wraps a Type B message for transmission.
func DataPacket(payload []byte) Packet {
	return Packet{Control: false, Cmd: CmdData, Payload: payload}
}
