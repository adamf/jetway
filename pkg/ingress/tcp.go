package ingress

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/adamf/jetway/pkg/config"
	"github.com/adamf/jetway/pkg/metrics"
	"github.com/adamf/jetway/pkg/transport"
)

// framer is the subset of transport.Framer this package needs.
type framer = transport.Framer

func lengthFramer(f config.Framing) (framer, error) {
	return transport.LengthPrefix{
		HeaderBytes:  f.HeaderBytes,
		LittleEndian: f.LittleEndian,
		Inclusive:    f.Inclusive,
		Max:          f.MaxBytes,
		Label:        fmt.Sprintf("length-prefix/%dB", f.HeaderBytes),
	}, nil
}

func sentinelFramer(f config.Framing) (framer, error) {
	if f.Terminator == "" {
		return nil, errors.New("ingress: sentinel framing needs a terminator")
	}
	return transport.Sentinel{
		Terminator: []byte(f.Terminator), Max: f.MaxBytes, Label: "sentinel",
	}, nil
}

// TCP accepts framed messages on a socket, optionally over TLS.
//
// Unlike the demo link server, there is no application handshake. A partner
// connects and starts sending; who they are was settled by the TLS handshake or
// by the network they came from. That is what makes this usable with a real
// carrier, whose front end will not speak a bespoke hello.
type TCP struct {
	rateLimit float64
	burst     int
	// shared caps the ingress as a whole. Each peer is paced by its own
	// bucket first, so a flooding peer exhausts its share and not the
	// others': the shared bucket only bites when the sum of the shares is
	// more than the node should take.
	shared *bucket

	name     string
	addr     string
	framer   framer
	tls      *tls.Config
	resolver *Resolver
	log      *slog.Logger

	// sessions holds the open connection per peer, so a reply can go back down
	// the link the request arrived on.
	mu       sync.RWMutex
	sessions map[string]*session
	ln       net.Listener
	// inflight tracks handlers still running, so shutdown can drain.
	inflight sync.WaitGroup
}

type session struct {
	conn   net.Conn
	framer framer
	out    *transport.Outbox
	limit  *bucket
}

// bucket is a token bucket: rate tokens a second, burst tokens deep. wait
// blocks the caller until a token is available, which on a read loop means
// the peer's writes back up into its own socket -- the flow control a Type
// B circuit always had, now with a number on it.
type bucket struct {
	mu     sync.Mutex
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
}

func newBucket(rate float64, burst int) *bucket {
	if rate <= 0 {
		return nil
	}
	if burst <= 0 {
		burst = int(rate) + 1
	}
	return &bucket{rate: rate, burst: float64(burst), tokens: float64(burst), last: time.Now()}
}

func (b *bucket) wait(ctx context.Context) {
	if b == nil {
		return
	}
	for {
		b.mu.Lock()
		now := time.Now()
		b.tokens = min(b.burst, b.tokens+now.Sub(b.last).Seconds()*b.rate)
		b.last = now
		if b.tokens >= 1 {
			b.tokens--
			b.mu.Unlock()
			return
		}
		need := time.Duration((1 - b.tokens) / b.rate * float64(time.Second))
		b.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-time.After(need):
		}
	}
}

// newSession wires the connection to its outbox: writes go through one
// writer goroutine so the read loop never waits on the peer's window (see
// transport.Outbox). A failed write closes the connection and so ends the
// read loop, and the peer redials.
func newSession(conn net.Conn, f framer, log *slog.Logger, peer string) *session {
	s := &session{conn: conn, framer: f}
	s.out = transport.NewOutbox(transport.OutboxDepth, func(raw []byte) error {
		if err := conn.SetWriteDeadline(time.Now().Add(30 * time.Second)); err != nil {
			return err
		}
		return f.WriteFrame(conn, raw)
	}, func(err error) {
		log.Warn("link write failed; closing", "peer", peer, "err", err)
		conn.Close()
	})
	s.out.Peer = peer
	return s
}

func (s *session) send(raw []byte) error { return s.out.Send(raw) }

func (s *session) close() {
	s.out.Close()
	s.conn.Close()
}

// NewTCP builds a TCP ingress.
func NewTCP(c config.Ingress, log *slog.Logger) (*TCP, error) {
	f, err := FramerFor(c.Framing)
	if err != nil {
		return nil, err
	}
	tc, err := TLSConfig(c.TLS)
	if err != nil {
		return nil, err
	}
	r, err := NewResolver(c.Identify)
	if err != nil {
		return nil, err
	}
	return &TCP{
		name: c.Name, addr: c.Addr, framer: f, tls: tc, resolver: r,
		rateLimit: c.RateLimit, burst: c.Burst,
		shared: newBucket(c.TotalRateLimit, c.TotalBurst),
		log:    log.With("ingress", c.Name), sessions: map[string]*session{},
	}, nil
}

func (t *TCP) Name() string { return t.name }

func (t *TCP) Addr() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.ln != nil {
		return t.ln.Addr().String()
	}
	return t.addr
}

// Listen binds the socket. Separated from Start so that a failure to bind is
// reported at startup rather than in a goroutine nobody is watching.
func (t *TCP) Listen() error {
	ln, err := net.Listen("tcp", t.addr)
	if err != nil {
		return fmt.Errorf("ingress %s: listen: %w", t.name, err)
	}
	if t.tls != nil {
		ln = tls.NewListener(ln, t.tls)
	}
	t.mu.Lock()
	t.ln = ln
	t.mu.Unlock()
	return nil
}

func (t *TCP) Start(ctx context.Context, h Handler) error {
	t.mu.RLock()
	ln := t.ln
	t.mu.RUnlock()
	if ln == nil {
		if err := t.Listen(); err != nil {
			return err
		}
		t.mu.RLock()
		ln = t.ln
		t.mu.RUnlock()
	}
	go func() { <-ctx.Done(); ln.Close() }()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		go t.serve(ctx, conn, h)
	}
}

func (t *TCP) serve(ctx context.Context, conn net.Conn, h Handler) {
	defer conn.Close()

	// Complete the TLS handshake before resolving identity: the peer
	// certificate is not available until it has run.
	var state *tls.ConnectionState
	if tc, ok := conn.(*tls.Conn); ok {
		if err := tc.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
			return
		}
		if err := tc.HandshakeContext(ctx); err != nil {
			t.log.Warn("tls handshake failed", "remote", conn.RemoteAddr().String(), "err", err)
			metrics.Counter("jetway_ingress_rejected_total", "connections refused before any message",
				metrics.Labels{"ingress": t.name, "reason": "tls_handshake"})
			return
		}
		s := tc.ConnectionState()
		state = &s
		if err := tc.SetDeadline(time.Time{}); err != nil {
			return
		}
	}

	r := bufio.NewReaderSize(conn, 64<<10)
	var peer, remote string
	var err error
	if t.resolver.ByHello() {
		// The first frame names the subscriber: one listener, a whole
		// population. The claim is trusted the way a source network would
		// be; a link that lies about its identity is a network problem,
		// not a parsing one.
		peer, err = readHello(conn, r, t.framer)
		remote = conn.RemoteAddr().String()
	} else {
		peer, remote, err = t.resolver.Resolve(state, conn.RemoteAddr())
	}
	if err != nil {
		t.log.Warn("refusing unidentified connection",
			"remote", conn.RemoteAddr().String(), "err", err)
		metrics.Counter("jetway_ingress_rejected_total", "connections refused before any message",
			metrics.Labels{"ingress": t.name, "reason": "unidentified"})
		return
	}

	sess := newSession(conn, t.framer, t.log, peer)
	sess.limit = newBucket(t.rateLimit, t.burst)
	defer sess.out.Close()
	t.mu.Lock()
	if prev := t.sessions[peer]; prev != nil {
		prev.close()
	}
	t.sessions[peer] = sess
	t.mu.Unlock()

	t.log.Info("link up", "peer", peer, "remote", remote, "framing", t.framer.Name())
	metrics.Gauge("jetway_ingress_links", "open links per ingress",
		metrics.Labels{"ingress": t.name}, float64(t.linkCount()))

	defer func() {
		t.mu.Lock()
		if t.sessions[peer] == sess {
			delete(t.sessions, peer)
		}
		t.mu.Unlock()
		t.log.Info("link down", "peer", peer)
		metrics.Gauge("jetway_ingress_links", "open links per ingress",
			metrics.Labels{"ingress": t.name}, float64(t.linkCount()))
	}()

	for {
		if ctx.Err() != nil {
			return
		}
		raw, err := t.framer.ReadFrame(r)
		if len(raw) > 0 {
			sess.limit.wait(ctx)
			t.shared.wait(ctx)
			t.inflight.Add(1)
			_, herr := h(ctx, Message{Peer: peer, Transport: t.name, Remote: remote, Raw: raw})
			t.inflight.Done()
			if herr != nil {
				// The message was not made durable. Closing the link is the only
				// honest signal available on a raw stream: it makes the partner
				// retransmit rather than assume delivery.
				t.log.Error("could not accept message; closing link so it is retransmitted",
					"peer", peer, "err", herr)
				metrics.Counter("jetway_ingress_refused_total", "messages the pipeline would not accept",
					metrics.Labels{"ingress": t.name, "peer": peer})
				return
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) && ctx.Err() == nil {
				t.log.Warn("link read ended", "peer", peer, "err", err)
			}
			return
		}
	}
}

// readHello reads the identification frame a by_hello listener opens with.
// The reader is the same one the session then reads from: a subscriber that
// pipelines its hello and its first message in one write must lose nothing.
func readHello(conn net.Conn, r *bufio.Reader, f framer) (string, error) {
	if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return "", err
	}
	raw, err := f.ReadFrame(r)
	if err != nil {
		return "", fmt.Errorf("reading the hello frame: %w", err)
	}
	var hello transport.Hello
	if err := json.Unmarshal(raw, &hello); err != nil || hello.Peer == "" {
		return "", &ErrUnidentified{Detail: "the hello frame does not name a peer"}
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		return "", err
	}
	return hello.Peer, nil
}

func (t *TCP) linkCount() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.sessions)
}

// Send delivers a message on the open session with a peer, which is how a reply
// goes back down the link its request arrived on.
func (t *TCP) Send(ctx context.Context, peer string, raw []byte) error {
	t.mu.RLock()
	s := t.sessions[peer]
	t.mu.RUnlock()
	if s == nil {
		return fmt.Errorf("ingress %s: no open link to %q", t.name, peer)
	}
	return s.send(raw)
}

// Peers lists the peers with an open link.
func (t *TCP) Peers() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]string, 0, len(t.sessions))
	for p := range t.sessions {
		out = append(out, p)
	}
	return out
}

// Drain waits for in-flight handlers to finish, then closes every link.
//
// Cutting sessions without draining loses whatever was mid-pipeline, which on a
// store-and-forward link means a partner believes a message was delivered that
// this process never finished writing.
func (t *TCP) Drain(ctx context.Context) error {
	done := make(chan struct{})
	go func() { t.inflight.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		t.log.Warn("drain deadline reached with work still in flight")
	}
	return t.Close()
}

func (t *TCP) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, s := range t.sessions {
		s.close()
	}
	t.sessions = map[string]*session{}
	if t.ln != nil {
		return t.ln.Close()
	}
	return nil
}
