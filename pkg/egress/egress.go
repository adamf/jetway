// Package egress delivers messages to partners, and keeps trying.
//
// The gateway previously recorded a failed send and moved on, which means a
// confirmation a carrier is waiting for is lost the moment their front end
// blips. Airline links blip constantly: a partner restarts, a circuit flaps, a
// TLS certificate rolls. Redelivery is not an optional refinement, it is the
// difference between a booking that settles and one that sits at HN forever.
package egress

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/adamf/jetway/pkg/config"
	"github.com/adamf/jetway/pkg/metrics"
	"github.com/adamf/jetway/pkg/transport"
)

// Sender delivers raw bytes to one peer.
type Sender interface {
	Send(ctx context.Context, raw []byte) error
	// Describe names the destination for logs.
	Describe() string
}

// SenderFunc adapts a function to Sender.
type SenderFunc struct {
	Fn   func(ctx context.Context, raw []byte) error
	Desc string
}

func (s SenderFunc) Send(ctx context.Context, raw []byte) error { return s.Fn(ctx, raw) }
func (s SenderFunc) Describe() string                           { return s.Desc }

// ---------------------------------------------------------------- TCP dial

// TCPDial opens an outbound connection to a partner and keeps it up.
type TCPDial struct {
	addr   string
	framer transport.Framer
	tls    *tls.Config
	log    *slog.Logger

	mu   sync.Mutex
	conn net.Conn
}

// NewTCPDial builds an outbound TCP sender.
func NewTCPDial(e config.Egress, log *slog.Logger) (*TCPDial, error) {
	f, err := framerFor(e.Framing)
	if err != nil {
		return nil, err
	}
	var tc *tls.Config
	if e.TLS != nil {
		cert, err := tls.LoadX509KeyPair(e.TLS.Cert, e.TLS.Key)
		if err != nil {
			return nil, fmt.Errorf("egress: load client certificate: %w", err)
		}
		tc = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
		if e.TLS.ClientCA != "" {
			pem, err := os.ReadFile(e.TLS.ClientCA)
			if err != nil {
				return nil, fmt.Errorf("egress: read CA: %w", err)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pem) {
				return nil, fmt.Errorf("egress: CA %s contains no certificates", e.TLS.ClientCA)
			}
			tc.RootCAs = pool
		}
	}
	return &TCPDial{addr: e.Addr, framer: f, tls: tc, log: log}, nil
}

func (t *TCPDial) Describe() string { return "tcp://" + t.addr }

func (t *TCPDial) Send(ctx context.Context, raw []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.conn == nil {
		if err := t.dial(ctx); err != nil {
			return err
		}
	}
	if err := t.conn.SetWriteDeadline(time.Now().Add(30 * time.Second)); err != nil {
		return err
	}
	if err := t.framer.WriteFrame(t.conn, raw); err != nil {
		// The connection is suspect; drop it so the next attempt redials
		// rather than writing into a half-open socket.
		t.conn.Close()
		t.conn = nil
		return err
	}
	return nil
}

func (t *TCPDial) dial(ctx context.Context) error {
	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", t.addr)
	if err != nil {
		return err
	}
	if t.tls != nil {
		tc := tls.Client(conn, t.tls)
		if err := tc.HandshakeContext(ctx); err != nil {
			conn.Close()
			return fmt.Errorf("egress: tls handshake with %s: %w", t.addr, err)
		}
		conn = tc
	}
	t.conn = conn
	return nil
}

// Close drops the connection.
func (t *TCPDial) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.conn != nil {
		err := t.conn.Close()
		t.conn = nil
		return err
	}
	return nil
}

// ---------------------------------------------------------------- HTTPS post

// HTTPSPost delivers a message by posting it to a partner's endpoint.
type HTTPSPost struct {
	url    string
	client *http.Client
}

// NewHTTPSPost builds an HTTP sender, using a client certificate when one is
// configured.
func NewHTTPSPost(e config.Egress) (*HTTPSPost, error) {
	tr := &http.Transport{
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     90 * time.Second,
	}
	if e.TLS != nil {
		cert, err := tls.LoadX509KeyPair(e.TLS.Cert, e.TLS.Key)
		if err != nil {
			return nil, fmt.Errorf("egress: load client certificate: %w", err)
		}
		tc := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
		if e.TLS.ClientCA != "" {
			pem, err := os.ReadFile(e.TLS.ClientCA)
			if err != nil {
				return nil, fmt.Errorf("egress: read CA: %w", err)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pem) {
				return nil, fmt.Errorf("egress: CA %s contains no certificates", e.TLS.ClientCA)
			}
			tc.RootCAs = pool
		}
		tr.TLSClientConfig = tc
	}
	return &HTTPSPost{url: e.URL, client: &http.Client{Transport: tr, Timeout: 60 * time.Second}}, nil
}

func (h *HTTPSPost) Describe() string { return h.url }

func (h *HTTPSPost) Send(ctx context.Context, raw []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := h.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("egress: %s returned %s: %s", h.url, resp.Status, bytes.TrimSpace(body))
	}
	return nil
}

// ---------------------------------------------------------------- file drop

// FileDrop writes a message into a directory a partner collects from.
type FileDrop struct{ dir string }

// NewFileDrop builds a file-drop sender.
func NewFileDrop(e config.Egress) (*FileDrop, error) {
	if err := os.MkdirAll(e.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("egress: create %s: %w", e.Dir, err)
	}
	return &FileDrop{dir: e.Dir}, nil
}

func (f *FileDrop) Describe() string { return "file://" + f.dir }

// Send writes to a temporary name and renames into place, so a partner
// collecting the directory never sees a partial file.
func (f *FileDrop) Send(ctx context.Context, raw []byte) error {
	name := fmt.Sprintf("%d.msg", time.Now().UnixNano())
	tmp := filepath.Join(f.dir, "."+name)
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(f.dir, name))
}

func framerFor(f config.Framing) (transport.Framer, error) {
	switch f.Kind {
	case "sentinel":
		if f.Terminator == "" {
			return nil, errors.New("egress: sentinel framing needs a terminator")
		}
		return transport.Sentinel{Terminator: []byte(f.Terminator), Max: f.MaxBytes}, nil
	default:
		hb := f.HeaderBytes
		if hb == 0 {
			hb = 4
		}
		return transport.LengthPrefix{
			HeaderBytes: hb, LittleEndian: f.LittleEndian,
			Inclusive: f.Inclusive, Max: f.MaxBytes,
		}, nil
	}
}

// Build constructs the sender for a peer's configured egress. sessions supplies
// delivery over an inbound link for the "tcp_accept" arrangement, where the
// partner connects to us and replies go back down that same session.
// Resolver finds another peer's sender, which is what a "via" egress needs:
// the transit peer's link is looked up at send time, so registration order
// does not matter and a transit link that reconnects is picked up.
type Resolver interface {
	Sender(peer string) (Sender, bool)
}

func Build(p config.Peer, sessions func(ctx context.Context, peer string, raw []byte) error, log *slog.Logger) (Sender, error) {
	return BuildWith(p, sessions, nil, log)
}

// BuildWith is Build with a resolver for "via" egress. Build keeps its
// signature because most peers do not transit.
func BuildWith(p config.Peer, sessions func(ctx context.Context, peer string, raw []byte) error, transit Resolver, log *slog.Logger) (Sender, error) {
	switch p.Egress.Type {
	case "via":
		if p.Egress.Via == "" {
			return nil, fmt.Errorf("egress: peer %q uses via but names no transit peer", p.Name)
		}
		if transit == nil {
			return nil, fmt.Errorf("egress: peer %q uses via but no resolver was supplied", p.Name)
		}
		through := p.Egress.Via
		name := p.Name
		return SenderFunc{
			Fn: func(ctx context.Context, raw []byte) error {
				s, ok := transit.Sender(through)
				if !ok {
					return fmt.Errorf("egress: no link to %q to carry traffic for %q", through, name)
				}
				return s.Send(ctx, raw)
			},
			Desc: "via " + through,
		}, nil
	}
	switch p.Egress.Type {
	case "tcp_accept":
		if sessions == nil {
			return nil, fmt.Errorf("egress: peer %q uses tcp_accept but no ingress can carry replies", p.Name)
		}
		peer := p.Name
		return SenderFunc{
			Fn:   func(ctx context.Context, raw []byte) error { return sessions(ctx, peer, raw) },
			Desc: "reply on the inbound link",
		}, nil
	case "tcp_dial":
		return NewTCPDial(p.Egress, log)
	case "https_post":
		return NewHTTPSPost(p.Egress)
	case "filedrop":
		return NewFileDrop(p.Egress)
	}
	return nil, fmt.Errorf("egress: peer %q has an unknown egress type %q", p.Name, p.Egress.Type)
}

// recordMetrics is shared by the router and the retrier.
func recordAttempt(peer string, err error) {
	result := "ok"
	if err != nil {
		result = "error"
	}
	metrics.Counter("jetway_egress_attempts_total", "outbound delivery attempts",
		metrics.Labels{"peer": peer, "result": result})
}

// FramerFor is the framer a framing configuration names, for callers that
// hold a link of their own -- the node's dialled links -- rather than an
// egress built here.
func FramerFor(f config.Framing) (transport.Framer, error) { return framerFor(f) }
