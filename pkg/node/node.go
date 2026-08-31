// Package node assembles a running jetwayd from a configuration.
//
// It exists so that there is exactly one wiring. jetwayd builds a node and
// serves it; the scenario suite builds a node and drives it. An integration
// test standing on a second, parallel copy of this assembly would be testing
// the copy -- and this repository has twice shipped tests that passed because
// they encoded the same assumption as the code they were checking.
package node

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/adamf/jetway/pkg/api"
	"github.com/adamf/jetway/pkg/avail"
	"github.com/adamf/jetway/pkg/config"
	"github.com/adamf/jetway/pkg/demo"
	"github.com/adamf/jetway/pkg/egress"
	"github.com/adamf/jetway/pkg/gateway"
	"github.com/adamf/jetway/pkg/ingress"
	"github.com/adamf/jetway/pkg/metrics"
	"github.com/adamf/jetway/pkg/queue"
	"github.com/adamf/jetway/pkg/spool"
	"github.com/adamf/jetway/pkg/store"
)

// Node is an assembled gateway: store, links, queues, sweeper and console.
//
// Everything a caller might want to drive or assert on is exported, because
// the scenario suite is a legitimate consumer rather than a special case that
// needs back doors cut for it.
type Node struct {
	Config  *config.Config
	Log     *slog.Logger
	Store   store.Store
	Gateway *gateway.Gateway
	Bus     *gateway.Bus
	Queues  *queue.Manager
	Sweeper *queue.Sweeper
	Router  *egress.Router
	Spool   *spool.Spool
	Fleet   *demo.RunningFleet
	API     *api.Server

	listeners []ingress.Ingress
	tcp       map[string]*ingress.TCP
	// matip holds the MATIP listeners so replies can find the session that
	// carried the request. It is per-node rather than package-level: two nodes
	// in one process -- which is what the load harness builds -- must not share
	// each other's sessions.
	matip map[string]*ingress.MATIP
}

// Options are the knobs the scenario suite needs and jetwayd does not.
type Options struct {
	// LocatorSecret seeds record locator allocation. Required.
	LocatorSecret []byte
	// SkipConsole omits the HTTP server. A load run wants the pipeline, not
	// the console.
	SkipConsole bool
	// ExtendAPI is passed through to the console's mux, so an embedder can
	// serve its own pages from the node's one listener.
	ExtendAPI func(mux *http.ServeMux)
}

// Build assembles a node without starting anything that accepts work.
//
// Listeners bind here, so a port conflict or an unreadable certificate fails
// at build time rather than later in a goroutine where it reads as silence.
func Build(ctx context.Context, cfg *config.Config, log *slog.Logger, opts Options) (*Node, error) {
	st, err := openStore(ctx, cfg, log)
	if err != nil {
		return nil, err
	}

	n := &Node{Config: cfg, Log: log, Store: st, Bus: gateway.NewBus(400),
		matip: map[string]*ingress.MATIP{}}

	if cfg.Spool.Enabled {
		if n.Spool, err = spool.Open(cfg.Spool.Dir); err != nil {
			st.Close()
			return nil, err
		}
		log.Info("write-ahead spool enabled", "dir", n.Spool.Dir())
	} else {
		log.Warn("write-ahead spool disabled",
			"consequence", "a store outage becomes refused acknowledgements to partners")
	}

	n.Gateway = gateway.New(gateway.Identity{
		Designator: cfg.Identity.Designator,
		TTYAddress: cfg.Identity.TTYAddress,
		Name:       cfg.Identity.Name,
	}, st, n.Bus, log, opts.LocatorSecret)

	n.Gateway.Avail = avail.NewCache()
	n.Gateway.Log.Info("availability cache ready", "trust_window", n.Gateway.Avail.StaleAfter)

	n.Gateway.Relay = cfg.Routing.Relay
	if n.Gateway.Relay {
		log.Warn("address relay enabled",
			"consequence", "messages addressed to other links will be forwarded",
			"check", "only do this for links you would answer for")
	}

	// Queue state lives in the store because a worklist has to be listed,
	// counted and audited. Notify is the seam for an external broker that
	// wants to be told rather than to poll.
	n.Queues = &queue.Manager{
		Store: st, Log: log,
		Notify: func(item *store.QueueItem) { n.Bus.Publish(gateway.EvQueue, item) },
	}
	n.Gateway.Queues = n.Queues

	n.Router = egress.NewRouter(st, log)
	n.Gateway.Sender = n.Router

	if n.listeners, n.tcp, err = n.buildIngress(); err != nil {
		n.Close()
		return nil, err
	}
	if err := n.registerPeers(); err != nil {
		n.Close()
		return nil, err
	}

	// The sweeper is what notices silence: a request nobody answered, a
	// ticketing deadline that passed. Nothing else can, because neither is an
	// event a partner sends.
	n.Sweeper = &queue.Sweeper{Records: st, Queues: n.Queues, Log: log}

	if !opts.SkipConsole {
		n.API = &api.Server{
			Gateway: n.Gateway, Store: st, Bus: n.Bus, Log: log,
			Extend:  opts.ExtendAPI,
			Console: cfg.HTTP.Console,
			Metrics: cfg.HTTP.Metrics,
			Ready: func(ctx context.Context) error {
				_, err := st.ListPNRs(ctx, 1)
				return err
			},
			LinkPeers: n.LivePeers,
		}
	}
	return n, nil
}

// Start brings up everything that accepts or generates work, and returns once
// the links are running. It does not serve the console; Serve does that.
func (n *Node) Start(ctx context.Context) error {
	handler := n.Handler()
	for _, in := range n.listeners {
		in := in
		go func() {
			if err := in.Start(ctx, handler); err != nil && ctx.Err() == nil {
				n.Log.Error("ingress stopped", "ingress", in.Name(), "err", err)
			}
		}()
		n.Log.Info("ingress listening", "name", in.Name(), "addr", in.Addr())
	}

	if n.Spool != nil {
		go n.drainSpool(ctx, n.Config.Spool.DrainInterval)
	}
	go n.Router.Run(ctx)
	if c, err := n.Router.Recover(ctx); err != nil {
		n.Log.Error("could not recover undelivered messages", "err", err)
	} else if c > 0 {
		n.Log.Info("queued undelivered messages for redelivery", "count", c)
	}

	fleet, err := n.startDemo(ctx)
	if err != nil {
		return err
	}
	n.Fleet = fleet
	if n.API != nil {
		n.API.Fleet = fleet
	}

	n.registerRuntimeMetrics()
	metrics.Default.OnCollect(func() {
		metrics.Gauge("jetway_availability_entries", "availability beliefs currently held",
			nil, float64(n.Gateway.Avail.Len()))
	})
	go n.purgeAvailability(ctx)
	go n.Sweeper.Run(ctx, time.Minute)
	return nil
}

// Addr returns the address of the named TCP listener, which is how a test that
// asked for port 0 finds out what it got.
func (n *Node) Addr(listener string) string {
	if t, ok := n.tcp[listener]; ok {
		return t.Addr()
	}
	for _, in := range n.listeners {
		if in.Name() == listener {
			return in.Addr()
		}
	}
	return ""
}

// Listeners returns the bound ingress listeners.
func (n *Node) Listeners() []ingress.Ingress { return n.listeners }

// Close releases everything Build acquired. It is safe on a partly built node,
// which is what makes it usable on Build's own error paths.
func (n *Node) Close() {
	for _, in := range n.listeners {
		in.Close() //nolint:errcheck
	}
	if n.Store != nil {
		n.Store.Close()
	}
}

// Drain stops taking new work, lets what is in flight finish, then stops
// serving. Cutting links first would lose messages mid-pipeline.
func (n *Node) Drain(ctx context.Context, hs *http.Server) {
	for _, in := range n.listeners {
		if d, ok := in.(interface{ Drain(context.Context) error }); ok {
			if err := d.Drain(ctx); err != nil {
				n.Log.Warn("ingress drain", "ingress", in.Name(), "err", err)
			}
		} else {
			in.Close() //nolint:errcheck
		}
	}
	if hs != nil {
		hs.Shutdown(ctx) //nolint:errcheck
	}
}

// Serve runs the console until the context is cancelled.
func (n *Node) Serve(ctx context.Context, drainTimeout time.Duration) error {
	if n.API == nil {
		<-ctx.Done()
		return nil
	}
	hs := &http.Server{
		Addr: n.Config.HTTP.Addr, Handler: n.API.Handler(), ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		n.Log.Info("draining")
		dctx, cancel := context.WithTimeout(context.Background(), drainTimeout)
		defer cancel()
		n.Drain(dctx, hs)
	}()
	n.Log.Info("console ready", "url", "http://"+n.Config.HTTP.Addr,
		"identity", n.Config.Identity.Designator, "store", n.Config.Store.Backend)
	return hs.ListenAndServe()
}

// LivePeers names the partners currently holding a session.
func (n *Node) LivePeers() []string {
	seen := map[string]bool{}
	var out []string
	add := func(ps []string) {
		for _, p := range ps {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	for _, t := range n.tcp {
		add(t.Peers())
	}
	for _, m := range n.matip {
		add(m.Peers())
	}
	// The router's peer list is configuration, not liveness: senders are
	// registered at boot and never unregistered, so including them here made
	// every peer that ever connected read as live forever.
	return out
}

func fmtErr(what string, err error) error { return fmt.Errorf("node: %s: %w", what, err) }
