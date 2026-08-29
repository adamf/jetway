// Package ingress accepts messages from partners.
//
// Everything here shares one rule: a peer's identity comes from something they
// cannot choose — the certificate they presented, the network they connected
// from, or the fact that a listener serves exactly one partner. It is never
// taken from the payload or from a name the sender asserts about itself. A
// gateway that trusts an asserted name lets any partner write to any other
// partner's records.
package ingress

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"

	"github.com/adamf/jetway/internal/config"
)

// Message is one inbound unit handed to the pipeline.
type Message struct {
	// Peer is the resolved partner name. Never sender-supplied.
	Peer string
	// Transport names the ingress that accepted it, for the audit trail.
	Transport string
	// Remote describes where it came from: an address, a certificate subject,
	// or a file path.
	Remote string
	Raw    []byte
	// FromFile marks a message read from a drop directory.
	FromFile bool
	// Synchronous means the sender is holding the exchange open for a reply.
	// Such a message cannot be spooled: the reply only exists once it has been
	// processed, so processing must happen before the caller is answered.
	Synchronous bool
}

// Receipt is what the pipeline gives back once a message is accepted.
type Receipt struct {
	// ID is the stored message identifier, echoed to the sender where the
	// transport allows it so both sides can refer to the same message.
	ID string
	// Reply is a generated response, present only when the caller asked for
	// synchronous processing. Normally a reply goes out over the peer's own
	// egress instead.
	Reply []byte
}

// Handler processes one inbound message. Returning an error means the message
// was not accepted; the ingress must not acknowledge it, so the partner
// retransmits.
type Handler func(ctx context.Context, m Message) (Receipt, error)

// Ingress is a source of inbound messages.
type Ingress interface {
	// Name identifies the listener in logs and metrics.
	Name() string
	// Addr reports where it is listening, once started.
	Addr() string
	// Start runs until ctx is cancelled.
	Start(ctx context.Context, h Handler) error
	// Close stops accepting and releases resources.
	Close() error
}

// Resolver maps a connection or request to a configured peer.
type Resolver struct {
	static   string
	byCertCN map[string]string
	byCIDR   []cidrPeer
}

type cidrPeer struct {
	net  *net.IPNet
	peer string
}

// NewResolver builds a resolver from configuration.
func NewResolver(id config.Identify) (*Resolver, error) {
	r := &Resolver{static: id.Peer, byCertCN: id.ByCertCN}
	for cidr, peer := range id.ByCIDR {
		_, n, err := net.ParseCIDR(cidr)
		if err != nil {
			return nil, fmt.Errorf("ingress: %q is not a network in CIDR form: %w", cidr, err)
		}
		r.byCIDR = append(r.byCIDR, cidrPeer{net: n, peer: peer})
	}
	return r, nil
}

// ErrUnidentified means nothing about the connection maps to a configured peer.
type ErrUnidentified struct{ Detail string }

func (e *ErrUnidentified) Error() string {
	return "ingress: could not identify the sender: " + e.Detail
}

// Resolve determines the peer for a connection.
//
// The order is deliberate: a certificate is the strongest claim, a source
// network is weaker and only meaningful on a private link, and a static peer
// applies when the listener serves one partner and nothing else can reach it.
func (r *Resolver) Resolve(state *tls.ConnectionState, remote net.Addr) (string, string, error) {
	if len(r.byCertCN) > 0 {
		if state == nil || len(state.PeerCertificates) == 0 {
			return "", "", &ErrUnidentified{Detail: "no client certificate was presented"}
		}
		cert := state.PeerCertificates[0]
		cn := cert.Subject.CommonName
		if peer, ok := r.byCertCN[cn]; ok {
			return peer, "cert:" + cn, nil
		}
		// A SAN DNS name is a reasonable alternative to the common name, which
		// modern tooling increasingly leaves empty.
		for _, name := range cert.DNSNames {
			if peer, ok := r.byCertCN[name]; ok {
				return peer, "cert:" + name, nil
			}
		}
		return "", "", &ErrUnidentified{
			Detail: fmt.Sprintf("client certificate %q is not mapped to a peer", describeSubject(cert)),
		}
	}

	if len(r.byCIDR) > 0 && remote != nil {
		host, _, err := net.SplitHostPort(remote.String())
		if err != nil {
			host = remote.String()
		}
		if ip := net.ParseIP(host); ip != nil {
			for _, c := range r.byCIDR {
				if c.net.Contains(ip) {
					return c.peer, "ip:" + host, nil
				}
			}
		}
		return "", "", &ErrUnidentified{Detail: "source address " + host + " is not in any configured network"}
	}

	if r.static != "" {
		remoteStr := ""
		if remote != nil {
			remoteStr = remote.String()
		}
		return r.static, remoteStr, nil
	}
	return "", "", &ErrUnidentified{Detail: "this listener has no identification rules"}
}

func describeSubject(c *x509.Certificate) string {
	if c.Subject.CommonName != "" {
		return c.Subject.CommonName
	}
	if len(c.DNSNames) > 0 {
		return c.DNSNames[0]
	}
	return c.Subject.String()
}

// TLSConfig builds a server TLS configuration.
//
// When a client CA is configured the handshake requires and verifies a client
// certificate. That is the difference between a link that is encrypted and a
// link that is authenticated, and only the second one is safe to act on.
func TLSConfig(c *config.TLS) (*tls.Config, error) {
	if c == nil {
		return nil, nil
	}
	cert, err := tls.LoadX509KeyPair(c.Cert, c.Key)
	if err != nil {
		return nil, fmt.Errorf("ingress: load certificate: %w", err)
	}
	min := uint16(tls.VersionTLS12)
	if c.MinVersion == "1.3" {
		min = tls.VersionTLS13
	}
	t := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: min}
	if c.ClientCA != "" {
		pem, err := os.ReadFile(c.ClientCA)
		if err != nil {
			return nil, fmt.Errorf("ingress: read client CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("ingress: client CA %s contains no certificates", c.ClientCA)
		}
		t.ClientCAs = pool
		t.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return t, nil
}

// FramerFor builds a transport framer from configuration.
func FramerFor(f config.Framing) (framer, error) {
	switch f.Kind {
	case "sentinel":
		return sentinelFramer(f)
	default:
		return lengthFramer(f)
	}
}
