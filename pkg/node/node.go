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
	"github.com/adamf/jetway/pkg/dcs"
	"github.com/adamf/jetway/pkg/ops"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
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
	"github.com/adamf/jetway/pkg/transport"
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
	// Ops is the operations desk, when the configuration names a schedule.
	Ops     *ops.Desk
	Bus     *gateway.Bus
	Queues  *queue.Manager
	Sweeper *queue.Sweeper
	Router  *egress.Router
	Spool   *spool.Spool
	Fleet   *demo.RunningFleet
	API     *api.Server

	listeners []ingress.Ingress
	// holding is whether this process is the system's writer right now.
	holding       atomic.Bool
	standbyLogged atomic.Bool
	// leaseHolder names this process in the lease while it holds one, so
	// Drain and Close can give it back once the links are quiet.
	leaseMu     sync.Mutex
	leaseHolder string
	tcp         map[string]*ingress.TCP
	// matip holds the MATIP listeners so replies can find the session that
	// carried the request. It is per-node rather than package-level: two nodes
	// in one process -- which is what the load harness builds -- must not share
	// each other's sessions.
	matip map[string]*ingress.MATIP
	// links are the circuits this node holds open itself (link_dial
	// egress): a trunk to another switch, or a carrier's link to its
	// switch. They come up with the listeners and count as live peers
	// while connected.
	links   map[string]*transport.Client
	linksMu sync.Mutex
	// runCtx is the context Start was given, so a link added afterwards
	// (ReloadPeers) is dialled under it; nil before Start.
	runMu  sync.Mutex
	runCtx context.Context
}

// Options are the knobs the scenario suite needs and jetwayd does not.
type Options struct {
	// Store, when set, is used instead of the one the config names: a
	// harness sharing one store between nodes, which is how a lease is
	// tested.
	Store store.Store

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
	st := opts.Store
	var err error
	if st == nil {
		if st, err = openStore(ctx, cfg, log); err != nil {
			return nil, err
		}
	}

	n := &Node{Config: cfg, Log: log, Store: st, Bus: gateway.NewBus(400),
		matip: map[string]*ingress.MATIP{}, links: map[string]*transport.Client{}}

	if cfg.Spool.Enabled {
		if n.Spool, err = spool.Open(cfg.Spool.Dir); err != nil {
			st.Close()
			return nil, err
		}
		n.Spool.MaxEntries = cfg.Spool.MaxEntries
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
	if cfg.Ops.Schedule != "" {
		// A carrier, not only a gateway: the operations desk answers the
		// airport, files movements, hears the towers and the slots.
		legs, err := ops.LoadSchedule(cfg.Ops.Schedule)
		if err != nil {
			return nil, err
		}
		n.Ops = ops.New(n.Gateway, cfg.Identity.Designator, legs, ops.Config{Via: cfg.Ops.Via, MovementsTo: cfg.Ops.MovementsTo, AccountingCode: cfg.Ops.AccountingCode}, log)
		n.Gateway.Ground = n.Ops
	}
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
		var ground *dcs.Station
		if n.Ops != nil {
			ground = n.Ops.Station
		}
		n.API = &api.Server{
			Gateway: n.Gateway, Store: st, Bus: n.Bus, Log: log, Ground: ground,
			Ops:     n.Ops,
			Extend:  opts.ExtendAPI,
			Console: cfg.HTTP.Console,
			Metrics: cfg.HTTP.Metrics,
			Ready: func(ctx context.Context) error {
				if cfg.Lease.Enabled && !n.Holding() {
					return fmt.Errorf("standing by: %s is held elsewhere", cfg.Identity.Designator)
				}
				if n.Spool != nil {
					if err := spoolReady(n.Spool, SpoolReadyAge); err != nil {
						return err
					}
				}
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
	n.runMu.Lock()
	n.runCtx = ctx
	n.runMu.Unlock()
	if n.Config.Lease.Enabled {
		// The links open only while this process holds the system. Until
		// then it stands by, ready to take over the moment the holder lets
		// the lease lapse.
		go n.runLease(ctx)
	} else {
		n.holding.Store(true)
		n.bindListeners(ctx)
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

// Holding reports whether this process currently holds the system: without
// a lease, always; with one, only while it is the writer.
func (n *Node) Holding() bool { return n.holding.Load() }

// bindListeners opens the ingress listeners and serves them.
func (n *Node) bindListeners(ctx context.Context) {
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
	for name, c := range n.linkClients() {
		name, c := name, c
		go func() {
			if err := c.Run(ctx); err != nil && ctx.Err() == nil {
				n.Log.Error("link stopped", "peer", name, "err", err)
			}
		}()
		n.Log.Info("link dialling", "peer", name, "addr", c.Addr, "role", c.Hello.Role)
	}
}

// runLease is the standby and the hold: acquire, bind, renew; on losing the
// lease, drain the links and stand by again.
func (n *Node) runLease(ctx context.Context) {
	leaser, ok := n.Store.(store.Leaser)
	if !ok {
		n.Log.Error("lease enabled but the store cannot hold one; binding without it")
		n.holding.Store(true)
		n.bindListeners(ctx)
		return
	}
	system := n.Config.Identity.Designator
	holder := n.Config.Lease.Holder
	if holder == "" {
		host, _ := os.Hostname()
		holder = fmt.Sprintf("%s/%d", host, os.Getpid())
	}
	ttl := n.Config.Lease.TTL
	if ttl <= 0 {
		ttl = 15 * time.Second
	}
	poll := ttl / 3
	for ctx.Err() == nil {
		got, err := leaser.Acquire(ctx, system, holder, ttl)
		if err != nil {
			n.Log.Warn("lease acquire failed", "system", system, "err", err)
		}
		if !got {
			if n.standbyLogged.CompareAndSwap(false, true) {
				n.Log.Info("standing by", "system", system, "holder", holder)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(poll):
			}
			continue
		}
		n.standbyLogged.Store(false)
		n.Log.Info("holding the system", "system", system, "holder", holder, "ttl", ttl.String())
		lctx, cancel := context.WithCancel(ctx)
		n.holding.Store(true)
		n.leaseMu.Lock()
		n.leaseHolder = holder
		n.leaseMu.Unlock()
		n.bindListeners(lctx)
		for lctx.Err() == nil {
			select {
			case <-ctx.Done():
				// Shutting down. The lease is given back by Drain or Close,
				// after the links have gone quiet, so the standby does not
				// bind while this process still has answers in flight.
				cancel()
				return
			case <-time.After(poll):
			}
			ok, err := leaser.Renew(lctx, system, holder, ttl)
			if err != nil {
				n.Log.Warn("lease renew failed", "system", system, "err", err)
				continue // the term has time in it; try again next poll
			}
			if !ok {
				break
			}
		}
		// Lost: stop being the writer before anything else, then drop the
		// links so partners redial to whoever holds it now.
		n.holding.Store(false)
		n.Log.Error("lease lost; dropping the links", "system", system, "holder", holder)
		cancel()
		for _, in := range n.listeners {
			in.Close() //nolint:errcheck
		}
		if ls, tcp, err := n.buildIngress(); err == nil {
			n.listeners, n.tcp = ls, tcp
		} else {
			n.Log.Error("could not rebuild the ingress after losing the lease", "err", err)
			return
		}
	}
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
// releaseLease gives the system back, once, if this process holds it.
func (n *Node) releaseLease() {
	n.leaseMu.Lock()
	holder := n.leaseHolder
	n.leaseHolder = ""
	n.leaseMu.Unlock()
	if holder == "" {
		return
	}
	if leaser, ok := n.Store.(store.Leaser); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := leaser.Release(ctx, n.Config.Identity.Designator, holder); err != nil {
			n.Log.Warn("lease release failed", "err", err)
		} else {
			n.Log.Info("lease released", "system", n.Config.Identity.Designator, "holder", holder)
		}
	}
	n.holding.Store(false)
}

func (n *Node) Close() {
	n.releaseLease()
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
	// Quiet now: the standby may have the system.
	n.releaseLease()
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
	for name, c := range n.linkClients() {
		if c.Connected() {
			add([]string{name})
		}
	}
	// The router's peer list is configuration, not liveness: senders are
	// registered at boot and never unregistered, so including them here made
	// every peer that ever connected read as live forever.
	return out
}

func fmtErr(what string, err error) error { return fmt.Errorf("node: %s: %w", what, err) }

// SpoolReadyAge is how old the spool's oldest unflushed entry may be before
// the node reports not ready: the store is not keeping up, and a load
// balancer should send partners' new sessions to a node whose is.
var SpoolReadyAge = 30 * time.Second

// backlog is what the readiness rule asks of a spool.
type backlog interface {
	Oldest() (time.Duration, bool, error)
}

// spoolReady is the readiness rule for the write-ahead spool.
func spoolReady(sp backlog, maxAge time.Duration) error {
	if sp == nil {
		return nil
	}
	age, ok, err := sp.Oldest()
	if err != nil {
		return fmt.Errorf("spool: %w", err)
	}
	if ok && age > maxAge {
		return fmt.Errorf("spool backlog: oldest entry %s old, store not keeping up", age.Round(time.Second))
	}
	return nil
}

// SetPeerToken gives a peer a shared secret on every hello-identified
// listener of this node, or clears it with "". A link the peer holds now
// is cut, so whoever presents the token next is the peer. It is how a
// world hands one of its carriers to a node it does not run.
func (n *Node) SetPeerToken(peer, token string) {
	for _, t := range n.tcp {
		t.SetToken(peer, token)
		t.CloseSession(peer)
	}
}

// linkClients is a snapshot of the links this node holds open itself; the
// map is written when a link is added while running.
func (n *Node) linkClients() map[string]*transport.Client {
	n.linksMu.Lock()
	defer n.linksMu.Unlock()
	out := make(map[string]*transport.Client, len(n.links))
	for k, v := range n.links {
		out[k] = v
	}
	return out
}
