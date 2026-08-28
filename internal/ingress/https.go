package ingress

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/adamf/jetway/internal/config"
	"github.com/adamf/jetway/internal/metrics"
)

// MaxRequestBytes bounds a posted message. Generous for interactive
// reservation traffic and far below what a batch file would be; a partner
// sending batches should use a file drop.
const MaxRequestBytes = 8 << 20

// HTTPS accepts messages posted over HTTP, optionally with mutual TLS.
//
// This is the ingress a partner can be onboarded onto today, without a SITA
// contract, a leased circuit or an agreed socket framing. POST the message
// bytes; identity comes from the client certificate.
type HTTPS struct {
	name        string
	addr        string
	tls         *tls.Config
	resolver    *Resolver
	synchronous bool
	log         *slog.Logger

	srv      *http.Server
	ln       net.Listener
	mu       sync.RWMutex
	inflight sync.WaitGroup
}

// NewHTTPS builds an HTTPS ingress.
func NewHTTPS(c config.Ingress, log *slog.Logger) (*HTTPS, error) {
	tc, err := TLSConfig(c.TLS)
	if err != nil {
		return nil, err
	}
	r, err := NewResolver(c.Identify)
	if err != nil {
		return nil, err
	}
	return &HTTPS{
		name: c.Name, addr: c.Addr, tls: tc, resolver: r,
		synchronous: c.Synchronous, log: log.With("ingress", c.Name),
	}, nil
}

func (h *HTTPS) Name() string { return h.name }

func (h *HTTPS) Addr() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.ln != nil {
		return h.ln.Addr().String()
	}
	return h.addr
}

// Listen binds the socket.
func (h *HTTPS) Listen() error {
	ln, err := net.Listen("tcp", h.addr)
	if err != nil {
		return fmt.Errorf("ingress %s: listen: %w", h.name, err)
	}
	if h.tls != nil {
		ln = tls.NewListener(ln, h.tls)
	}
	h.mu.Lock()
	h.ln = ln
	h.mu.Unlock()
	return nil
}

func (h *HTTPS) Start(ctx context.Context, handle Handler) error {
	h.mu.RLock()
	ln := h.ln
	h.mu.RUnlock()
	if ln == nil {
		if err := h.Listen(); err != nil {
			return err
		}
		h.mu.RLock()
		ln = h.ln
		h.mu.RUnlock()
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /messages", h.post(handle))
	// Partners' operations teams check reachability before anything else.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "ok\n") //nolint:errcheck
	})

	h.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 15 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       5 * time.Minute,
		ErrorLog:          nil,
	}
	go func() { <-ctx.Done(); h.srv.Close() }() //nolint:errcheck

	h.log.Info("https ingress listening", "addr", ln.Addr().String(),
		"mutual_tls", h.tls != nil && h.tls.ClientAuth == tls.RequireAndVerifyClientCert,
		"synchronous", h.synchronous)

	if err := h.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (h *HTTPS) post(handle Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		peer, remote, err := h.resolver.Resolve(r.TLS, addrOf(r))
		if err != nil {
			h.log.Warn("refusing unidentified request", "remote", r.RemoteAddr, "err", err)
			metrics.Counter("jetway_ingress_rejected_total", "requests refused before processing",
				metrics.Labels{"ingress": h.name, "reason": "unidentified"})
			// 403, not 401: there is no credential to re-present over HTTP.
			// The client certificate is the credential and it did not resolve.
			http.Error(w, "sender could not be identified", http.StatusForbidden)
			return
		}

		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxRequestBytes))
		if err != nil {
			http.Error(w, "could not read the request body", http.StatusRequestEntityTooLarge)
			return
		}
		if len(raw) == 0 {
			http.Error(w, "empty message", http.StatusBadRequest)
			return
		}

		m := Message{Peer: peer, Transport: h.name, Remote: remote, Raw: raw,
			Synchronous: h.synchronous}
		h.inflight.Add(1)
		defer h.inflight.Done()

		receipt, err := handle(r.Context(), m)
		if err != nil {
			// Refusing here is the whole contract: the partner must be told the
			// message was not accepted so it retransmits, rather than being
			// told 202 for something that never reached durable storage.
			h.log.Error("could not accept posted message", "peer", peer, "err", err)
			metrics.Counter("jetway_ingress_refused_total", "messages the pipeline would not accept",
				metrics.Labels{"ingress": h.name, "peer": peer})
			http.Error(w, "message not accepted; please retransmit", http.StatusServiceUnavailable)
			return
		}

		metrics.Counter("jetway_ingress_accepted_total", "messages accepted",
			metrics.Labels{"ingress": h.name, "peer": peer})

		if receipt.ID != "" {
			w.Header().Set("X-Jetway-Message-Id", receipt.ID)
		}
		// A synchronous listener returns the generated reply in the response
		// body, which is how an HTTP partner expects a request-response
		// exchange to work. Otherwise the reply goes out over this peer's own
		// egress and the response only acknowledges receipt.
		if h.synchronous && len(receipt.Reply) > 0 {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.WriteHeader(http.StatusOK)
			w.Write(receipt.Reply) //nolint:errcheck
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintf(w, "{\"status\":\"accepted\",\"id\":%q}\n", receipt.ID)
	}
}

func addrOf(r *http.Request) net.Addr {
	host, port, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return nil
	}
	p := 0
	fmt.Sscanf(port, "%d", &p) //nolint:errcheck
	ip := net.ParseIP(host)
	if ip == nil {
		return nil
	}
	return &net.TCPAddr{IP: ip, Port: p}
}

// Drain waits for in-flight requests, then stops the server.
func (h *HTTPS) Drain(ctx context.Context) error {
	done := make(chan struct{})
	go func() { h.inflight.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		h.log.Warn("drain deadline reached with requests still in flight")
	}
	if h.srv != nil {
		return h.srv.Shutdown(ctx)
	}
	return nil
}

func (h *HTTPS) Close() error {
	if h.srv != nil {
		return h.srv.Close()
	}
	if h.ln != nil {
		return h.ln.Close()
	}
	return nil
}
