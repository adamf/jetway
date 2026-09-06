package matip

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

// fakeConn feeds a byte string to the session code as if a peer had sent it.
type fakeConn struct {
	io.Reader
}

func (fakeConn) Write(b []byte) (int, error)      { return len(b), nil }
func (fakeConn) Close() error                     { return nil }
func (fakeConn) LocalAddr() net.Addr              { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 351} }
func (fakeConn) RemoteAddr() net.Addr             { return &net.TCPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 4000} }
func (fakeConn) SetDeadline(time.Time) error      { return nil }
func (fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (fakeConn) SetWriteDeadline(time.Time) error { return nil }

func FuzzPackets(f *testing.F) {
	so, _ := SessionOpen{Coding: CodingASCII, Protection: ProtectionBATAP}.Packet().MarshalBinary()
	soHLD, _ := SessionOpen{Coding: CodingASCII, HasHLD: true, SenderHLD: 1, RecipientHLD: 2}.Packet().MarshalBinary()
	data, _ := DataPacket([]byte("QU LHRRMBA\n.LONRM1J 011200\nHELLO\n")).MarshalBinary()
	acc, _ := AcceptPacket().MarshalBinary()
	ref, _ := RefusePacket(CauseNoTrafficMatch).MarshalBinary()
	cls, _ := ClosePacket(CloseNormal).MarshalBinary()
	f.Add(append(append(append([]byte{}, so...), data...), cls...))
	f.Add(append(append([]byte{}, soHLD...), data...))
	f.Add(append(append([]byte{}, acc...), ref...))
	f.Add([]byte{0x01, 0x80 | CmdSessionOpen, 0x00, 0x03})
	f.Add([]byte{0x01, 0x00, 0xFF, 0xFF})
	f.Fuzz(func(t *testing.T, raw []byte) {
		_, _, _, _ = ParseHeader(raw)
		r := bytes.NewReader(raw)
		for {
			p, err := ReadPacket(r)
			if err != nil {
				break
			}
			_ = p.String()
			_ = p.IsData()
			if s, err := ParseSessionOpen(p); err == nil {
				_ = s.Coding.String()
				_ = s.Origin.String()
				_, _ = s.Packet().MarshalBinary()
			}
			if _, c, err := ParseOpenConfirm(p); err == nil {
				_ = c.String()
			}
			if c, err := ParseSessionClose(p); err == nil {
				_ = c.String()
			}
			_, _ = p.MarshalBinary()
		}
		// The answering side of the handshake, then the receive loop.
		sess, err := Accept(fakeConn{bytes.NewReader(raw)}, Config{Coding: CodingASCII}, nil)
		if err != nil {
			return
		}
		_ = sess.Remote()
		for i := 0; i < 64; i++ {
			if _, err := sess.Receive(); err != nil {
				break
			}
		}
		_ = sess.Close(CloseNormal)
	})
}
