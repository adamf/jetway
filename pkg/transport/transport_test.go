package transport

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"
)

func TestLengthPrefixRoundTrip(t *testing.T) {
	payloads := [][]byte{
		[]byte(""),
		[]byte("QU LHRRMBA\r\n.LONRM1J 121430\r\nBA0175Y15JUNLHRJFKNN1\r\n"),
		bytes.Repeat([]byte("A"), 70000),
		{0x00, 0xff, 0x01, 0x80}, // binary payloads must survive intact
	}
	for _, f := range []LengthPrefix{
		{HeaderBytes: 4},
		{HeaderBytes: 4, LittleEndian: true},
		{HeaderBytes: 4, Inclusive: true},
		{HeaderBytes: 2, Max: 65000},
		{HeaderBytes: 2, Inclusive: true, Max: 65000},
	} {
		var buf bytes.Buffer
		var want [][]byte
		for _, p := range payloads {
			if len(p) > f.max() {
				continue
			}
			if err := f.WriteFrame(&buf, p); err != nil {
				t.Fatalf("%s: WriteFrame: %v", f.Name(), err)
			}
			want = append(want, p)
		}
		r := bufio.NewReader(&buf)
		for i, w := range want {
			got, err := f.ReadFrame(r)
			if err != nil {
				t.Fatalf("%s: ReadFrame %d: %v", f.Name(), i, err)
			}
			if !bytes.Equal(got, w) {
				t.Errorf("%s: frame %d: got %d bytes, want %d", f.Name(), i, len(got), len(w))
			}
		}
		if _, err := f.ReadFrame(r); !errors.Is(err, io.EOF) {
			t.Errorf("%s: expected EOF after the last frame, got %v", f.Name(), err)
		}
	}
}

// A frame arriving in pieces is the normal case on a real socket, not an edge
// case. A framer that assumes one read per frame works in tests and corrupts
// production traffic.
func TestLengthPrefixHandlesSplitReads(t *testing.T) {
	f := LengthPrefix{HeaderBytes: 4}
	payload := bytes.Repeat([]byte("BA0175Y15JUN"), 500)
	var buf bytes.Buffer
	if err := f.WriteFrame(&buf, payload); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()

	// A reader that hands over one byte at a time, splitting even the header.
	r := bufio.NewReader(iotest.OneByteReader(bytes.NewReader(raw)))
	got, err := f.ReadFrame(r)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("payload corrupted across split reads")
	}
}

func TestLengthPrefixRejectsOversizeFrame(t *testing.T) {
	f := LengthPrefix{HeaderBytes: 4, Max: 16}
	if err := f.WriteFrame(io.Discard, bytes.Repeat([]byte("x"), 17)); !errors.Is(err, ErrFrameTooLarge) {
		t.Errorf("WriteFrame = %v, want ErrFrameTooLarge", err)
	}
	// A corrupt or hostile length header must not become an allocation of
	// whatever the peer claimed.
	hdr := []byte{0x7f, 0xff, 0xff, 0xff}
	if _, err := f.ReadFrame(bufio.NewReader(bytes.NewReader(hdr))); !errors.Is(err, ErrFrameTooLarge) {
		t.Errorf("ReadFrame = %v, want ErrFrameTooLarge", err)
	}
}

func TestLengthPrefixRejectsNegativeLength(t *testing.T) {
	// An inclusive framer whose declared length is smaller than its own header.
	f := LengthPrefix{HeaderBytes: 4, Inclusive: true}
	hdr := []byte{0, 0, 0, 2}
	if _, err := f.ReadFrame(bufio.NewReader(bytes.NewReader(hdr))); err == nil {
		t.Error("expected a negative payload length to be rejected")
	}
}

func TestSentinelFraming(t *testing.T) {
	f := TypeBSentinel()
	var buf bytes.Buffer
	msgs := []string{
		"QU LHRRMBA\n.LONRM1J 121430\nBA0175Y15JUNLHRJFKNN1\n",
		"QU NYCRMAA\n.LONRM1J 121431\nAA0100Y16JUNJFKLHRKK1\n",
	}
	for _, m := range msgs {
		if err := f.WriteFrame(&buf, []byte(m)); err != nil {
			t.Fatal(err)
		}
	}
	r := bufio.NewReader(&buf)
	for i := range msgs {
		got, err := f.ReadFrame(r)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if !strings.HasPrefix(string(got), msgs[i]) {
			t.Errorf("frame %d = %q", i, got)
		}
		if !strings.HasSuffix(string(got), "NNNN\n") {
			t.Errorf("frame %d lost its terminator: %q", i, got)
		}
	}
}

// The terminator must not be doubled when the payload already carries it.
func TestSentinelDoesNotDoubleTerminator(t *testing.T) {
	f := TypeBSentinel()
	var buf bytes.Buffer
	if err := f.WriteFrame(&buf, []byte("HELLO\nNNNN\n")); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(buf.String(), "NNNN"); n != 1 {
		t.Errorf("terminator appears %d times, want 1: %q", n, buf.String())
	}
}

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// End to end over a real loopback socket: handshake, both directions, and
// delivery to a named peer.
func TestServerClientExchange(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var mu sync.Mutex
	serverGot := make(chan string, 4)
	clientGot := make(chan string, 4)
	connected := make(chan string, 4)

	srv := &Server{
		Addr: "127.0.0.1:0", Framer: DefaultFramer(), Log: testLogger(),
		OnMessage: func(ctx context.Context, peer string, raw []byte) error {
			mu.Lock()
			defer mu.Unlock()
			serverGot <- peer + ":" + string(raw)
			return nil
		},
		OnConnect: func(peer, format string) { connected <- peer },
	}
	if err := srv.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer srv.Close()
	go srv.Serve(ctx) //nolint:errcheck

	cli := &Client{
		Addr:   srv.Addr,
		Hello:  Hello{Peer: "BA", Role: "carrier", Format: "typeb"},
		Framer: DefaultFramer(), Log: testLogger(),
		OnMessage: func(ctx context.Context, peer string, raw []byte) error {
			clientGot <- string(raw)
			return nil
		},
	}
	go cli.Run(ctx) //nolint:errcheck

	select {
	case peer := <-connected:
		if peer != "BA" {
			t.Fatalf("connected peer = %q", peer)
		}
	case <-ctx.Done():
		t.Fatal("no connection within the timeout")
	}

	if err := cli.Send(ctx, "", []byte("SELL")); err != nil {
		t.Fatalf("client send: %v", err)
	}
	select {
	case got := <-serverGot:
		if got != "BA:SELL" {
			t.Errorf("server received %q", got)
		}
	case <-ctx.Done():
		t.Fatal("server did not receive the message")
	}

	// The server addresses a reply by peer name, which is what routing does.
	waitFor(t, ctx, func() bool { return len(srv.Peers()) == 1 })
	if err := srv.Send(ctx, "BA", []byte("REPLY")); err != nil {
		t.Fatalf("server send: %v", err)
	}
	select {
	case got := <-clientGot:
		if got != "REPLY" {
			t.Errorf("client received %q", got)
		}
	case <-ctx.Done():
		t.Fatal("client did not receive the reply")
	}

	if err := srv.Send(ctx, "NOPE", []byte("x")); err == nil {
		t.Error("sending to an unknown peer should fail")
	}
}

// A carrier restarting its front end must not need the gateway restarted too.
func TestClientReconnects(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	ups := make(chan struct{}, 8)
	got := make(chan string, 8)
	srv := &Server{
		Addr: "127.0.0.1:0", Framer: DefaultFramer(), Log: testLogger(),
		OnMessage: func(ctx context.Context, peer string, raw []byte) error {
			got <- string(raw)
			return nil
		},
	}
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	go srv.Serve(ctx) //nolint:errcheck

	cli := &Client{
		Addr: srv.Addr, Hello: Hello{Peer: "BA"}, Framer: DefaultFramer(),
		Log: testLogger(), OnUp: func() { ups <- struct{}{} },
		OnMessage: func(ctx context.Context, peer string, raw []byte) error { return nil },
	}
	go cli.Run(ctx) //nolint:errcheck

	<-ups
	waitFor(t, ctx, func() bool { return len(srv.Peers()) == 1 })

	// Drop the link from the server side.
	srv.mu.Lock()
	for _, l := range srv.links {
		l.Close()
	}
	srv.mu.Unlock()

	// It must come back on its own, and carry traffic again.
	select {
	case <-ups:
	case <-ctx.Done():
		t.Fatal("client did not reconnect")
	}
	waitFor(t, ctx, func() bool { return len(srv.Peers()) == 1 })
	if err := cli.Send(ctx, "", []byte("AFTER-RECONNECT")); err != nil {
		t.Fatalf("send after reconnect: %v", err)
	}
	select {
	case m := <-got:
		if m != "AFTER-RECONNECT" {
			t.Errorf("got %q", m)
		}
	case <-ctx.Done():
		t.Fatal("no traffic after reconnect")
	}
}

// A connection that never sends a usable hello must be dropped, not left
// occupying a slot forever.
func TestServerRejectsBadHandshake(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	srv := &Server{
		Addr: "127.0.0.1:0", Framer: DefaultFramer(), Log: testLogger(),
		OnMessage: func(ctx context.Context, peer string, raw []byte) error { return nil },
	}
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	go srv.Serve(ctx) //nolint:errcheck

	conn, err := net.Dial("tcp", srv.Addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := DefaultFramer().WriteFrame(conn, []byte("this is not json")); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(conn); err != nil && !errors.Is(err, io.EOF) {
		// A reset is an acceptable way to be dropped.
		t.Logf("read after rejected handshake: %v", err)
	}
	// The connection is gone; no peer should ever have been registered for it.
	time.Sleep(50 * time.Millisecond)
	if n := len(srv.Peers()); n != 0 {
		t.Errorf("peers = %d, want 0", n)
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
