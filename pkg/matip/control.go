package matip

import (
	"encoding/binary"
	"fmt"
)

// Origin says whether a session open came from a host or a gateway.
type Origin uint8

const (
	OriginHost    Origin = 0b00
	OriginGateway Origin = 0b01
)

func (o Origin) String() string {
	if o == OriginGateway {
		return "gateway"
	}
	return "host"
}

// SessionOpen is the SO control packet body for Type B traffic.
//
// The RFC gives its length as exactly 6 bytes without the host identifiers, or
// 10 bytes with them. Note that the prose placing those identifiers at "bytes
// 9,10 and 11,12" cannot be reconciled with a 10-byte packet; the bit diagram
// and the stated lengths agree with each other, so this follows those and puts
// them at offsets 6..9.
type SessionOpen struct {
	Coding Coding
	// Protection identifies the messaging responsibility transfer protocol.
	// ProtectionBATAP is the only value the RFC assigns.
	Protection uint8
	Origin     Origin
	// HasHLD reports whether the host identifiers are present.
	HasHLD       bool
	SenderHLD    uint16
	RecipientHLD uint16
}

// BFLAG bit layout. The low pair signals whether host identifiers follow; the
// high pair distinguishes a host from a gateway.
const (
	bflagHLDPresent  = 0b0010
	bflagOriginShift = 2
)

// Packet renders the session open as a control packet.
func (s SessionOpen) Packet() Packet {
	bflag := uint8(s.Origin) << bflagOriginShift
	n := 2
	if s.HasHLD {
		bflag |= bflagHLDPresent
		n = 6
	}
	body := make([]byte, n)
	body[0] = uint8(s.Coding) & 0x07
	body[1] = (s.Protection&0x0F)<<4 | bflag&0x0F
	if s.HasHLD {
		binary.BigEndian.PutUint16(body[2:4], s.SenderHLD)
		binary.BigEndian.PutUint16(body[4:6], s.RecipientHLD)
	}
	return Packet{Control: true, Cmd: CmdSessionOpen, Payload: body}
}

// ParseSessionOpen decodes an SO packet body.
func ParseSessionOpen(p Packet) (SessionOpen, error) {
	var s SessionOpen
	if !p.Control || p.Cmd != CmdSessionOpen {
		return s, fmt.Errorf("matip: not a session open packet: %s", p)
	}
	// 6 and 10 are the whole-packet lengths the RFC allows; the header is 4.
	if len(p.Payload) != 2 && len(p.Payload) != 6 {
		return s, fmt.Errorf("matip: session open is %d bytes, the standard allows 6 or 10",
			HeaderLen+len(p.Payload))
	}
	if p.Payload[0]&0xF8 != 0 {
		return s, fmt.Errorf("matip: reserved bits set in the coding byte 0x%02X", p.Payload[0])
	}
	s.Coding = Coding(p.Payload[0] & 0x07)
	s.Protection = p.Payload[1] >> 4
	bflag := p.Payload[1] & 0x0F
	s.Origin = Origin(bflag >> bflagOriginShift)
	s.HasHLD = bflag&bflagHLDPresent != 0
	if s.HasHLD != (len(p.Payload) == 6) {
		return s, fmt.Errorf("matip: session open flags say identifiers present=%t but the packet is %d bytes",
			s.HasHLD, HeaderLen+len(p.Payload))
	}
	if s.HasHLD {
		s.SenderHLD = binary.BigEndian.Uint16(p.Payload[2:4])
		s.RecipientHLD = binary.BigEndian.Uint16(p.Payload[4:6])
	}
	return s, nil
}

// RefusalCause explains why a session open was refused.
type RefusalCause uint8

const (
	// CauseNoTrafficMatch means the two sides do not agree on traffic type.
	CauseNoTrafficMatch RefusalCause = 0b000001
	// CauseIncoherentHeader means the session open header did not make sense.
	CauseIncoherentHeader RefusalCause = 0b000010
	// CauseProtectionMismatch means the protection mechanisms differ.
	CauseProtectionMismatch RefusalCause = 0b000011
)

func (c RefusalCause) String() string {
	switch c {
	case CauseNoTrafficMatch:
		return "no traffic type matching between sender and recipient"
	case CauseIncoherentHeader:
		return "information in the session open header is incoherent"
	case CauseProtectionMismatch:
		return "protection mechanisms differ"
	}
	return fmt.Sprintf("unassigned cause %06b", uint8(c))
}

// refuseFlag marks an open confirm as a refusal. Acceptance is a zero byte;
// refusal sets this bit and carries a cause in the low six bits.
const refuseFlag = 0x40

// AcceptPacket builds an open confirm accepting the session.
func AcceptPacket() Packet {
	return Packet{Control: true, Cmd: CmdOpenConfirm, Payload: []byte{0x00}}
}

// RefusePacket builds an open confirm refusing the session.
func RefusePacket(c RefusalCause) Packet {
	return Packet{Control: true, Cmd: CmdOpenConfirm, Payload: []byte{refuseFlag | byte(c)&0x3F}}
}

// ParseOpenConfirm decodes an open confirm, reporting whether the session was
// accepted and, when not, why.
func ParseOpenConfirm(p Packet) (accepted bool, cause RefusalCause, err error) {
	if !p.Control || p.Cmd != CmdOpenConfirm {
		return false, 0, fmt.Errorf("matip: not an open confirm packet: %s", p)
	}
	if len(p.Payload) != 1 {
		return false, 0, fmt.Errorf("matip: open confirm is %d bytes, the standard requires 5",
			HeaderLen+len(p.Payload))
	}
	b := p.Payload[0]
	if b == 0x00 {
		return true, 0, nil
	}
	if b&refuseFlag == 0 {
		return false, 0, fmt.Errorf("matip: open confirm byte 0x%02X is neither an acceptance nor a refusal", b)
	}
	return false, RefusalCause(b & 0x3F), nil
}

// CloseCause explains a session closure. Zero is a normal close; values from
// 0b10000100 upward are application defined.
type CloseCause uint8

// CloseNormal is an orderly shutdown.
const CloseNormal CloseCause = 0x00

func (c CloseCause) String() string {
	if c == CloseNormal {
		return "normal close"
	}
	if c >= 0b10000100 {
		return fmt.Sprintf("application-defined cause 0x%02X", uint8(c))
	}
	return fmt.Sprintf("reserved cause 0x%02X", uint8(c))
}

// ClosePacket builds a session close.
func ClosePacket(c CloseCause) Packet {
	return Packet{Control: true, Cmd: CmdSessionClose, Payload: []byte{byte(c)}}
}

// ParseSessionClose decodes a session close.
func ParseSessionClose(p Packet) (CloseCause, error) {
	if !p.Control || p.Cmd != CmdSessionClose {
		return 0, fmt.Errorf("matip: not a session close packet: %s", p)
	}
	if len(p.Payload) != 1 {
		return 0, fmt.Errorf("matip: session close is %d bytes, the standard requires 5",
			HeaderLen+len(p.Payload))
	}
	return CloseCause(p.Payload[0]), nil
}
