// Package scenario holds end-to-end exchanges through a real Jetway node, and
// the two ways of running them.
//
// One set of scenarios, two drivers. `go test ./internal/scenario` runs each
// once and asserts it behaved; `jetwayload` runs them concurrently for a
// duration and reports throughput and latency. That is deliberate: a load
// generator that exercises different paths from the integration suite measures
// something nobody has checked is correct, and an integration suite that never
// runs under concurrency misses every race. Sharing the scenarios means neither
// can drift from the other without the drift showing up as a test failure.
//
// Nothing here simulates the transport. The carriers are the same simulated
// fleet the demo runs, dialling real TCP sessions into real listeners, because
// framing, reconnection and partial reads are the parts most likely to break.
package scenario

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/adamf/jetway/pkg/config"
	"github.com/adamf/jetway/pkg/demo"
	"github.com/adamf/jetway/pkg/gateway"
	"github.com/adamf/jetway/pkg/node"
	"github.com/adamf/jetway/pkg/queue"
	"github.com/adamf/jetway/pkg/store"
)

// Harness is a running node with its simulated carriers attached.
type Harness struct {
	*node.Node
	cancel context.CancelFunc
}

// Options configure a harness.
type Options struct {
	// DSN, when set, runs against Postgres instead of the in-memory store.
	// The scenarios are identical either way; only the store differs, which
	// is the point of being able to set it.
	DSN string
	// Verbose lets the node's own logging through. Off by default because a
	// load run would otherwise spend its time formatting log lines.
	Verbose bool
	// LinkWait bounds how long Start waits for the carriers to come up. Zero
	// uses DefaultLinkWait.
	LinkWait time.Duration
	// Carriers overrides the simulated fleet. Nil uses demo.Fleet.
	Carriers []demo.Carrier
	// Capacity is the seats each simulated carrier offers per class per
	// flight. Zero uses DefaultCapacity.
	//
	// It has to be generous. The demo's default capacity is derived from the
	// flight number and runs to single digits, which is right for a
	// demonstration and wrong here: a load run would exhaust the flight in the
	// first second and spend the rest of its time measuring how quickly a
	// carrier can say no.
	Capacity int
}

// DefaultCapacity is the per-class seat count the harness gives each flight.
const DefaultCapacity = 100_000

// DefaultLinkWait is how long to wait for every simulated carrier to dial in.
const DefaultLinkWait = 15 * time.Second

// Start builds a node on ephemeral ports and waits for its links.
//
// Ephemeral ports rather than the demo's fixed ones: a load run starts several
// nodes, tests run in parallel, and a developer usually has jetwayd already
// running on 9101. Binding a fixed port would make all three fail as "address
// already in use", which reads like a broken test rather than a busy machine.
func Start(ctx context.Context, opts Options) (*Harness, error) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if opts.Verbose {
		log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}

	carriers := opts.Carriers
	if carriers == nil {
		carriers = demo.Fleet
	}
	cfg := loopbackConfig(carriers, opts.DSN)
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("scenario: config: %w", err)
	}

	ctx, cancel := context.WithCancel(ctx)
	nd, err := node.Build(ctx, cfg, log, node.Options{
		LocatorSecret: []byte("scenario-locator-secret"),
		SkipConsole:   true,
	})
	if err != nil {
		cancel()
		return nil, err
	}
	if err := nd.Start(ctx); err != nil {
		cancel()
		nd.Close()
		return nil, err
	}

	h := &Harness{Node: nd, cancel: cancel}
	capacity := opts.Capacity
	if capacity <= 0 {
		capacity = DefaultCapacity
	}

	wait := opts.LinkWait
	if wait <= 0 {
		wait = DefaultLinkWait
	}
	if err := h.waitForLinks(ctx, len(carriers), wait); err != nil {
		h.Stop()
		return nil, err
	}
	for _, c := range carriers {
		if n := h.Fleet.Node(c.Designator); n != nil {
			n.Inventory.SetCapacity(capacity)
		}
	}

	// Wait for the first availability broadcast here rather than making every
	// scenario wait for it. The simulated carriers broadcast a couple of
	// seconds after the link comes up, and a load run that paid that cost per
	// iteration would report it as the latency of booking.
	if err := h.waitForAvailability(ctx, wait); err != nil {
		h.Stop()
		return nil, err
	}
	return h, nil
}

// waitForAvailability blocks until the carriers have said what they will sell.
func (h *Harness) waitForAvailability(ctx context.Context, within time.Duration) error {
	deadline := time.Now().Add(within)
	for {
		if h.Gateway.Avail.Len() > 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("scenario: no carrier published availability within %s", within)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// waitForLinks blocks until every simulated carrier has a session.
//
// Without this a scenario can send to a peer whose TCP session has not
// finished dialling, and the failure looks like a routing bug rather than a
// startup race -- which is exactly the sort of flake that gets a suite
// disbelieved and then ignored.
func (h *Harness) waitForLinks(ctx context.Context, want int, within time.Duration) error {
	deadline := time.Now().Add(within)
	for {
		if len(h.LivePeers()) >= want {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("scenario: only %d of %d carrier links came up within %s: %v",
				len(h.LivePeers()), want, within, h.LivePeers())
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// Stop shuts the node down and releases its ports.
func (h *Harness) Stop() {
	h.cancel()
	h.Node.Close()
}

// Sweep runs one sweeper pass, which is how a scenario exercises the paths that
// only time triggers.
func (h *Harness) Sweep(ctx context.Context) (int, error) { return h.Sweeper.Sweep(ctx) }

// SweepAt runs one sweeper pass as if it were the given moment, so a scenario
// can reach a ticketing deadline without waiting for one.
func (h *Harness) SweepAt(ctx context.Context, at time.Time) (int, error) {
	sw := &queue.Sweeper{
		Records: h.Store, Queues: h.Queues, Log: h.Log,
		PendingAfter: h.Sweeper.PendingAfter, TicketingLead: h.Sweeper.TicketingLead,
		Now: func() time.Time { return at },
	}
	return sw.Sweep(ctx)
}

// loopbackConfig is the demo topology on ports the operating system picks.
func loopbackConfig(carriers []demo.Carrier, dsn string) *config.Config {
	cfg := config.Default()
	cfg.HTTP.Addr = "127.0.0.1:0"
	cfg.Demo.Carriers = true
	cfg.Spool.Enabled = false
	if dsn != "" {
		cfg.Store.Backend, cfg.Store.DSN, cfg.Store.Migrate = "postgres", dsn, true
	}

	cfg.Ingress = nil
	cfg.Peers = nil
	for _, c := range carriers {
		name := "link-" + lower(c.Designator)
		cfg.Ingress = append(cfg.Ingress, config.Ingress{
			Name: name, Type: "tcp", Addr: "127.0.0.1:0",
			Identify: config.Identify{Peer: c.Designator},
		})
		cfg.Peers = append(cfg.Peers, config.Peer{
			Name: c.Designator, Carrier: c.Designator, TTYAddress: c.TTYAddress,
			Format: formatName(c.Format),
			Egress: config.Egress{Type: "tcp_accept"},
		})
	}
	return cfg
}

func formatName(f store.Format) string {
	if f == store.FormatEDIFACT {
		return "edifact"
	}
	return "typeb"
}

func lower(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

// Gw is the gateway under test, named short because scenarios use it
// constantly.
func (h *Harness) Gw() *gateway.Gateway { return h.Gateway }
