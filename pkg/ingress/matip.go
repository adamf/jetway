package ingress

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/adamf/jetway/pkg/config"
	"github.com/adamf/jetway/pkg/metrics"
	"github.com/adamf/jetway/pkg/matip"
)

// MATIP accepts Type B traffic over MATIP sessions, per RFC 2351.
//
// This is how a carrier's existing Type B front end reaches a gateway over IP:
// it is what they already speak, on a port IANA allocated for it, so there is
// nothing bespoke to agree. Identity still comes from the transport -- the
// client certificate or the circuit -- not from the session open, which
// declares traffic characteristics rather than authenticating anybody.
type MATIP struct {
	name     string
	addr     string
	tls      *tls.Config
	resolver *Resolver
	cfg      matip.Config
	log      *slog.Logger

	mu       sync.RWMutex
	sessions map[string]*matip.Session
	ln       net.Listener
	inflight sync.WaitGroup
}

// NewMATIP builds a MATIP listener.
func NewMATIP(c config.Ingress, log *slog.Logger) (*MATIP, error) {
	tc, err := TLSConfig(c.TLS)
	if err != nil {
		return nil, err
	}
	r, err := NewResolver(c.Identify)
	if err != nil {
		return nil, err
	}
	coding, err := codingFor(c.MATIP.Coding)
	if err != nil {
		return nil, fmt.Errorf("ingress %s: %w", c.Name, err)
	}
	addr := c.Addr
	if addr == "" {
		addr = fmt.Sprintf("0.0.0.0:%d", matip.PortTypeB)
	}
	return &MATIP{
		name: c.Name, addr: addr, tls: tc, resolver: r,
		cfg: matip.Config{
			Coding:           coding,
			Protection:       uint8(c.MATIP.Protection),
			HandshakeTimeout: c.MATIP.HandshakeTimeout,
		},
		log: log.With("ingress", c.Name), sessions: map[string]*matip.Session{},
	}, nil
}

func codingFor(name string) (matip.Coding, error) {
	switch name {
	case "", "ascii":
		return matip.CodingASCII, nil
	case "ebcdic":
		return matip.CodingEBCDIC, nil
	case "ipars":
		return matip.CodingIPARS, nil
	case "baudot":
		return matip.CodingBaudot, nil
	}
	return 0, fmt.Errorf("matip coding %q is not one of ascii, ebcdic, ipars, baudot", name)
}

func (m *MATIP) Name() string { return m.name }

func (m *MATIP) Addr() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.ln != nil {
		return m.ln.Addr().String()
	}
	return m.addr
}

// Listen binds the socket.
func (m *MATIP) Listen() error {
	ln, err := net.Listen("tcp", m.addr)
	if err != nil {
		return fmt.Errorf("ingress %s: listen: %w", m.name, err)
	}
	if m.tls != nil {
		ln = tls.NewListener(ln, m.tls)
	}
	m.mu.Lock()
	m.ln = ln
	m.mu.Unlock()
	return nil
}

func (m *MATIP) Start(ctx context.Context, h Handler) error {
	m.mu.RLock()
	ln := m.ln
	m.mu.RUnlock()
	if ln == nil {
		if err := m.Listen(); err != nil {
			return err
		}
		m.mu.RLock()
		ln = m.ln
		m.mu.RUnlock()
	}
	go func() { <-ctx.Done(); ln.Close() }()
	m.log.Info("matip ingress listening", "addr", ln.Addr().String(), "coding", m.cfg.Coding)

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go m.serve(ctx, conn, h)
	}
}

func (m *MATIP) serve(ctx context.Context, conn net.Conn, h Handler) {
	defer conn.Close()

	var state *tls.ConnectionState
	if tc, ok := conn.(*tls.Conn); ok {
		if err := tc.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
			return
		}
		if err := tc.HandshakeContext(ctx); err != nil {
			m.log.Warn("tls handshake failed", "remote", conn.RemoteAddr().String(), "err", err)
			metrics.Counter("jetway_ingress_rejected_total", "connections refused before any message",
				metrics.Labels{"ingress": m.name, "reason": "tls_handshake"})
			return
		}
		s := tc.ConnectionState()
		state = &s
		tc.SetDeadline(time.Time{}) //nolint:errcheck
	}

	// Identity is settled before the MATIP handshake. The session open declares
	// what the traffic looks like, not who is sending it, so it must never be
	// the thing that decides which partner's records this connection may touch.
	peer, remote, err := m.resolver.Resolve(state, conn.RemoteAddr())
	if err != nil {
		m.log.Warn("refusing unidentified connection", "remote", conn.RemoteAddr().String(), "err", err)
		metrics.Counter("jetway_ingress_rejected_total", "connections refused before any message",
			metrics.Labels{"ingress": m.name, "reason": "unidentified"})
		return
	}

	sess, err := matip.Accept(conn, m.cfg, nil)
	if err != nil {
		m.log.Warn("matip session not established", "peer", peer, "err", err)
		metrics.Counter("jetway_ingress_rejected_total", "connections refused before any message",
			metrics.Labels{"ingress": m.name, "reason": "matip_handshake"})
		return
	}
	so := sess.Remote()
	m.log.Info("matip session up", "peer", peer, "remote", remote,
		"coding", so.Coding, "origin", so.Origin, "sender_hld", so.SenderHLD)

	m.mu.Lock()
	if prev := m.sessions[peer]; prev != nil {
		prev.Close(matip.CloseNormal) //nolint:errcheck
	}
	m.sessions[peer] = sess
	m.mu.Unlock()
	metrics.Gauge("jetway_ingress_links", "open links per ingress",
		metrics.Labels{"ingress": m.name}, float64(m.count()))

	defer func() {
		m.mu.Lock()
		if m.sessions[peer] == sess {
			delete(m.sessions, peer)
		}
		m.mu.Unlock()
		sess.Close(matip.CloseNormal) //nolint:errcheck
		m.log.Info("matip session down", "peer", peer)
		metrics.Gauge("jetway_ingress_links", "open links per ingress",
			metrics.Labels{"ingress": m.name}, float64(m.count()))
	}()

	for {
		if ctx.Err() != nil {
			return
		}
		payload, err := sess.Receive()
		if err != nil {
			var closed *matip.ErrClosed
			if errors.As(err, &closed) {
				m.log.Info("peer closed the session", "peer", peer, "cause", closed.Cause)
			} else if ctx.Err() == nil {
				m.log.Warn("matip session ended", "peer", peer, "err", err)
			}
			return
		}
		m.inflight.Add(1)
		_, herr := h(ctx, Message{Peer: peer, Transport: m.name, Remote: remote, Raw: payload})
		m.inflight.Done()
		if herr != nil {
			// Closing the session is the honest signal: the partner retransmits
			// rather than assuming a message we never made durable was taken.
			m.log.Error("could not accept message; closing the session so it is retransmitted",
				"peer", peer, "err", herr)
			metrics.Counter("jetway_ingress_refused_total", "messages the pipeline would not accept",
				metrics.Labels{"ingress": m.name, "peer": peer})
			return
		}
		metrics.Counter("jetway_ingress_accepted_total", "messages accepted",
			metrics.Labels{"ingress": m.name, "peer": peer})
	}
}

func (m *MATIP) count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.sessions)
}

// Send delivers a message on the open session with a peer.
func (m *MATIP) Send(ctx context.Context, peer string, raw []byte) error {
	m.mu.RLock()
	s := m.sessions[peer]
	m.mu.RUnlock()
	if s == nil {
		return fmt.Errorf("ingress %s: no open matip session with %q", m.name, peer)
	}
	return s.Send(raw)
}

// Peers lists peers with an open session.
func (m *MATIP) Peers() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, 0, len(m.sessions))
	for p := range m.sessions {
		out = append(out, p)
	}
	return out
}

// Drain waits for in-flight work, then closes every session.
func (m *MATIP) Drain(ctx context.Context) error {
	done := make(chan struct{})
	go func() { m.inflight.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		m.log.Warn("drain deadline reached with work still in flight")
	}
	return m.Close()
}

func (m *MATIP) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.sessions {
		s.Close(matip.CloseNormal) //nolint:errcheck
	}
	m.sessions = map[string]*matip.Session{}
	if m.ln != nil {
		return m.ln.Close()
	}
	return nil
}
