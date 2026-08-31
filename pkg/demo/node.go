package demo

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/adamf/jetway/pkg/avail"
	"github.com/adamf/jetway/pkg/avs"
	"github.com/adamf/jetway/pkg/typeb"

	"github.com/adamf/jetway/pkg/gateway"
	"github.com/adamf/jetway/pkg/store"
	"github.com/adamf/jetway/pkg/transport"
)

// Node is a running simulated carrier reservation system.
type Node struct {
	Carrier   Carrier
	Gateway   *gateway.Gateway
	Store     store.Store
	Inventory *gateway.Inventory
	client    *transport.Client
}

// StartCarrier brings up a carrier that connects to the gateway's link server
// at addr and answers reservation requests from its own inventory.
//
// The link is a real TCP session even when the carrier runs in the same process
// as the gateway. Simulating the transport away would leave the part most
// likely to break in production -- framing, reconnection, partial reads --
// untested.
func StartCarrier(ctx context.Context, c Carrier, addr string, bus *gateway.Bus, log *slog.Logger) (*Node, error) {
	st := store.NewMem()
	gw := gateway.New(gateway.Identity{
		Designator: c.Designator,
		TTYAddress: c.TTYAddress,
		Name:       c.Designator + " res",
	}, st, bus, log.With("node", c.Designator), []byte("carrier-locator-secret-"+c.Designator))

	inv := gateway.NewInventory()
	inv.Carrier = c.Designator
	gw.Responder = inv

	client := &transport.Client{
		Addr:   addr,
		Hello:  transport.Hello{Peer: c.Designator, Role: "carrier", Format: string(c.Format)},
		Framer: transport.DefaultFramer(),
		Log:    log.With("node", c.Designator),
		// The gateway identifies this carrier from the listener it dialled,
		// the way a real circuit does, so there is nothing to announce.
		SkipHello: true,
	}
	gw.Sender = client

	// From the carrier's side the peer is the distribution system. Its
	// designator is learned from the first message; the link name is fixed by
	// the handshake.
	gw.AddPeer(&gateway.Peer{
		Name: "gds", Carrier: "", Format: c.Format, TTYAddress: "LONRM1J",
	})

	client.OnMessage = func(ctx context.Context, peer string, raw []byte) error {
		_, err := gw.Ingest(ctx, "gds", raw)
		return err
	}
	client.OnUp = func() {
		bus.Publish(gateway.EvLink, map[string]any{
			"node": c.Designator, "peer": "gds", "state": "up",
			"format": string(c.Format), "name": c.Name,
		})
	}

	n := &Node{Carrier: c, Gateway: gw, Store: st, Inventory: inv, client: client}
	go func() {
		if err := client.Run(ctx); err != nil {
			log.Error("carrier link ended", "carrier", c.Designator, "err", err)
		}
	}()
	go n.broadcastAvailability(ctx, log)
	return n, nil
}

// AVSInterval is how often a simulated carrier rebroadcasts availability.
//
// Short, because a demonstration should show the cache filling. A real carrier
// broadcasts on change rather than on a timer; the periodic form here also
// exercises the cache's rule that an older assertion never moves state
// backwards.
const AVSInterval = 20 * time.Second

// The window of departure dates a simulated carrier publishes around the
// default booking date.
const (
	AVSDaysBack    = 2
	AVSDaysForward = 2
)

// broadcastAvailability publishes what this carrier will sell without being
// asked. That is what free sale is: permission granted in advance.
func (n *Node) broadcastAvailability(ctx context.Context, log *slog.Logger) {
	send := func() {
		// Broadcast a window of dates, not just one. A carrier publishes
		// availability for the days it is selling, and covering a single date
		// makes every booking on any other day fall back to asking -- correct,
		// but it hides the feature and hides its bugs.
		var keys []avail.Key
		base := DefaultDate()
		for d := -AVSDaysBack; d <= AVSDaysForward; d++ {
			keys = append(keys, ScheduleKeys(n.Carrier.Designator, base.AddDate(0, 0, d))...)
		}
		entries := n.Inventory.Availability(keys, time.Now().UTC())
		if len(entries) == 0 {
			return
		}
		text := avs.Build(entries)
		out := &typeb.Message{
			Priority:     "QU",
			Destinations: []typeb.Address{mustAddr("LONRM1J")},
			Origin:       mustAddr(n.Carrier.TTYAddress),
			OriginTime:   nowOriginTime(),
			Text:         text,
		}
		raw, err := out.Encode(typeb.EncodeOptions{Charset: typeb.CharsetITA2, CRLF: true})
		if err != nil {
			log.Error("could not encode availability broadcast", "carrier", n.Carrier.Designator, "err", err)
			return
		}
		peer := n.Gateway.Peer("gds")
		if peer == nil {
			return
		}
		if _, err := n.Gateway.Send(ctx, peer, raw, "AVS", "", ""); err != nil {
			log.Debug("availability broadcast not delivered", "carrier", n.Carrier.Designator, "err", err)
		}
	}

	// A first broadcast shortly after the link comes up, then on a timer.
	select {
	case <-ctx.Done():
		return
	case <-time.After(2 * time.Second):
		send()
	}
	t := time.NewTicker(AVSInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			send()
		}
	}
}

func mustAddr(s string) typeb.Address {
	a, _ := typeb.ParseAddress(s)
	return a
}

func nowOriginTime() typeb.OriginTime {
	n := time.Now().UTC()
	return typeb.OriginTime{Day: n.Day(), Hour: n.Hour(), Minute: n.Minute(), Present: true}
}

// Fleet holds the running simulated carriers.
type RunningFleet struct {
	Nodes map[string]*Node
}

// StartFleet brings up every carrier in the fleet. addrFor supplies the
// gateway listener each carrier dials; a carrier with no address is skipped.
func StartFleet(ctx context.Context, carriers []Carrier, addrFor func(Carrier) string,
	bus *gateway.Bus, log *slog.Logger) (*RunningFleet, error) {
	f := &RunningFleet{Nodes: map[string]*Node{}}
	for _, c := range carriers {
		addr := addrFor(c)
		if addr == "" {
			log.Warn("no gateway listener for simulated carrier", "carrier", c.Designator)
			continue
		}
		n, err := StartCarrier(ctx, c, addr, bus, log)
		if err != nil {
			return nil, fmt.Errorf("demo: start carrier %s: %w", c.Designator, err)
		}
		f.Nodes[c.Designator] = n
		log.Info("simulated carrier started", "carrier", c.String())
	}
	return f, nil
}

// Node returns a running carrier by designator.
func (f *RunningFleet) Node(designator string) *Node {
	if f == nil {
		return nil
	}
	return f.Nodes[designator]
}
