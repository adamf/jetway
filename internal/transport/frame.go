// Package transport carries message bytes between the gateway and its peers.
//
// It is deliberately ignorant of message content. Its whole job is to turn a
// byte stream into discrete messages and back, to reconnect when a link drops,
// and to hand every message to the pipeline exactly once per delivery.
package transport

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// ErrFrameTooLarge is returned when a declared frame exceeds the configured
// limit. A gateway must bound this: a corrupt length header on a long-lived
// link would otherwise become an allocation of whatever the peer said.
var ErrFrameTooLarge = errors.New("transport: frame exceeds maximum size")

// DefaultMaxFrame is a generous ceiling for airline messaging. PNL/ADL batches
// for a wide-body departure are the large case and stay well under this.
const DefaultMaxFrame = 4 << 20

// Framer splits a stream into messages.
type Framer interface {
	// ReadFrame returns the next complete message payload.
	ReadFrame(r *bufio.Reader) ([]byte, error)
	// WriteFrame writes one message payload.
	WriteFrame(w io.Writer, payload []byte) error
	// Name identifies the framing for logs and configuration.
	Name() string
}

// LengthPrefix frames messages with a fixed-width binary length header.
//
// This one type covers most carrier links. MATIP, IBM MQ bridges and the
// bespoke TCP framings carriers hand out in their interface control documents
// differ mainly in header width, byte order, and whether the count includes the
// header itself. Making those three things configuration rather than code is
// what lets a new link be onboarded without a release.
type LengthPrefix struct {
	// HeaderBytes is the width of the length field: 2 or 4.
	HeaderBytes int
	// LittleEndian selects byte order. Network links are overwhelmingly big
	// endian; the option exists because not all of them are.
	LittleEndian bool
	// Inclusive means the declared length counts the header bytes too.
	Inclusive bool
	// Max bounds a single frame. Zero uses DefaultMaxFrame.
	Max int
	// Label names this framing profile in logs.
	Label string
}

func (f LengthPrefix) Name() string {
	if f.Label != "" {
		return f.Label
	}
	return fmt.Sprintf("length-prefix/%dB", f.HeaderBytes)
}

func (f LengthPrefix) max() int {
	if f.Max <= 0 {
		return DefaultMaxFrame
	}
	return f.Max
}

func (f LengthPrefix) order() binary.ByteOrder {
	if f.LittleEndian {
		return binary.LittleEndian
	}
	return binary.BigEndian
}

func (f LengthPrefix) ReadFrame(r *bufio.Reader) ([]byte, error) {
	hdr := make([]byte, f.HeaderBytes)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, err
	}
	var n int
	switch f.HeaderBytes {
	case 2:
		n = int(f.order().Uint16(hdr))
	case 4:
		n = int(f.order().Uint32(hdr))
	default:
		return nil, fmt.Errorf("transport: unsupported header width %d", f.HeaderBytes)
	}
	if f.Inclusive {
		n -= f.HeaderBytes
	}
	if n < 0 {
		return nil, fmt.Errorf("transport: negative payload length %d", n)
	}
	if n > f.max() {
		return nil, fmt.Errorf("%w: %d > %d", ErrFrameTooLarge, n, f.max())
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

func (f LengthPrefix) WriteFrame(w io.Writer, payload []byte) error {
	n := len(payload)
	if n > f.max() {
		return fmt.Errorf("%w: %d > %d", ErrFrameTooLarge, n, f.max())
	}
	if f.Inclusive {
		n += f.HeaderBytes
	}
	hdr := make([]byte, f.HeaderBytes)
	switch f.HeaderBytes {
	case 2:
		if n > 0xFFFF {
			return fmt.Errorf("%w: %d does not fit a 2-byte header", ErrFrameTooLarge, n)
		}
		f.order().PutUint16(hdr, uint16(n))
	case 4:
		f.order().PutUint32(hdr, uint32(n))
	default:
		return fmt.Errorf("transport: unsupported header width %d", f.HeaderBytes)
	}
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

// DefaultFramer is the framing Jetway uses between its own components and the
// sensible starting point for a new link: a 4-byte big-endian payload length.
func DefaultFramer() Framer {
	return LengthPrefix{HeaderBytes: 4, Label: "jetway/4B-be"}
}

// MATIPProfile returns the length framing used by MATIP-style links.
//
// RFC 2351 defines a session layer above this framing that this profile does
// not implement. Treat it as a starting point to validate against the carrier's
// interface control document, not as a conformant MATIP implementation: several
// carriers run non-conforming variants and the header layout must be confirmed
// per link.
func MATIPProfile() Framer {
	return LengthPrefix{HeaderBytes: 2, Inclusive: true, Label: "matip-like/2B-be-inclusive"}
}

// Sentinel frames messages by a terminating byte sequence, which is how Type B
// traffic arrives on links that carry the classic teletype end-of-message.
type Sentinel struct {
	// Terminator ends a message and is included in the returned payload.
	Terminator []byte
	Max        int
	Label      string
}

func (f Sentinel) Name() string {
	if f.Label != "" {
		return f.Label
	}
	return "sentinel"
}

func (f Sentinel) ReadFrame(r *bufio.Reader) ([]byte, error) {
	max := f.Max
	if max <= 0 {
		max = DefaultMaxFrame
	}
	last := f.Terminator[len(f.Terminator)-1]
	var buf []byte
	for {
		chunk, err := r.ReadBytes(last)
		if err != nil {
			// Return what we have alongside the error so a truncated final
			// message can still be captured rather than silently discarded.
			if len(buf)+len(chunk) > 0 {
				return append(buf, chunk...), err
			}
			return nil, err
		}
		buf = append(buf, chunk...)
		if len(buf) > max {
			return buf, fmt.Errorf("%w: %d > %d", ErrFrameTooLarge, len(buf), max)
		}
		if len(buf) >= len(f.Terminator) &&
			string(buf[len(buf)-len(f.Terminator):]) == string(f.Terminator) {
			return buf, nil
		}
	}
}

func (f Sentinel) WriteFrame(w io.Writer, payload []byte) error {
	if _, err := w.Write(payload); err != nil {
		return err
	}
	// Only append the terminator when the payload does not already carry it.
	if len(payload) < len(f.Terminator) ||
		string(payload[len(payload)-len(f.Terminator):]) != string(f.Terminator) {
		if _, err := w.Write(f.Terminator); err != nil {
			return err
		}
	}
	return nil
}

// TypeBSentinel frames on the classic teletype end-of-message sequence.
func TypeBSentinel() Framer {
	return Sentinel{Terminator: []byte("\nNNNN\n"), Label: "typeb/NNNN"}
}
