// Command jetwayd runs the Jetway gateway: the PNR store, the message
// pipeline, the partner ingress listeners and the operations console.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/adamf/jetway/internal/api"
	"github.com/adamf/jetway/internal/config"
	"github.com/adamf/jetway/internal/demo"
	"github.com/adamf/jetway/internal/egress"
	"github.com/adamf/jetway/internal/gateway"
	"github.com/adamf/jetway/internal/ingress"
	"github.com/adamf/jetway/internal/metrics"
	"github.com/adamf/jetway/internal/queue"
	"github.com/adamf/jetway/internal/spool"
	"github.com/adamf/jetway/internal/store"
	"github.com/adamf/jetway/internal/telemetry"
	"github.com/adamf/jetway/pkg/avail"
)

// drainTimeout bounds how long shutdown waits for in-flight work. Long enough
// for a message mid-pipeline to finish; short enough that an orchestrator does
// not lose patience and send SIGKILL.
const drainTimeout = 20 * time.Second

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "jetwayd:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		configPath = flag.String("config", envOr("JETWAY_CONFIG", ""),
			"path to a YAML configuration file; without one, the loopback demo runs")
		httpAddr  = flag.String("http", "", "override http.addr")
		backend   = flag.String("store", "", "override store.backend: mem or postgres")
		dsn       = flag.String("dsn", envOr("JETWAY_DSN", ""), "override store.dsn")
		noDemo    = flag.Bool("no-demo-carriers", false, "do not run the simulated carrier fleet")
		printConf = flag.Bool("print-config", false, "print the effective configuration and exit")
		verbose   = flag.Bool("v", false, "debug logging")
	)
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	cfg, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	if *httpAddr != "" {
		cfg.HTTP.Addr = *httpAddr
	}
	if *backend != "" {
		cfg.Store.Backend = *backend
	}
	if *dsn != "" {
		cfg.Store.DSN = *dsn
	}
	if *noDemo {
		cfg.Demo.Carriers = false
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	if *printConf {
		return printConfig(cfg)
	}

	secret, err := locatorSecret(cfg.LocatorSecret, log)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := openStore(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer st.Close()

	var sp *spool.Spool
	if cfg.Spool.Enabled {
		if sp, err = spool.Open(cfg.Spool.Dir); err != nil {
			return err
		}
		log.Info("write-ahead spool enabled", "dir", sp.Dir())
	} else {
		log.Warn("write-ahead spool disabled",
			"consequence", "a store outage becomes refused acknowledgements to partners")
	}

	service := cfg.Telemetry.ServiceName
	if service == "" {
		service = cfg.Identity.Name
	}
	shutdownTracing, err := telemetry.Setup(ctx, telemetry.Config{
		Endpoint: cfg.Telemetry.Endpoint, Headers: cfg.Telemetry.Headers,
		ServiceName: service, Environment: cfg.Telemetry.Environment,
		SampleRatio: cfg.Telemetry.SampleRatio,
	})
	if err != nil {
		return fmt.Errorf("telemetry: %w", err)
	}
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownTracing(sctx); err != nil {
			log.Warn("tracing did not shut down cleanly", "err", err)
		}
	}()
	if cfg.Telemetry.Endpoint != "" {
		log.Info("tracing enabled", "endpoint", cfg.Telemetry.Endpoint, "service", service)
	}

	bus := gateway.NewBus(400)
	gw := gateway.New(gateway.Identity{
		Designator: cfg.Identity.Designator,
		TTYAddress: cfg.Identity.TTYAddress,
		Name:       cfg.Identity.Name,
	}, st, bus, log, secret)

	gw.Avail = avail.NewCache()
	gw.Log.Info("availability cache ready", "trust_window", gw.Avail.StaleAfter)

	gw.Relay = cfg.Routing.Relay
	if gw.Relay {
		log.Warn("address relay enabled",
			"consequence", "messages addressed to other links will be forwarded",
			"check", "only do this for links you would answer for")
	}

	// Queue state lives in the store because a worklist has to be listed,
	// counted and audited. Publish is the seam for an external broker that
	// wants to be told rather than to poll; nothing is configured here yet.
	gw.Queues = &queue.Manager{
		Store: st, Log: log,
		Notify: func(item *store.QueueItem) { bus.Publish(gateway.EvQueue, item) },
	}

	router := egress.NewRouter(st, log)
	gw.Sender = router

	// Ingress listeners bind before anything else starts, so a port conflict or
	// an unreadable certificate fails at startup rather than in a goroutine.
	listeners, tcpIngress, err := buildIngress(cfg, log)
	if err != nil {
		return err
	}

	if err := registerPeers(cfg, gw, router, tcpIngress, log); err != nil {
		return err
	}

	handler := makeHandler(gw, sp, log)
	for _, in := range listeners {
		in := in
		go func() {
			if err := in.Start(ctx, handler); err != nil && ctx.Err() == nil {
				log.Error("ingress stopped", "ingress", in.Name(), "err", err)
			}
		}()
		log.Info("ingress listening", "name", in.Name(), "addr", in.Addr())
	}

	if sp != nil {
		go drainSpool(ctx, sp, gw, cfg.Spool.DrainInterval, log)
	}
	go router.Run(ctx)
	if n, err := router.Recover(ctx); err != nil {
		log.Error("could not recover undelivered messages", "err", err)
	} else if n > 0 {
		log.Info("queued undelivered messages for redelivery", "count", n)
	}

	fleet, err := startDemo(ctx, cfg, listeners, bus, log)
	if err != nil {
		return err
	}

	srv := &api.Server{
		Gateway: gw, Store: st, Bus: bus, Log: log, Fleet: fleet,
		Console: cfg.HTTP.Console,
		Metrics: cfg.HTTP.Metrics,
		Ready: func(ctx context.Context) error {
			_, err := st.ListPNRs(ctx, 1)
			return err
		},
		LinkPeers: func() []string { return livePeers(tcpIngress, router) },
	}
	registerRuntimeMetrics(sp, router)
	metrics.Default.OnCollect(func() {
		metrics.Gauge("jetway_availability_entries", "availability beliefs currently held",
			nil, float64(gw.Avail.Len()))
	})
	go purgeAvailability(ctx, gw, log)

	// The sweeper is what notices silence: a request nobody answered, a
	// ticketing deadline that passed. Nothing else can, because neither is an
	// event a partner sends.
	sweeper := &queue.Sweeper{Records: st, Queues: gw.Queues, Log: log}
	go sweeper.Run(ctx, time.Minute)

	hs := &http.Server{
		Addr: cfg.HTTP.Addr, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		log.Info("draining")
		dctx, cancel := context.WithTimeout(context.Background(), drainTimeout)
		defer cancel()
		// Stop taking new work first, let what is in flight finish, then stop
		// serving. Cutting links first would lose messages mid-pipeline.
		for _, in := range listeners {
			if d, ok := in.(interface{ Drain(context.Context) error }); ok {
				if err := d.Drain(dctx); err != nil {
					log.Warn("ingress drain", "ingress", in.Name(), "err", err)
				}
			} else {
				in.Close() //nolint:errcheck
			}
		}
		hs.Shutdown(dctx) //nolint:errcheck
	}()

	log.Info("console ready", "url", "http://"+cfg.HTTP.Addr,
		"identity", cfg.Identity.Designator, "store", cfg.Store.Backend)
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	log.Info("stopped")
	return nil
}

func loadConfig(path string) (*config.Config, error) {
	if path == "" {
		c := config.Default()
		return c, c.Validate()
	}
	return config.Load(path)
}

func printConfig(c *config.Config) error {
	fmt.Printf("identity:   %s %s (%s)\n", c.Identity.Designator, c.Identity.TTYAddress, c.Identity.Name)
	fmt.Printf("store:      %s\n", c.Store.Backend)
	fmt.Printf("spool:      enabled=%t dir=%s\n", c.Spool.Enabled, c.Spool.Dir)
	fmt.Printf("http:       %s console=%t metrics=%t\n", c.HTTP.Addr, c.HTTP.Console, c.HTTP.Metrics)
	for _, in := range c.Ingress {
		fmt.Printf("ingress:    %-16s %-9s %-22s tls=%t mtls=%t\n",
			in.Name, in.Type, addrOrDir(in), in.TLS != nil, in.TLS.Mutual())
	}
	for _, p := range c.Peers {
		fmt.Printf("peer:       %-6s carrier=%-3s format=%-8s egress=%s\n",
			p.Name, p.Carrier, p.Format, p.Egress.Type)
	}
	return nil
}

func addrOrDir(in config.Ingress) string {
	if in.Type == "filedrop" {
		return in.Dir
	}
	return in.Addr
}

func openStore(ctx context.Context, cfg *config.Config, log *slog.Logger) (store.Store, error) {
	switch cfg.Store.Backend {
	case "mem":
		m := store.NewMem()
		m.MaxMessages, m.MaxRecords = cfg.Store.MaxMessages, cfg.Store.MaxRecords
		if m.MaxMessages == 0 && m.MaxRecords == 0 {
			log.Warn("using the in-memory store unbounded; nothing survives a restart and memory grows with traffic")
		} else {
			log.Info("using the in-memory store", "max_messages", m.MaxMessages, "max_records", m.MaxRecords)
		}
		return m, nil
	case "postgres":
		pg, err := store.OpenPostgres(ctx, cfg.Store.DSN)
		if err != nil {
			return nil, err
		}
		if cfg.Store.Migrate {
			if err := store.MigrateSchema(ctx, pg); err != nil {
				pg.Close()
				return nil, fmt.Errorf("apply schema: %w", err)
			}
		}
		log.Info("connected to postgres")
		return pg, nil
	}
	return nil, fmt.Errorf("unknown store backend %q", cfg.Store.Backend)
}

// buildIngress constructs and binds every listener.
func buildIngress(cfg *config.Config, log *slog.Logger) ([]ingress.Ingress, map[string]*ingress.TCP, error) {
	var out []ingress.Ingress
	tcps := map[string]*ingress.TCP{}
	matips := map[string]*ingress.MATIP{}
	for _, ic := range cfg.Ingress {
		switch ic.Type {
		case "tcp":
			t, err := ingress.NewTCP(ic, log)
			if err != nil {
				return nil, nil, err
			}
			if err := t.Listen(); err != nil {
				return nil, nil, err
			}
			tcps[ic.Name] = t
			out = append(out, t)
		case "matip":
			mp, err := ingress.NewMATIP(ic, log)
			if err != nil {
				return nil, nil, err
			}
			if err := mp.Listen(); err != nil {
				return nil, nil, err
			}
			matips[ic.Name] = mp
			out = append(out, mp)
		case "https":
			h, err := ingress.NewHTTPS(ic, log)
			if err != nil {
				return nil, nil, err
			}
			if err := h.Listen(); err != nil {
				return nil, nil, err
			}
			out = append(out, h)
		case "filedrop":
			f, err := ingress.NewFileDrop(ic, log)
			if err != nil {
				return nil, nil, err
			}
			out = append(out, f)
		}
	}
	// Replies on a MATIP session go back down that session, so those listeners
	// join the same reply path as the plain TCP ones.
	for name, mp := range matips {
		matipSenders[name] = mp
	}
	return out, tcps, nil
}

// matipSenders lets registerPeers reach MATIP sessions for replies.
var matipSenders = map[string]*ingress.MATIP{}

// registerPeers wires each configured partner into routing and egress.
func registerPeers(cfg *config.Config, gw *gateway.Gateway, router *egress.Router,
	tcps map[string]*ingress.TCP, log *slog.Logger) error {

	// Replies on an inbound link go back down whichever TCP listener currently
	// holds a session with that peer.
	sessions := func(ctx context.Context, peer string, raw []byte) error {
		for _, t := range tcps {
			if err := t.Send(ctx, peer, raw); err == nil {
				return nil
			}
		}
		for _, m := range matipSenders {
			if err := m.Send(ctx, peer, raw); err == nil {
				return nil
			}
		}
		return fmt.Errorf("no open link to %q", peer)
	}

	for _, p := range cfg.Peers {
		format := store.FormatTypeB
		if p.Format == "edifact" {
			format = store.FormatEDIFACT
		}
		gw.AddPeer(&gateway.Peer{
			Name: p.Name, Carrier: p.Carrier, Format: format,
			TTYAddress: p.TTYAddress, Addresses: p.Addresses, CONTRL: p.CONTRL,
		})
		s, err := egress.Build(p, sessions, log)
		if err != nil {
			return err
		}
		router.Register(p.Name, s, p.Egress.Retry, format)
		log.Info("peer configured", "name", p.Name, "carrier", p.Carrier,
			"format", p.Format, "egress", s.Describe())
	}
	return nil
}

// makeHandler builds the function every ingress calls.
func makeHandler(gw *gateway.Gateway, sp *spool.Spool, log *slog.Logger) ingress.Handler {
	return func(ctx context.Context, m ingress.Message) (ingress.Receipt, error) {
		start := time.Now()
		defer func() {
			metrics.Observe("jetway_ingest_seconds", "time to accept an inbound message",
				metrics.Labels{"transport": m.Transport}, time.Since(start).Seconds())
		}()

		// A spool decouples acknowledging the partner from the store being up.
		// Synchronous exchanges cannot use it: the caller is holding the
		// connection open waiting for a reply that only processing produces.
		if sp != nil && !m.Synchronous {
			e := spool.Entry{
				ID: gateway.NewMessageID(), Peer: m.Peer, Transport: m.Transport,
				At: time.Now().UTC(), Raw: m.Raw,
			}
			if err := sp.Put(e); err != nil {
				return ingress.Receipt{}, fmt.Errorf("spool: %w", err)
			}
			return ingress.Receipt{ID: e.ID}, nil
		}

		res, err := gw.IngestWith(ctx, m.Peer, m.Raw, gateway.IngestOptions{
			Transport: m.Transport, Remote: m.Remote,
			HoldReply: m.Synchronous, FromFile: m.FromFile,
		})
		if err != nil {
			return ingress.Receipt{}, err
		}
		return ingress.Receipt{ID: res.MessageID, Reply: res.Reply}, nil
	}
}

// drainSpool moves spooled messages into the pipeline.
func drainSpool(ctx context.Context, sp *spool.Spool, gw *gateway.Gateway, interval time.Duration, log *slog.Logger) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		ids, err := sp.List()
		if err != nil {
			log.Error("could not list the spool", "err", err)
		}
		progress := false
		for _, id := range ids {
			if ctx.Err() != nil {
				return
			}
			e, err := sp.Get(id)
			if err != nil {
				log.Error("could not read a spooled message", "id", id, "err", err)
				continue
			}
			if _, err := gw.IngestWith(ctx, e.Peer, e.Raw, gateway.IngestOptions{
				Transport: e.Transport, Remote: "spool",
			}); err != nil {
				// The store is still unhappy. Leave the entry where it is; the
				// bytes are safe and the next sweep tries again.
				log.Warn("spooled message not yet accepted, will retry",
					"id", id, "attempts", e.Attempts, "err", err)
				break
			}
			if err := sp.Done(id); err != nil {
				log.Error("could not clear a drained spool entry", "id", id, "err", err)
			}
			progress = true
		}
		if progress {
			continue // keep going while the backlog is moving
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func startDemo(ctx context.Context, cfg *config.Config, listeners []ingress.Ingress,
	bus *gateway.Bus, log *slog.Logger) (*demo.RunningFleet, error) {
	if !cfg.Demo.Carriers {
		return nil, nil
	}
	// Each simulated carrier dials the listener configured to identify it,
	// which is what a real partner circuit looks like.
	byName := map[string]ingress.Ingress{}
	for _, in := range listeners {
		byName[in.Name()] = in
	}
	peerListener := map[string]string{}
	for _, ic := range cfg.Ingress {
		if ic.Type != "tcp" || ic.Identify.Peer == "" {
			continue
		}
		if in, ok := byName[ic.Name]; ok {
			peerListener[ic.Identify.Peer] = in.Addr()
		}
	}
	addrFor := func(c demo.Carrier) string {
		if a, ok := cfg.Demo.LinkAddrs[c.Designator]; ok {
			return a
		}
		return peerListener[c.Designator]
	}
	return demo.StartFleet(ctx, demo.Fleet, addrFor, bus, log)
}

func livePeers(tcps map[string]*ingress.TCP, router *egress.Router) []string {
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
	for _, t := range tcps {
		add(t.Peers())
	}
	for _, m := range matipSenders {
		add(m.Peers())
	}
	return out
}

// registerRuntimeMetrics exposes the numbers worth alerting on.
func registerRuntimeMetrics(sp *spool.Spool, router *egress.Router) {
	metrics.Default.OnCollect(func() {
		metrics.Gauge("jetway_egress_retry_queue", "messages awaiting redelivery", nil,
			float64(router.QueueDepth()))
		if sp == nil {
			return
		}
		// A rising spool means the store is not keeping up, or is down. It is
		// the single most important number here.
		if n, err := sp.Depth(); err == nil {
			metrics.Gauge("jetway_spool_depth", "inbound messages not yet persisted", nil, float64(n))
		}
		if age, ok, err := sp.Oldest(); err == nil && ok {
			metrics.Gauge("jetway_spool_oldest_seconds", "age of the oldest unpersisted message",
				nil, age.Seconds())
		}
	})
}

// purgeAvailability drops beliefs that have gone stale or whose flight has
// departed, so the cache does not grow without bound.
func purgeAvailability(ctx context.Context, gw *gateway.Gateway, log *slog.Logger) {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n := gw.Avail.Purge(); n > 0 {
				log.Debug("purged stale availability", "entries", n)
			}
		}
	}
}

func locatorSecret(configured string, log *slog.Logger) ([]byte, error) {
	hexKey := envOr("JETWAY_LOCATOR_SECRET", configured)
	if hexKey != "" {
		b, err := hex.DecodeString(hexKey)
		if err != nil {
			return nil, fmt.Errorf("locator secret must be hex: %w", err)
		}
		if len(b) < 16 {
			return nil, fmt.Errorf("locator secret must be at least 16 bytes")
		}
		return b, nil
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	log.Warn("generated an ephemeral record locator secret",
		"action", "set JETWAY_LOCATOR_SECRET to a stable value before running this for real",
		"why", "a changing secret remaps the locator space and will eventually collide")
	return b, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
