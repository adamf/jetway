package transport

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"
)

// Handler receives one inbound message from a link.
//
// Returning an error does not drop the message: the caller has already made it
// durable. The error is logged and the link stays up, because a message the
// pipeline could not handle is a reason to inspect that message, not a reason
// to disconnect a carrier.
type Handler func(ctx context.Context, peer string, raw []byte) error

// Hello is the identification frame exchanged when a link opens.
//
// Real carrier links identify the peer out of band, by the address on the leased
// circuit or by the credentials on the TLS session. This handshake stands in
// for that so a demo can run many peers over loopback. It is not authentication
// and must not be used as such: bind identity to the transport's own
// credentials before putting a link into production.
type Hello struct {
	Peer   string `json:"peer"`
	Role   string `json:"role"`
	Format string `json:"format"`
}

// Link is one open, bidirectional session with a peer.
type Link struct {
	Peer   string
	Format string

	conn   net.Conn
	framer Framer
	log    *slog.Logger

	mu     sync.Mutex
	closed bool
	out    *Outbox
}

// newLink wires a connection to its outbox. A write failure closes the
// connection, which ends the read loop, which is how both sides learn.
func newLink(peer, format string, conn net.Conn, framer Framer, log *slog.Logger) *Link {
	l := &Link{Peer: peer, Format: format, conn: conn, framer: framer, log: log}
	l.out = NewOutbox(OutboxDepth, func(raw []byte) error {
		if err := conn.SetWriteDeadline(time.Now().Add(30 * time.Second)); err != nil {
			return err
		}
		return framer.WriteFrame(conn, raw)
	}, func(err error) {
		log.Warn("link write failed; closing", "peer", peer, "err", err)
		l.Close()
	})
	l.out.Peer = peer
	return l
}

// Send queues one message for the link's writer. It is safe for concurrent
// use; it returns ErrCongested when the queue has been full for
// SendTimeout, and an error naming the link once it is closed.
func (l *Link) Send(raw []byte) error {
	l.mu.Lock()
	closed := l.closed
	l.mu.Unlock()
	if closed {
		return fmt.Errorf("transport: link to %s is closed", l.Peer)
	}
	return l.out.Send(raw)
}

// Queued reports how many frames wait to be written on this link.
func (l *Link) Queued() int { return l.out.Depth() }

// Close shuts the link down.
func (l *Link) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	l.out.Close()
	return l.conn.Close()
}

// serve reads frames until the connection ends.
func (l *Link) serve(ctx context.Context, h Handler) {
	r := bufio.NewReaderSize(l.conn, 64<<10)
	for {
		if ctx.Err() != nil {
			return
		}
		raw, err := l.framer.ReadFrame(r)
		if len(raw) > 0 {
			if herr := h(ctx, l.Peer, raw); herr != nil {
				l.log.Error("handler failed", "peer", l.Peer, "err", herr)
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) && ctx.Err() == nil {
				l.log.Warn("link read ended", "peer", l.Peer, "err", err)
			}
			return
		}
	}
}

// Server accepts inbound links.
type Server struct {
	Addr   string
	Framer Framer
	Log    *slog.Logger
	// OnMessage handles every message received on any link.
	OnMessage Handler
	// OnConnect and OnDisconnect report link lifecycle for the console.
	OnConnect    func(peer, format string)
	OnDisconnect func(peer string)

	mu    sync.RWMutex
	links map[string]*Link
	ln    net.Listener
}

// Listen binds the server's address.
func (s *Server) Listen() error {
	ln, err := net.Listen("tcp", s.Addr)
	if err != nil {
		return err
	}
	s.ln = ln
	s.Addr = ln.Addr().String()
	s.initLinks()
	return nil
}

// initLinks prepares the link table under the lock. Doing it lazily and
// unguarded races with Peers and Send, which may be called from another
// goroutine before the first connection ever arrives.
func (s *Server) initLinks() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.links == nil {
		s.links = map[string]*Link{}
	}
}

// Serve accepts connections until ctx is cancelled. Listen must be called first.
func (s *Server) Serve(ctx context.Context) error {
	if s.ln == nil {
		if err := s.Listen(); err != nil {
			return err
		}
	}
	s.initLinks()
	go func() { <-ctx.Done(); s.ln.Close() }()

	for {
		conn, err := s.ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go s.handle(ctx, conn)
	}
}

func (s *Server) handle(ctx context.Context, conn net.Conn) {
	// The same promise the client makes: when the context ends, idle inbound
	// links close rather than lingering blocked in read.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	r := bufio.NewReaderSize(conn, 64<<10)
	if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		conn.Close()
		return
	}
	raw, err := s.Framer.ReadFrame(r)
	if err != nil {
		s.Log.Warn("link handshake failed", "err", err)
		conn.Close()
		return
	}
	var hello Hello
	if err := json.Unmarshal(raw, &hello); err != nil || hello.Peer == "" {
		s.Log.Warn("link sent an unusable hello", "err", err)
		conn.Close()
		return
	}
	// Clear the handshake deadline: sessions are long lived and idle for long
	// stretches between bookings.
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		conn.Close()
		return
	}

	l := newLink(hello.Peer, hello.Format, conn, s.Framer, s.Log)
	s.mu.Lock()
	if prev := s.links[hello.Peer]; prev != nil {
		prev.Close()
	}
	s.links[hello.Peer] = l
	s.mu.Unlock()

	s.Log.Info("link up", "peer", hello.Peer, "role", hello.Role, "format", hello.Format,
		"remote", conn.RemoteAddr().String())
	if s.OnConnect != nil {
		s.OnConnect(hello.Peer, hello.Format)
	}

	l.serve(ctx, s.OnMessage)

	s.mu.Lock()
	if s.links[hello.Peer] == l {
		delete(s.links, hello.Peer)
	}
	s.mu.Unlock()
	l.Close()
	s.Log.Info("link down", "peer", hello.Peer)
	if s.OnDisconnect != nil {
		s.OnDisconnect(hello.Peer)
	}
}

// Send delivers a message to a named peer.
func (s *Server) Send(ctx context.Context, peer string, raw []byte) error {
	s.mu.RLock()
	l := s.links[peer]
	s.mu.RUnlock()
	if l == nil {
		return fmt.Errorf("transport: no link to %q", peer)
	}
	return l.Send(raw)
}

// Peers lists the currently connected peers.
func (s *Server) Peers() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.links))
	for p := range s.links {
		out = append(out, p)
	}
	return out
}

// Close stops the listener and every link.
func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, l := range s.links {
		l.Close()
	}
	if s.ln != nil {
		return s.ln.Close()
	}
	return nil
}

// Client maintains an outbound link with automatic reconnection.
type Client struct {
	Addr      string
	Hello     Hello
	Framer    Framer
	Log       *slog.Logger
	OnMessage Handler
	// OnUp is called each time the link is established.
	OnUp func()
	// SkipHello suppresses the identification frame. Set it when connecting to
	// a listener that identifies peers from the transport itself -- a client
	// certificate or the circuit -- which is what a real link does. The hello
	// exists only for the demo handshake server.
	SkipHello bool

	mu   sync.Mutex
	link *Link
}

// Run dials and keeps the link up until ctx is cancelled.
//
// Reconnection backs off to a ceiling rather than retrying immediately.
// A carrier restarting its front end should see a link come back promptly, not
// a connection storm from every gateway that was attached to it.
func (c *Client) Run(ctx context.Context) error {
	backoff := 200 * time.Millisecond
	const maxBackoff = 15 * time.Second
	for {
		if ctx.Err() != nil {
			return nil
		}
		err := c.session(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			c.Log.Warn("link session ended", "addr", c.Addr, "err", err, "retry_in", backoff)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func (c *Client) session(ctx context.Context) error {
	d := net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", c.Addr)
	if err != nil {
		return err
	}
	// serve only notices cancellation between frames, and an idle link sees no
	// frames. Closing the conn is what actually takes a quiet link down.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if !c.SkipHello {
		hello, err := json.Marshal(c.Hello)
		if err != nil {
			conn.Close()
			return err
		}
		if err := c.Framer.WriteFrame(conn, hello); err != nil {
			conn.Close()
			return err
		}
	}
	l := newLink(c.Hello.Peer, c.Hello.Format, conn, c.Framer, c.Log)
	c.mu.Lock()
	c.link = l
	c.mu.Unlock()
	c.Log.Info("link up", "addr", c.Addr, "as", c.Hello.Peer)
	if c.OnUp != nil {
		c.OnUp()
	}

	l.serve(ctx, c.OnMessage)

	c.mu.Lock()
	if c.link == l {
		c.link = nil
	}
	c.mu.Unlock()
	l.Close()
	return nil
}

// Send writes a message on the current link.
func (c *Client) Send(ctx context.Context, peer string, raw []byte) error {
	c.mu.Lock()
	l := c.link
	c.mu.Unlock()
	if l == nil {
		return fmt.Errorf("transport: link to %s is not up", c.Addr)
	}
	return l.Send(raw)
}
