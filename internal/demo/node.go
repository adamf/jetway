package demo

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/adamf/jetway/internal/gateway"
	"github.com/adamf/jetway/internal/store"
	"github.com/adamf/jetway/internal/transport"
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
	return n, nil
}

// Fleet holds the running simulated carriers.
type RunningFleet struct {
	Nodes map[string]*Node
}

// StartFleet brings up every carrier in the fleet.
func StartFleet(ctx context.Context, carriers []Carrier, addr string, bus *gateway.Bus, log *slog.Logger) (*RunningFleet, error) {
	f := &RunningFleet{Nodes: map[string]*Node{}}
	for _, c := range carriers {
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
