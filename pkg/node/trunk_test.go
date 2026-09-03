package node

import (
	"context"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/adamf/jetway/pkg/config"
	"github.com/adamf/jetway/pkg/store"
	"github.com/adamf/jetway/pkg/transport"
)

// switchConfig is a message switch on loopback: relaying, identifying its
// links by hello, with the peers given.
func switchConfig(designator, tty string, peers []config.Peer) *config.Config {
	cfg := config.Default()
	cfg.Identity = config.Identity{Designator: designator, TTYAddress: tty, Name: "switch " + designator}
	cfg.HTTP.Addr = "127.0.0.1:0"
	cfg.Store = config.Store{Backend: "mem"}
	cfg.Spool.Enabled = false
	cfg.Demo.Carriers = false
	cfg.Routing.Relay = true
	cfg.Ingress = []config.Ingress{{Name: "links", Type: "tcp", Addr: "127.0.0.1:0", Identify: config.Identify{ByHello: true}}}
	cfg.Peers = peers
	return cfg
}

func startSwitch(t *testing.T, ctx context.Context, cfg *config.Config) *Node {
	t.Helper()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	n, err := Build(ctx, cfg, log, Options{LocatorSecret: []byte("s"), SkipConsole: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(n.Close)
	return n
}

// carrierLink is a carrier's own link to a switch, collecting what it is sent.
type carrierLink struct {
	client *transport.Client
	mu     sync.Mutex
	got    []string
}

func dialCarrier(t *testing.T, ctx context.Context, designator, addr string) *carrierLink {
	t.Helper()
	cl := &carrierLink{}
	up := make(chan struct{}, 1)
	cl.client = &transport.Client{
		Addr: addr, Framer: transport.DefaultFramer(), Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Hello: transport.Hello{Peer: designator, Role: "carrier", Format: "typeb"},
		OnMessage: func(ctx context.Context, peer string, raw []byte) error {
			cl.mu.Lock()
			cl.got = append(cl.got, string(raw))
			cl.mu.Unlock()
			return nil
		},
		OnUp: func() {
			select {
			case up <- struct{}{}:
			default:
			}
		},
	}
	go cl.client.Run(ctx)
	select {
	case <-up:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s never reached its switch at %s", designator, addr)
	}
	return cl
}

func (cl *carrierLink) received() []string {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	return append([]string(nil), cl.got...)
}

// Two switches, one trunk: a carrier on the first switch addresses a
// carrier on the second, and the message crosses the trunk both ways. The
// first switch holds the trunk open (link_dial); the second sees it as a
// link that dialled in and answers down it. Each switch knows the other's
// subscribers as reached via the trunk, which is how the real network is
// wired: nobody holds a circuit to everyone.
func TestTwoSwitchesTrunkTypeBBothWays(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Switch B first, so A has an address to dial.
	b := startSwitch(t, ctx, switchConfig("1Y", "XCHDD1Y", []config.Peer{
		{Name: "YY", Carrier: "YY", Format: "typeb", TTYAddress: "MANRMYY", Addresses: []string{"MANKPYY"}, Egress: config.Egress{Type: "tcp_accept"}},
		{Name: "1X", Carrier: "1X", Format: "typeb", TTYAddress: "XCHDD1X", Trunk: true, Egress: config.Egress{Type: "tcp_accept"}},
		{Name: "XX", Carrier: "XX", Format: "typeb", TTYAddress: "LHRRMXX", Egress: config.Egress{Type: "via", Via: "1X"}},
		{Name: "ZZ", Carrier: "ZZ", Format: "typeb", TTYAddress: "LHRRMZZ", Egress: config.Egress{Type: "via", Via: "1X"}},
	}))
	bAddr := b.Addr("links")
	if _, _, err := net.SplitHostPort(bAddr); err != nil {
		t.Fatalf("switch B listener: %q", bAddr)
	}
	a := startSwitch(t, ctx, switchConfig("1X", "XCHDD1X", []config.Peer{
		{Name: "XX", Carrier: "XX", Format: "typeb", TTYAddress: "LHRRMXX", Egress: config.Egress{Type: "tcp_accept"}},
		{Name: "1Y", Carrier: "1Y", Format: "typeb", TTYAddress: "XCHDD1Y", Egress: config.Egress{Type: "link_dial", Addr: bAddr, Role: "switch"}},
		{Name: "YY", Carrier: "YY", Format: "typeb", TTYAddress: "MANRMYY", Addresses: []string{"MANKPYY"}, Egress: config.Egress{Type: "via", Via: "1Y"}},
		{Name: "ZZ", Carrier: "ZZ", Format: "typeb", TTYAddress: "LHRRMZZ", Egress: config.Egress{Type: "tcp_accept"}},
	}))

	xx := dialCarrier(t, ctx, "XX", a.Addr("links"))
	yy := dialCarrier(t, ctx, "YY", bAddr)

	// The trunk shows as a live peer on both ends.
	deadline := time.Now().Add(5 * time.Second)
	for {
		aLive, bLive := strings.Join(a.LivePeers(), ","), strings.Join(b.LivePeers(), ",")
		if strings.Contains(aLive, "1Y") && strings.Contains(bLive, "1X") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("trunk never came up: A sees %s, B sees %s", aLive, bLive)
		}
		time.Sleep(20 * time.Millisecond)
	}

	send := func(cl *carrierLink, text string) {
		if err := cl.client.Send(ctx, "", []byte(text)); err != nil {
			t.Fatal(err)
		}
	}
	waitFor := func(cl *carrierLink, want string) {
		deadline := time.Now().Add(10 * time.Second)
		for {
			for _, m := range cl.received() {
				if strings.Contains(m, want) {
					return
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("never received %q; got %q", want, cl.received())
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	// XX on switch A tells YY on switch B something; it crosses the trunk.
	send(xx, "QU MANRMYY\n.LHRRMXX 121430\nACROSS THE TRUNK ONE\n")
	waitFor(yy, "ACROSS THE TRUNK ONE")
	// And YY answers, back down the same trunk.
	send(yy, "QU LHRRMXX\n.MANRMYY 121431\nACROSS THE TRUNK TWO\n")
	waitFor(xx, "ACROSS THE TRUNK TWO")

	// A message for both a local and a remote subscriber goes to each once.
	send(xx, "QU MANRMYY LHRRMXX\n.LHRRMXX 121432\nTO BOTH\n")
	waitFor(yy, "TO BOTH")
	time.Sleep(200 * time.Millisecond)
	count := func(cl *carrierLink, want string) int {
		n := 0
		for _, m := range cl.received() {
			if strings.Contains(m, want) {
				n++
			}
		}
		return n
	}
	if count(yy, "TO BOTH") != 1 {
		t.Errorf("YY got the message %d times", count(yy, "TO BOTH"))
	}
	if count(xx, "TO BOTH") != 0 {
		t.Errorf("XX was sent its own message back %d times", count(xx, "TO BOTH"))
	}

	// A message for several addressees on the far switch crosses the trunk
	// once; the far switch fans it out. And one for a subscriber on each
	// switch is not returned down the trunk it arrived on: B serves YY,
	// A serves XX's neighbour ZZ, and nobody bounces.
	zz := dialCarrier(t, ctx, "ZZ", a.Addr("links"))
	_ = zz
	send(xx, "QU MANRMYY MANKPYY\n.LHRRMXX 121433\nONCE OVER THE TRUNK\n")
	waitFor(yy, "ONCE OVER THE TRUNK")
	time.Sleep(300 * time.Millisecond)
	if n := count(yy, "ONCE OVER THE TRUNK"); n != 2 {
		t.Errorf("YY's two addresses on B: %d deliveries, want 2", n)
	}
	inbound := func(n *Node, want string) int {
		msgs, _ := n.Store.ListMessages(ctx, store.MessageFilter{Limit: 1000})
		c := 0
		for _, m := range msgs {
			if m.Direction == store.Inbound && strings.Contains(string(m.Raw), want) {
				c++
			}
		}
		return c
	}
	if n := inbound(b, "ONCE OVER THE TRUNK"); n != 1 {
		t.Errorf("the trunk carried the message %d times, want once", n)
	}
	send(xx, "QU MANRMYY LHRRMZZ\n.LHRRMXX 121434\nNO BOUNCE\n")
	waitFor(yy, "NO BOUNCE")
	waitFor(zz, "NO BOUNCE")
	time.Sleep(300 * time.Millisecond)
	if n := inbound(a, "NO BOUNCE"); n != 1 {
		t.Errorf("switch A saw the message %d times: it came back over the trunk", n)
	}
	if n := inbound(b, "NO BOUNCE"); n != 1 {
		t.Errorf("switch B saw the message %d times", n)
	}
}
