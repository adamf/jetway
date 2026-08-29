package matip

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

// Config describes the traffic characteristics both sides must agree on.
type Config struct {
	Coding     Coding
	Protection uint8
	Origin     Origin
	// HLD identifiers, when the link uses them.
	HasHLD       bool
	SenderHLD    uint16
	RecipientHLD uint16
	// HandshakeTimeout bounds the session open exchange.
	HandshakeTimeout time.Duration
	// WriteTimeout bounds a single write.
	WriteTimeout time.Duration
}

func (c Config) withDefaults() Config {
	if c.Coding == 0 && c.Protection == 0 {
		// ASCII is what Type B over IP carries in practice; baudot is the
		// zero value and would be a surprising default.
		c.Coding = CodingASCII
	}
	if c.HandshakeTimeout <= 0 {
		c.HandshakeTimeout = 20 * time.Second
	}
	if c.WriteTimeout <= 0 {
		c.WriteTimeout = 30 * time.Second
	}
	return c
}

func (c Config) sessionOpen() SessionOpen {
	return SessionOpen{
		Coding: c.Coding, Protection: c.Protection, Origin: c.Origin,
		HasHLD: c.HasHLD, SenderHLD: c.SenderHLD, RecipientHLD: c.RecipientHLD,
	}
}

// ErrRefused is returned when a peer refuses the session.
type ErrRefused struct{ Cause RefusalCause }

func (e *ErrRefused) Error() string { return "matip: session refused: " + e.Cause.String() }

// ErrClosed is returned when the peer closed the session.
type ErrClosed struct{ Cause CloseCause }

func (e *ErrClosed) Error() string { return "matip: session closed by peer: " + e.Cause.String() }

// Session is an open MATIP session over a TCP connection.
//
// There is no keep-alive at the MATIP level; the RFC leaves liveness to TCP,
// so an idle session stays up until TCP notices otherwise.
type Session struct {
	conn   net.Conn
	r      *bufio.Reader
	cfg    Config
	remote SessionOpen

	mu     sync.Mutex
	closed bool
}

// Remote returns the characteristics the peer declared when opening.
func (s *Session) Remote() SessionOpen { return s.remote }

// Conn exposes the underlying connection, for addresses and deadlines.
func (s *Session) Conn() net.Conn { return s.conn }

// Dial performs the session open handshake as the initiating side.
func Dial(conn net.Conn, cfg Config) (*Session, error) {
	cfg = cfg.withDefaults()
	s := &Session{conn: conn, r: bufio.NewReaderSize(conn, 64<<10), cfg: cfg}

	if err := conn.SetDeadline(time.Now().Add(cfg.HandshakeTimeout)); err != nil {
		return nil, err
	}
	if err := WritePacket(conn, cfg.sessionOpen().Packet()); err != nil {
		return nil, fmt.Errorf("matip: sending session open: %w", err)
	}
	p, err := ReadPacket(s.r)
	if err != nil {
		return nil, fmt.Errorf("matip: awaiting open confirm: %w", err)
	}
	accepted, cause, err := ParseOpenConfirm(p)
	if err != nil {
		return nil, err
	}
	if !accepted {
		return nil, &ErrRefused{Cause: cause}
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}
	return s, nil
}

// Approver decides whether to accept an incoming session. Returning a non-zero
// cause refuses it.
type Approver func(SessionOpen) RefusalCause

// Accept performs the handshake as the answering side.
func Accept(conn net.Conn, cfg Config, approve Approver) (*Session, error) {
	cfg = cfg.withDefaults()
	s := &Session{conn: conn, r: bufio.NewReaderSize(conn, 64<<10), cfg: cfg}

	if err := conn.SetDeadline(time.Now().Add(cfg.HandshakeTimeout)); err != nil {
		return nil, err
	}
	p, err := ReadPacket(s.r)
	if err != nil {
		return nil, fmt.Errorf("matip: awaiting session open: %w", err)
	}
	so, err := ParseSessionOpen(p)
	if err != nil {
		// The peer sent something that is not a session open. Tell it why
		// rather than dropping the connection silently.
		WritePacket(conn, RefusePacket(CauseIncoherentHeader)) //nolint:errcheck
		return nil, err
	}
	s.remote = so

	cause := RefusalCause(0)
	if approve != nil {
		cause = approve(so)
	}
	if cause == 0 && so.Coding != cfg.Coding {
		// Both sides must agree on the coding, which is exactly what the
		// handshake exists to establish.
		cause = CauseNoTrafficMatch
	}
	if cause != 0 {
		WritePacket(conn, RefusePacket(cause)) //nolint:errcheck
		return nil, &ErrRefused{Cause: cause}
	}
	if err := WritePacket(conn, AcceptPacket()); err != nil {
		return nil, fmt.Errorf("matip: sending open confirm: %w", err)
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}
	return s, nil
}

// Send transmits one Type B message as a data packet.
func (s *Session) Send(payload []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return net.ErrClosed
	}
	if err := s.conn.SetWriteDeadline(time.Now().Add(s.cfg.WriteTimeout)); err != nil {
		return err
	}
	return WritePacket(s.conn, DataPacket(payload))
}

// Receive returns the next Type B message.
//
// Control packets are handled here rather than surfaced: a session close ends
// the session, and a session open on an already-open session clears it, which
// is what the RFC requires.
func (s *Session) Receive() ([]byte, error) {
	for {
		p, err := ReadPacket(s.r)
		if err != nil {
			return nil, err
		}
		if p.IsData() {
			return p.Payload, nil
		}
		if !p.Control {
			return nil, fmt.Errorf("matip: unexpected non-control command 0x%02X", p.Cmd)
		}
		switch p.Cmd {
		case CmdSessionClose:
			cause, err := ParseSessionClose(p)
			if err != nil {
				return nil, err
			}
			return nil, &ErrClosed{Cause: cause}
		case CmdSessionOpen:
			// A session open on an open session clears the existing one.
			return nil, errors.New("matip: peer reopened the session, clearing this one")
		case CmdOpenConfirm:
			// Stray confirm after the handshake; ignore rather than tear down.
			continue
		default:
			return nil, fmt.Errorf("matip: unknown control command 0x%02X", p.Cmd)
		}
	}
}

// Close sends a session close and closes the connection.
//
// Closing the TCP connection is not required when closing a MATIP session, but
// doing both is what a gateway wants: an idle socket with no session on it is
// a resource nobody is watching.
func (s *Session) Close(cause CloseCause) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.conn.SetWriteDeadline(time.Now().Add(5 * time.Second)) //nolint:errcheck
	WritePacket(s.conn, ClosePacket(cause))                  //nolint:errcheck
	return s.conn.Close()
}

// IgnoreOnCollision implements the RFC's tie-break for simultaneous opens: the
// session opened from the lower IP address is ignored.
func IgnoreOnCollision(local, remote net.Addr) bool {
	lh, _, err1 := net.SplitHostPort(local.String())
	rh, _, err2 := net.SplitHostPort(remote.String())
	if err1 != nil || err2 != nil {
		return false
	}
	li, ri := net.ParseIP(lh), net.ParseIP(rh)
	if li == nil || ri == nil {
		return false
	}
	return compareIP(li, ri) < 0
}

func compareIP(a, b net.IP) int {
	a16, b16 := a.To16(), b.To16()
	for i := range a16 {
		switch {
		case a16[i] < b16[i]:
			return -1
		case a16[i] > b16[i]:
			return 1
		}
	}
	return 0
}
