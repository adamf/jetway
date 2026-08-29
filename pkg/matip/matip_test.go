package matip

import (
	"bytes"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"testing"
	"testing/iotest"
	"time"
)

// The header is five zero bits, a three-bit version, a control flag, a
// seven-bit command, and a length covering the whole packet including the
// header. These are the byte patterns RFC 2351's diagrams describe.
func TestHeaderLayout(t *testing.T) {
	cases := []struct {
		name string
		pkt  Packet
		want string
	}{
		{"data, empty", DataPacket(nil), "01000004"},
		{"data with payload", DataPacket([]byte("QU LHRRMBA")), "0100000e" + hex.EncodeToString([]byte("QU LHRRMBA"))},
		{"session close, normal", ClosePacket(CloseNormal), "01fc000500"},
		{"open confirm, accept", AcceptPacket(), "01fd000500"},
		{"open confirm, refuse", RefusePacket(CauseNoTrafficMatch), "01fd000541"},
	}
	for _, c := range cases {
		b, err := c.pkt.MarshalBinary()
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := hex.EncodeToString(b); got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, c.want)
		}
		// The length field must equal the whole packet.
		if len(b) != int(uint16(b[2])<<8|uint16(b[3])) {
			t.Errorf("%s: length field %d does not match the %d bytes written",
				c.name, uint16(b[2])<<8|uint16(b[3]), len(b))
		}
	}
}

func TestControlFlagAndCommand(t *testing.T) {
	for _, cmd := range []uint8{CmdSessionOpen, CmdOpenConfirm, CmdSessionClose} {
		b, _ := Packet{Control: true, Cmd: cmd, Payload: []byte{0}}.MarshalBinary()
		control, got, _, err := ParseHeader(b)
		if err != nil {
			t.Fatalf("cmd 0x%02X: %v", cmd, err)
		}
		if !control {
			t.Errorf("cmd 0x%02X: control flag lost", cmd)
		}
		if got != cmd {
			t.Errorf("cmd = 0x%02X, want 0x%02X", got, cmd)
		}
	}
	b, _ := DataPacket([]byte("x")).MarshalBinary()
	if control, cmd, _, _ := ParseHeader(b); control || cmd != CmdData {
		t.Errorf("data packet decoded as control=%v cmd=0x%02X", control, cmd)
	}
}

// A packet claiming any version other than 001 is invalid, and the reserved
// high bits must be clear.
func TestVersionIsEnforced(t *testing.T) {
	b, _ := DataPacket([]byte("x")).MarshalBinary()
	for _, bad := range []byte{0x00, 0x02, 0x07, 0x09, 0xFF} {
		corrupt := append([]byte(nil), b...)
		corrupt[0] = bad
		if _, _, _, err := ParseHeader(corrupt); !errors.Is(err, ErrBadVersion) {
			t.Errorf("first byte 0x%02X: err = %v, want ErrBadVersion", bad, err)
		}
	}
}

func TestLengthShorterThanHeaderRejected(t *testing.T) {
	if _, _, _, err := ParseHeader([]byte{0x01, 0x00, 0x00, 0x03}); !errors.Is(err, ErrBadLength) {
		t.Errorf("err = %v, want ErrBadLength", err)
	}
}

func TestReadPacketRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	msgs := [][]byte{
		[]byte("QU LHRRMBA\r\n.LONRM1J 121430\r\nBA0175Y15JUNLHRJFKNN1\r\n"),
		{0x00, 0xFF, 0x01},
		nil,
	}
	for _, m := range msgs {
		if err := WritePacket(&buf, DataPacket(m)); err != nil {
			t.Fatal(err)
		}
	}
	if err := WritePacket(&buf, ClosePacket(CloseNormal)); err != nil {
		t.Fatal(err)
	}
	for i, want := range msgs {
		p, err := ReadPacket(&buf)
		if err != nil {
			t.Fatalf("packet %d: %v", i, err)
		}
		if !p.IsData() {
			t.Errorf("packet %d is not data: %s", i, p)
		}
		if !bytes.Equal(p.Payload, want) {
			t.Errorf("packet %d payload changed", i)
		}
	}
	p, err := ReadPacket(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if cause, err := ParseSessionClose(p); err != nil || cause != CloseNormal {
		t.Errorf("close = %v, %v", cause, err)
	}
	if _, err := ReadPacket(&buf); !errors.Is(err, io.EOF) {
		t.Errorf("expected EOF after the last packet, got %v", err)
	}
}

// A frame arriving in pieces is normal on a socket; a reader that assumes one
// read per packet works in tests and corrupts production traffic.
func TestReadPacketAcrossSplitReads(t *testing.T) {
	payload := bytes.Repeat([]byte("BA0175Y15JUN"), 400)
	var buf bytes.Buffer
	WritePacket(&buf, DataPacket(payload)) //nolint:errcheck
	p, err := ReadPacket(iotest.OneByteReader(bytes.NewReader(buf.Bytes())))
	if err != nil {
		t.Fatalf("ReadPacket: %v", err)
	}
	if !bytes.Equal(p.Payload, payload) {
		t.Error("payload corrupted across split reads")
	}
}

// The 16-bit length field is the hard ceiling on a Type B message carried in
// one packet.
func TestPayloadCeiling(t *testing.T) {
	if _, err := DataPacket(bytes.Repeat([]byte("A"), MaxPayload)).MarshalBinary(); err != nil {
		t.Errorf("a maximum-size payload must encode: %v", err)
	}
	if _, err := DataPacket(bytes.Repeat([]byte("A"), MaxPayload+1)).MarshalBinary(); !errors.Is(err, ErrPayloadTooBig) {
		t.Errorf("err = %v, want ErrPayloadTooBig", err)
	}
}

// Session open is 6 bytes without host identifiers and 10 with them.
func TestSessionOpenLengths(t *testing.T) {
	short := SessionOpen{Coding: CodingASCII, Protection: ProtectionBATAP, Origin: OriginHost}
	b, err := short.Packet().MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 6 {
		t.Errorf("session open without identifiers is %d bytes, want 6", len(b))
	}

	long := short
	long.HasHLD = true
	long.SenderHLD = 0x1234
	long.RecipientHLD = 0xABCD
	long.Origin = OriginGateway
	b, err = long.Packet().MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 10 {
		t.Errorf("session open with identifiers is %d bytes, want 10", len(b))
	}

	got, err := ParseSessionOpen(long.Packet())
	if err != nil {
		t.Fatalf("ParseSessionOpen: %v", err)
	}
	if got != long {
		t.Errorf("round trip:\n got %+v\nwant %+v", got, long)
	}
	if got, err := ParseSessionOpen(short.Packet()); err != nil || got != short {
		t.Errorf("short round trip: %+v, %v", got, err)
	}
}

// The flag saying identifiers are present must agree with the packet length,
// or the peer and we disagree about where the fields are.
func TestSessionOpenFlagLengthMismatchRejected(t *testing.T) {
	p := SessionOpen{Coding: CodingASCII, HasHLD: true, SenderHLD: 1, RecipientHLD: 2}.Packet()
	p.Payload = p.Payload[:2] // claims identifiers, but truncated
	if _, err := ParseSessionOpen(p); err == nil {
		t.Error("expected the flag/length mismatch to be rejected")
	}
}

func TestCodingValues(t *testing.T) {
	// The RFC assigns these three-bit values; xx1 is reserved.
	for coding, want := range map[Coding]uint8{
		CodingBaudot: 0b000, CodingIPARS: 0b010, CodingASCII: 0b100, CodingEBCDIC: 0b110,
	} {
		if uint8(coding) != want {
			t.Errorf("%v = %03b, want %03b", coding, uint8(coding), want)
		}
		s := SessionOpen{Coding: coding}
		got, err := ParseSessionOpen(s.Packet())
		if err != nil || got.Coding != coding {
			t.Errorf("coding %v did not survive: %v, %v", coding, got.Coding, err)
		}
	}
}

func TestOpenConfirm(t *testing.T) {
	ok, _, err := ParseOpenConfirm(AcceptPacket())
	if err != nil || !ok {
		t.Errorf("accept: %v, %v", ok, err)
	}
	for _, c := range []RefusalCause{CauseNoTrafficMatch, CauseIncoherentHeader, CauseProtectionMismatch} {
		ok, got, err := ParseOpenConfirm(RefusePacket(c))
		if err != nil {
			t.Fatalf("refuse %v: %v", c, err)
		}
		if ok {
			t.Errorf("refusal %v decoded as acceptance", c)
		}
		if got != c {
			t.Errorf("cause = %v, want %v", got, c)
		}
	}
	// A body that is neither zero nor flagged as a refusal is malformed.
	bad := Packet{Control: true, Cmd: CmdOpenConfirm, Payload: []byte{0x01}}
	if _, _, err := ParseOpenConfirm(bad); err == nil {
		t.Error("expected an ambiguous open confirm to be rejected")
	}
}

func TestParsersRejectWrongPacketType(t *testing.T) {
	data := DataPacket([]byte("x"))
	if _, err := ParseSessionOpen(data); err == nil {
		t.Error("ParseSessionOpen accepted a data packet")
	}
	if _, _, err := ParseOpenConfirm(data); err == nil {
		t.Error("ParseOpenConfirm accepted a data packet")
	}
	if _, err := ParseSessionClose(data); err == nil {
		t.Error("ParseSessionClose accepted a data packet")
	}
}

// --- session handshake over a real socket ---

func pipeSessions(t *testing.T, client, server Config, approve Approver) (*Session, *Session, error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	type res struct {
		s   *Session
		err error
	}
	ch := make(chan res, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			ch <- res{nil, err}
			return
		}
		s, err := Accept(c, server, approve)
		ch <- res{s, err}
	}()

	conn, err := net.DialTimeout("tcp", ln.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	cs, cerr := Dial(conn, client)
	r := <-ch
	if cs != nil {
		t.Cleanup(func() { cs.Close(CloseNormal) })
	}
	if r.s != nil {
		t.Cleanup(func() { r.s.Close(CloseNormal) })
	}
	if cerr != nil {
		return nil, r.s, cerr
	}
	return cs, r.s, r.err
}

func TestSessionHandshakeAndDataExchange(t *testing.T) {
	cfg := Config{Coding: CodingASCII, Protection: ProtectionBATAP, Origin: OriginGateway,
		HasHLD: true, SenderHLD: 0x0101, RecipientHLD: 0x0202}
	client, server, err := pipeSessions(t, cfg, Config{Coding: CodingASCII}, nil)
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}

	// The answering side learns what the initiator declared.
	if got := server.Remote(); got.SenderHLD != 0x0101 || got.RecipientHLD != 0x0202 ||
		got.Origin != OriginGateway || got.Coding != CodingASCII {
		t.Errorf("declared characteristics lost: %+v", got)
	}

	msg := []byte("QU LHRRMBA\r\n.LONRM1J 121430\r\nBA0175Y15JUNLHRJFKNN1\r\n")
	if err := client.Send(msg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	got, err := server.Receive()
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if !bytes.Equal(got, msg) {
		t.Errorf("payload changed in transit")
	}

	// And back the other way: either side may send.
	reply := []byte("QU LONRM1J\r\n.LHRRMBA 121431\r\nBA0175Y15JUNLHRJFKKK1\r\n")
	if err := server.Send(reply); err != nil {
		t.Fatalf("reply Send: %v", err)
	}
	got, err = client.Receive()
	if err != nil || !bytes.Equal(got, reply) {
		t.Errorf("reply: %q, %v", got, err)
	}
}

// The handshake exists to establish that both sides agree; a coding mismatch
// must be refused rather than producing traffic neither can read.
func TestSessionRefusedOnCodingMismatch(t *testing.T) {
	_, _, err := pipeSessions(t, Config{Coding: CodingEBCDIC}, Config{Coding: CodingASCII}, nil)
	var refused *ErrRefused
	if !errors.As(err, &refused) {
		t.Fatalf("err = %v, want ErrRefused", err)
	}
	if refused.Cause != CauseNoTrafficMatch {
		t.Errorf("cause = %v, want no traffic match", refused.Cause)
	}
}

func TestSessionRefusedByApprover(t *testing.T) {
	approve := func(so SessionOpen) RefusalCause {
		if so.SenderHLD != 0x1234 {
			return CauseIncoherentHeader
		}
		return 0
	}
	_, _, err := pipeSessions(t,
		Config{Coding: CodingASCII, HasHLD: true, SenderHLD: 0x9999},
		Config{Coding: CodingASCII}, approve)
	var refused *ErrRefused
	if !errors.As(err, &refused) || refused.Cause != CauseIncoherentHeader {
		t.Fatalf("err = %v, want a refusal for an incoherent header", err)
	}
}

// A session close must surface as a distinguishable end, not a generic read
// error, so a gateway can tell an orderly shutdown from a dropped circuit.
func TestSessionCloseIsReported(t *testing.T) {
	client, server, err := pipeSessions(t, Config{Coding: CodingASCII}, Config{Coding: CodingASCII}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Close(CloseNormal); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, err = server.Receive()
	var closed *ErrClosed
	if !errors.As(err, &closed) {
		t.Fatalf("err = %v, want ErrClosed", err)
	}
	if closed.Cause != CloseNormal {
		t.Errorf("cause = %v, want normal", closed.Cause)
	}
}

// Simultaneous opens are broken by IP address: the session from the lower
// address is ignored.
func TestCollisionTieBreak(t *testing.T) {
	lo := &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 1}
	hi := &net.TCPAddr{IP: net.ParseIP("10.0.0.2"), Port: 1}
	if !IgnoreOnCollision(lo, hi) {
		t.Error("the lower address should yield")
	}
	if IgnoreOnCollision(hi, lo) {
		t.Error("the higher address should not yield")
	}
	if IgnoreOnCollision(lo, lo) {
		t.Error("equal addresses should not yield")
	}
}
