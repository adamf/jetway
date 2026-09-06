package ingress

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"testing"
	"time"

	"github.com/adamf/jetway/pkg/config"
	"github.com/adamf/jetway/pkg/transport"
)

// startHelloListener boots a by_hello listener with the given knobs and
// returns it with the framer clients speak and the messages it accepted.
func startHelloListener(t *testing.T, c config.Ingress) (*TCP, framer, chan Message) {
	t.Helper()
	c.Name, c.Type, c.Addr = "link-net", "tcp", "127.0.0.1:0"
	c.Framing = config.Framing{Kind: "length_prefix", HeaderBytes: 4}
	c.Identify = config.Identify{ByHello: true}
	tcp, err := NewTCP(c, testLog())
	if err != nil {
		t.Fatal(err)
	}
	if err := tcp.Listen(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); tcp.Close() })
	got := make(chan Message, 64)
	go tcp.Start(ctx, func(ctx context.Context, m Message) (Receipt, error) { //nolint:errcheck
		got <- m
		return Receipt{ID: "m"}, nil
	})
	waitListening(t, tcp.Addr())
	f, _ := FramerFor(c.Framing)
	return tcp, f, got
}

// helloAs dials and sends a hello with a token, then one message.
func helloAs(t *testing.T, addr string, f framer, peer, token string) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	hello, _ := json.Marshal(transport.Hello{Peer: peer, Role: "carrier", Token: token})
	if err := f.WriteFrame(conn, hello); err != nil {
		t.Fatal(err)
	}
	if err := f.WriteFrame(conn, []byte("FROM "+peer)); err != nil {
		t.Fatal(err)
	}
	return conn
}

// closedSoon reports whether the listener hangs up on the connection.
func closedSoon(conn net.Conn, within time.Duration) bool {
	conn.SetReadDeadline(time.Now().Add(within)) //nolint:errcheck
	_, err := conn.Read(make([]byte, 1))
	return err != nil && err != io.EOF || err == io.EOF
}

// A listener the internet reaches takes nobody's word: with require_token
// a peer that has no token here is refused before its first message, a
// peer with the wrong token likewise, and only the right token gets in.
func TestRequireTokenRefusesTokenlessPeers(t *testing.T) {
	tcp, f, got := startHelloListener(t, config.Ingress{RequireToken: true})
	tcp.SetTokens(map[string]string{"BA": "speedbird"})

	stranger := helloAs(t, tcp.Addr(), f, "AF", "")
	if !closedSoon(stranger, 3*time.Second) {
		t.Fatal("a tokenless peer was let in on a listener that requires tokens")
	}
	wrong := helloAs(t, tcp.Addr(), f, "BA", "not-it")
	if !closedSoon(wrong, 3*time.Second) {
		t.Fatal("a wrong token was accepted")
	}
	helloAs(t, tcp.Addr(), f, "BA", "speedbird")
	select {
	case m := <-got:
		if m.Peer != "BA" || string(m.Raw) != "FROM BA" {
			t.Fatalf("unexpected message %s from %s", m.Raw, m.Peer)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the right token did not get BA's message through")
	}
	if len(got) != 0 {
		t.Fatalf("%d messages from refused peers were accepted", len(got))
	}
}

// Without require_token a tokenless peer is still taken on its word, as
// every private-network deployment relies on.
func TestTokenlessPeersStillWelcomeByDefault(t *testing.T) {
	tcp, f, got := startHelloListener(t, config.Ingress{})
	helloAs(t, tcp.Addr(), f, "AF", "")
	select {
	case m := <-got:
		if m.Peer != "AF" {
			t.Fatalf("message attributed to %s", m.Peer)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a tokenless peer was refused on a listener that does not require tokens")
	}
}

// A quiet link is reaped after the idle timeout, so a peer that vanished
// or a stranger holding a socket does not hold a session for ever.
func TestIdleLinksAreReaped(t *testing.T) {
	tcp, f, _ := startHelloListener(t, config.Ingress{IdleTimeout: 300 * time.Millisecond})
	conn := helloAs(t, tcp.Addr(), f, "BA", "")
	deadline := time.Now().Add(5 * time.Second)
	for tcp.linkCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !closedSoon(conn, 3*time.Second) {
		t.Fatal("an idle link was not closed")
	}
	for tcp.linkCount() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := tcp.linkCount(); n != 0 {
		t.Fatalf("%d sessions still held after the idle timeout", n)
	}
}

// Beyond max_connections the door closes on newcomers and stays open for
// the peers already in.
func TestConnectionCapClosesTheDoor(t *testing.T) {
	tcp, f, got := startHelloListener(t, config.Ingress{MaxConnections: 2})
	helloAs(t, tcp.Addr(), f, "BA", "")
	helloAs(t, tcp.Addr(), f, "AF", "")
	for i := 0; i < 2; i++ {
		select {
		case <-got:
		case <-time.After(5 * time.Second):
			t.Fatal("the first two peers did not get through")
		}
	}
	// The third is closed as it arrives, so its writes may already fail:
	// either way it never gets a session.
	third, err := net.Dial("tcp", tcp.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer third.Close()
	hello, _ := json.Marshal(transport.Hello{Peer: "LH", Role: "carrier"})
	if werr := f.WriteFrame(third, hello); werr == nil {
		f.WriteFrame(third, []byte("FROM LH")) //nolint:errcheck
		if !closedSoon(third, 3*time.Second) {
			t.Fatal("a third connection was held past the cap")
		}
	}
	select {
	case m := <-got:
		t.Fatalf("message from %s accepted past the cap", m.Peer)
	case <-time.After(300 * time.Millisecond):
	}
}
