package ingress

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/adamf/jetway/internal/config"
)

func testLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// ---------------------------------------------------------------- test PKI

type ca struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newCA(t *testing.T) *ca {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "jetway-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return &ca{cert: cert, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

// issue mints a leaf certificate and writes the pair to disk.
func (c *ca) issue(t *testing.T, dir, name string, hosts []string, client bool) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	usage := x509.ExtKeyUsageServerAuth
	if client {
		usage = x509.ExtKeyUsageClientAuth
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{usage},
		DNSNames:     hosts,
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := x509.MarshalECPrivateKey(key)
	certPath = filepath.Join(dir, name+".crt")
	keyPath = filepath.Join(dir, name+".key")
	write(t, certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	write(t, keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}))
	return certPath, keyPath
}

func write(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (c *ca) caFile(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "ca.crt")
	write(t, p, c.pem)
	return p
}

func (c *ca) clientTLS(t *testing.T, certPath, keyPath string) *tls.Config {
	t.Helper()
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(c.pem)
	return &tls.Config{Certificates: []tls.Certificate{cert}, RootCAs: pool, MinVersion: tls.VersionTLS12}
}

// ---------------------------------------------------------------- resolver

func TestResolverPrefersCertificate(t *testing.T) {
	r, err := NewResolver(config.Identify{
		Peer:     "fallback",
		ByCertCN: map[string]string{"ba.example.com": "BA"},
	})
	if err != nil {
		t.Fatal(err)
	}
	state := &tls.ConnectionState{PeerCertificates: []*x509.Certificate{
		{Subject: pkix.Name{CommonName: "ba.example.com"}},
	}}
	peer, remote, err := r.Resolve(state, nil)
	if err != nil || peer != "BA" {
		t.Fatalf("Resolve = %q, %q, %v", peer, remote, err)
	}

	// A certificate that is valid but not mapped must be refused, not fall
	// through to the static peer. Otherwise any holder of a CA-signed
	// certificate becomes whichever partner the fallback names.
	other := &tls.ConnectionState{PeerCertificates: []*x509.Certificate{
		{Subject: pkix.Name{CommonName: "someone-else"}},
	}}
	if _, _, err := r.Resolve(other, nil); err == nil {
		t.Error("an unmapped certificate must not fall through to the static peer")
	}
	if _, _, err := r.Resolve(nil, nil); err == nil {
		t.Error("a missing certificate must be refused when identification is by certificate")
	}
}

func TestResolverBySAN(t *testing.T) {
	r, _ := NewResolver(config.Identify{ByCertCN: map[string]string{"aa.example.com": "AA"}})
	state := &tls.ConnectionState{PeerCertificates: []*x509.Certificate{
		{Subject: pkix.Name{}, DNSNames: []string{"aa.example.com"}},
	}}
	peer, _, err := r.Resolve(state, nil)
	if err != nil || peer != "AA" {
		t.Errorf("SAN did not resolve: %q, %v", peer, err)
	}
}

func TestResolverByCIDR(t *testing.T) {
	r, err := NewResolver(config.Identify{ByCIDR: map[string]string{"10.1.0.0/16": "LH"}})
	if err != nil {
		t.Fatal(err)
	}
	peer, _, err := r.Resolve(nil, &net.TCPAddr{IP: net.ParseIP("10.1.2.3"), Port: 5})
	if err != nil || peer != "LH" {
		t.Errorf("in-range address = %q, %v", peer, err)
	}
	if _, _, err := r.Resolve(nil, &net.TCPAddr{IP: net.ParseIP("10.2.2.3"), Port: 5}); err == nil {
		t.Error("an out-of-range address must be refused")
	}
}

func TestResolverRejectsMalformedCIDR(t *testing.T) {
	if _, err := NewResolver(config.Identify{ByCIDR: map[string]string{"not-a-network": "BA"}}); err == nil {
		t.Error("expected a malformed network to be rejected at construction")
	}
}

func TestResolverWithNoRulesRefuses(t *testing.T) {
	r, _ := NewResolver(config.Identify{})
	if _, _, err := r.Resolve(nil, nil); err == nil {
		t.Error("a listener with no identification rules must refuse everyone")
	}
}

// ---------------------------------------------------------------- https

func TestHTTPSMutualTLSIdentifiesPeerFromCertificate(t *testing.T) {
	dir := t.TempDir()
	c := newCA(t)
	srvCert, srvKey := c.issue(t, dir, "server", []string{"127.0.0.1", "localhost"}, false)
	baCert, baKey := c.issue(t, dir, "ba.example.com", nil, true)
	unknownCert, unknownKey := c.issue(t, dir, "stranger.example.com", nil, true)

	ic := config.Ingress{
		Name: "partners", Type: "https", Addr: "127.0.0.1:0",
		TLS: &config.TLS{Cert: srvCert, Key: srvKey, ClientCA: c.caFile(t, dir)},
		Identify: config.Identify{ByCertCN: map[string]string{
			"ba.example.com": "BA",
		}},
	}
	h, err := NewHTTPS(ic, testLog())
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Listen(); err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var mu sync.Mutex
	var got []Message
	go h.Start(ctx, func(ctx context.Context, m Message) (Receipt, error) { //nolint:errcheck
		mu.Lock()
		defer mu.Unlock()
		got = append(got, m)
		return Receipt{ID: "msg-1"}, nil
	})
	waitListening(t, h.Addr())

	url := "https://" + h.Addr() + "/messages"
	body := []byte("QU LHRRMBA\r\n.LONRM1J 121430\r\nBA0175Y15JUNLHRJFKNN1\r\n")

	// The mapped partner is accepted and identified from its certificate.
	resp, err := post(c.clientTLS(t, baCert, baKey), url, body)
	if err != nil {
		t.Fatalf("post as BA: %v", err)
	}
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("status = %s, want 202", resp.Status)
	}
	if id := resp.Header.Get("X-Jetway-Message-Id"); id != "msg-1" {
		t.Errorf("message id header = %q", id)
	}
	mu.Lock()
	if len(got) != 1 || got[0].Peer != "BA" {
		t.Errorf("handler saw %+v", got)
	}
	if len(got) == 1 && !bytes.Equal(got[0].Raw, body) {
		t.Error("payload was altered in transit")
	}
	mu.Unlock()

	// A certificate signed by the same CA but not mapped to a peer is refused.
	// This is the case that matters: the TLS handshake succeeds, so only the
	// mapping stands between a stranger and writing to BA's records.
	resp, err = post(c.clientTLS(t, unknownCert, unknownKey), url, body)
	if err != nil {
		t.Fatalf("post as stranger: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("unmapped certificate got %s, want 403", resp.Status)
	}

	// No client certificate at all fails in the handshake.
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(c.pem)
	if _, err := post(&tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, url, body); err == nil {
		t.Error("a connection with no client certificate should not complete")
	}
}

// A message the pipeline will not accept must produce a failure the partner can
// see, so they retransmit rather than assume delivery.
func TestHTTPSRefusalIsVisibleToTheSender(t *testing.T) {
	h, ctx, cancel := plainHTTPS(t, config.Identify{Peer: "BA"}, false)
	defer cancel()
	go h.Start(ctx, func(ctx context.Context, m Message) (Receipt, error) { //nolint:errcheck
		return Receipt{}, fmt.Errorf("store unavailable")
	})
	waitListening(t, h.Addr())

	resp, err := post(nil, "http://"+h.Addr()+"/messages", []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %s, want 503 so the partner retransmits", resp.Status)
	}
}

func TestHTTPSSynchronousReturnsTheReply(t *testing.T) {
	h, ctx, cancel := plainHTTPS(t, config.Identify{Peer: "BA"}, true)
	defer cancel()
	reply := []byte("QU LONRM1J\r\n.LHRRMBA 121431\r\nBA0175Y15JUNLHRJFKKK1\r\n")
	go h.Start(ctx, func(ctx context.Context, m Message) (Receipt, error) { //nolint:errcheck
		if !m.Synchronous {
			t.Error("a synchronous listener must mark its messages as such")
		}
		return Receipt{ID: "m1", Reply: reply}, nil
	})
	waitListening(t, h.Addr())

	resp, err := post(nil, "http://"+h.Addr()+"/messages", []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %s, want 200", resp.Status)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(body, reply) {
		t.Errorf("body = %q, want the generated reply", body)
	}
}

func TestHTTPSRejectsEmptyAndOversizeBodies(t *testing.T) {
	h, ctx, cancel := plainHTTPS(t, config.Identify{Peer: "BA"}, false)
	defer cancel()
	go h.Start(ctx, func(ctx context.Context, m Message) (Receipt, error) { //nolint:errcheck
		return Receipt{ID: "x"}, nil
	})
	waitListening(t, h.Addr())

	resp, err := post(nil, "http://"+h.Addr()+"/messages", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("empty body got %s, want 400", resp.Status)
	}
	resp, err = post(nil, "http://"+h.Addr()+"/messages", bytes.Repeat([]byte("A"), MaxRequestBytes+1024))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode < 400 {
		t.Errorf("oversize body got %s, want a rejection", resp.Status)
	}
}

func plainHTTPS(t *testing.T, id config.Identify, sync bool) (*HTTPS, context.Context, context.CancelFunc) {
	t.Helper()
	h, err := NewHTTPS(config.Ingress{
		Name: "test", Type: "https", Addr: "127.0.0.1:0", Identify: id, Synchronous: sync,
	}, testLog())
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Listen(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(func() { h.Close() })
	return h, ctx, cancel
}

func post(tc *tls.Config, url string, body []byte) (*http.Response, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	if tc != nil {
		client.Transport = &http.Transport{TLSClientConfig: tc}
	}
	return client.Post(url, "application/octet-stream", bytes.NewReader(body))
}

func waitListening(t *testing.T, addr string) {
	t.Helper()
	for i := 0; i < 200; i++ {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			c.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("nothing listening on %s", addr)
}

// ---------------------------------------------------------------- tcp

func TestTCPIdentifiesFromCertificateAndCarriesBothDirections(t *testing.T) {
	dir := t.TempDir()
	c := newCA(t)
	srvCert, srvKey := c.issue(t, dir, "server", []string{"127.0.0.1"}, false)
	baCert, baKey := c.issue(t, dir, "ba.example.com", nil, true)

	tcp, err := NewTCP(config.Ingress{
		Name: "link-ba", Type: "tcp", Addr: "127.0.0.1:0",
		Framing: config.Framing{Kind: "length_prefix", HeaderBytes: 4},
		TLS:     &config.TLS{Cert: srvCert, Key: srvKey, ClientCA: c.caFile(t, dir)},
		Identify: config.Identify{ByCertCN: map[string]string{
			"ba.example.com": "BA",
		}},
	}, testLog())
	if err != nil {
		t.Fatal(err)
	}
	if err := tcp.Listen(); err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	got := make(chan Message, 4)
	go tcp.Start(ctx, func(ctx context.Context, m Message) (Receipt, error) { //nolint:errcheck
		got <- m
		return Receipt{ID: "m"}, nil
	})
	waitListening(t, tcp.Addr())

	conn, err := tls.Dial("tcp", tcp.Addr(), c.clientTLS(t, baCert, baKey))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	f, _ := FramerFor(config.Framing{Kind: "length_prefix", HeaderBytes: 4})
	if err := f.WriteFrame(conn, []byte("BA0175Y15JUNLHRJFKNN1")); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-got:
		if m.Peer != "BA" {
			t.Errorf("peer = %q, want BA (from the certificate)", m.Peer)
		}
		if m.Remote != "cert:ba.example.com" {
			t.Errorf("remote = %q", m.Remote)
		}
	case <-ctx.Done():
		t.Fatal("no message arrived")
	}

	// A reply goes back down the same session, which is the tcp_accept
	// arrangement a partner using our listener expects.
	waitFor(t, ctx, func() bool { return len(tcp.Peers()) == 1 })
	if err := tcp.Send(ctx, "BA", []byte("BA0175Y15JUNLHRJFKKK1")); err != nil {
		t.Fatalf("send reply: %v", err)
	}
	reply, err := f.ReadFrame(newReader(conn))
	if err != nil {
		t.Fatalf("read reply: %v", err)
	}
	if string(reply) != "BA0175Y15JUNLHRJFKKK1" {
		t.Errorf("reply = %q", reply)
	}
	if err := tcp.Send(ctx, "NOBODY", []byte("x")); err == nil {
		t.Error("sending to a peer with no link should fail")
	}
}

// A refused message closes the link, which is the only honest signal a raw
// stream has for "we did not take that; send it again".
func TestTCPClosesLinkWhenAMessageIsRefused(t *testing.T) {
	tcp, err := NewTCP(config.Ingress{
		Name: "link", Type: "tcp", Addr: "127.0.0.1:0",
		Framing:  config.Framing{Kind: "length_prefix", HeaderBytes: 4},
		Identify: config.Identify{ByCIDR: map[string]string{"127.0.0.0/8": "BA"}},
	}, testLog())
	if err != nil {
		t.Fatal(err)
	}
	if err := tcp.Listen(); err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	go tcp.Start(ctx, func(ctx context.Context, m Message) (Receipt, error) { //nolint:errcheck
		return Receipt{}, fmt.Errorf("store unavailable")
	})
	waitListening(t, tcp.Addr())

	conn, err := net.Dial("tcp", tcp.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	f, _ := FramerFor(config.Framing{Kind: "length_prefix", HeaderBytes: 4})
	if err := f.WriteFrame(conn, []byte("x")); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second)) //nolint:errcheck
	if _, err := io.ReadAll(conn); err == nil {
		// An orderly EOF is the expected outcome.
		t.Log("link closed cleanly")
	}
	waitFor(t, ctx, func() bool { return len(tcp.Peers()) == 0 })
}

func TestTCPRefusesUnidentifiedConnection(t *testing.T) {
	tcp, err := NewTCP(config.Ingress{
		Name: "link", Type: "tcp", Addr: "127.0.0.1:0",
		Framing:  config.Framing{Kind: "length_prefix", HeaderBytes: 4},
		Identify: config.Identify{ByCIDR: map[string]string{"10.0.0.0/8": "BA"}},
	}, testLog())
	if err != nil {
		t.Fatal(err)
	}
	if err := tcp.Listen(); err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go tcp.Start(ctx, func(ctx context.Context, m Message) (Receipt, error) { //nolint:errcheck
		t.Error("an unidentified connection must never reach the handler")
		return Receipt{}, nil
	})
	waitListening(t, tcp.Addr())

	conn, err := net.Dial("tcp", tcp.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	f, _ := FramerFor(config.Framing{Kind: "length_prefix", HeaderBytes: 4})
	f.WriteFrame(conn, []byte("BA0175Y15JUNLHRJFKNN1")) //nolint:errcheck
	time.Sleep(300 * time.Millisecond)
	if n := len(tcp.Peers()); n != 0 {
		t.Errorf("peers = %d, want 0", n)
	}
}

func newReader(c net.Conn) *bufReader { return newBufReader(c) }

// ---------------------------------------------------------------- filedrop

func TestFileDropReadsStableFilesOnly(t *testing.T) {
	dir := t.TempDir()
	f, err := NewFileDrop(config.Ingress{
		Name: "batch", Type: "filedrop", Dir: dir, Pattern: "*.msg",
		Identify: config.Identify{Peer: "BA"},
		Poll:     20 * time.Millisecond, StableFor: 60 * time.Millisecond,
	}, testLog())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	got := make(chan Message, 4)
	go f.Start(ctx, func(ctx context.Context, m Message) (Receipt, error) { //nolint:errcheck
		got <- m
		return Receipt{ID: "m"}, nil
	})

	path := filepath.Join(dir, "batch1.msg")
	write(t, path, []byte("PART"))
	// Still growing: it must not be read yet.
	time.Sleep(40 * time.Millisecond)
	write(t, path, []byte("PARTCOMPLETE"))

	select {
	case m := <-got:
		if string(m.Raw) != "PARTCOMPLETE" {
			t.Errorf("read a partial file: %q", m.Raw)
		}
		if m.Peer != "BA" {
			t.Errorf("peer = %q", m.Peer)
		}
	case <-ctx.Done():
		t.Fatal("file was never picked up")
	}

	// A processed file is moved aside, not left to be read again.
	waitFor(t, ctx, func() bool {
		_, err := os.Stat(path)
		return os.IsNotExist(err)
	})
	if _, err := os.Stat(filepath.Join(dir, ".processed", "batch1.msg")); err != nil {
		t.Errorf("processed file was not archived: %v", err)
	}
}

func TestFileDropLeavesRefusedFilesForRetry(t *testing.T) {
	dir := t.TempDir()
	f, err := NewFileDrop(config.Ingress{
		Name: "batch", Type: "filedrop", Dir: dir, Pattern: "*.msg",
		Identify: config.Identify{Peer: "BA"},
		Poll:     20 * time.Millisecond, StableFor: 20 * time.Millisecond,
	}, testLog())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var mu sync.Mutex
	attempts := 0
	go f.Start(ctx, func(ctx context.Context, m Message) (Receipt, error) { //nolint:errcheck
		mu.Lock()
		attempts++
		mu.Unlock()
		return Receipt{}, fmt.Errorf("store unavailable")
	})

	path := filepath.Join(dir, "a.msg")
	write(t, path, []byte("DATA"))

	// The partner has gone; the file is the only copy, so it must stay put and
	// be retried rather than archived.
	waitFor(t, ctx, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return attempts >= 2
	})
	if _, err := os.Stat(path); err != nil {
		t.Errorf("a refused file must be left in place: %v", err)
	}
}

func TestFileDropRequiresAPeer(t *testing.T) {
	if _, err := NewFileDrop(config.Ingress{
		Name: "batch", Type: "filedrop", Dir: t.TempDir(),
	}, testLog()); err == nil {
		t.Error("a file carries no identity, so a peer must be configured")
	}
}

func waitFor(t *testing.T, ctx context.Context, cond func() bool) {
	t.Helper()
	for {
		if cond() {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("condition not met within the timeout")
		case <-time.After(10 * time.Millisecond):
		}
	}
}
