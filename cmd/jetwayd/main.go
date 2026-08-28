// Command jetwayd runs the Jetway gateway: the PNR store, the message pipeline,
// the carrier link server and the operations console.
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
	"github.com/adamf/jetway/internal/demo"
	"github.com/adamf/jetway/internal/gateway"
	"github.com/adamf/jetway/internal/store"
	"github.com/adamf/jetway/internal/transport"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "jetwayd:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		httpAddr  = flag.String("http", "127.0.0.1:8080", "address for the console and API")
		linkAddr  = flag.String("link", "127.0.0.1:9100", "address for carrier links")
		backend   = flag.String("store", "mem", "storage backend: mem or postgres")
		dsn       = flag.String("dsn", envOr("JETWAY_DSN", ""), "PostgreSQL DSN when -store=postgres")
		migrate   = flag.Bool("migrate", true, "apply the schema on start when using postgres")
		withFleet = flag.Bool("demo-carriers", true,
			"run the simulated carrier fleet in this process, connected over real TCP links")
		designator = flag.String("designator", "1J", "our two-character company code")
		ttyAddr    = flag.String("tty", "LONRM1J", "our seven-character Type B address")
		secretHex  = flag.String("locator-secret", envOr("JETWAY_LOCATOR_SECRET", ""),
			"hex secret keying record locator allocation; generated if empty")
		verbose = flag.Bool("v", false, "debug logging")
	)
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	secret, err := locatorSecret(*secretHex, log)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := openStore(ctx, *backend, *dsn, *migrate, log)
	if err != nil {
		return err
	}
	defer st.Close()

	bus := gateway.NewBus(1000)
	gw := gateway.New(gateway.Identity{
		Designator: *designator, TTYAddress: *ttyAddr, Name: "jetway",
	}, st, bus, log, secret)

	links := &transport.Server{
		Addr: *linkAddr, Framer: transport.DefaultFramer(), Log: log,
		OnMessage: func(ctx context.Context, peer string, raw []byte) error {
			_, err := gw.Ingest(ctx, peer, raw)
			return err
		},
		OnConnect: func(peer, format string) {
			bus.Publish(gateway.EvLink, map[string]any{
				"node": *designator, "peer": peer, "state": "up", "format": format})
		},
		OnDisconnect: func(peer string) {
			bus.Publish(gateway.EvLink, map[string]any{
				"node": *designator, "peer": peer, "state": "down"})
		},
	}
	if err := links.Listen(); err != nil {
		return fmt.Errorf("link listener: %w", err)
	}
	gw.Sender = links

	for _, c := range demo.Fleet {
		gw.AddPeer(&gateway.Peer{
			Name: c.Designator, Carrier: c.Designator,
			Format: c.Format, TTYAddress: c.TTYAddress,
		})
	}

	go func() {
		if err := links.Serve(ctx); err != nil {
			log.Error("link server stopped", "err", err)
		}
	}()
	log.Info("carrier link server listening", "addr", links.Addr)

	var fleet *demo.RunningFleet
	if *withFleet {
		// The simulated carriers connect back over the loopback link server, so
		// the transport, framing and reconnection paths are exercised for real
		// even when everything runs in one process.
		fleet, err = demo.StartFleet(ctx, demo.Fleet, links.Addr, bus, log)
		if err != nil {
			return err
		}
	}

	srv := &api.Server{Gateway: gw, Store: st, Bus: bus, Log: log, Links: links, Fleet: fleet}
	hs := &http.Server{
		Addr:              *httpAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		sd, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		hs.Shutdown(sd) //nolint:errcheck // shutting down anyway
	}()

	log.Info("console ready", "url", "http://"+*httpAddr)
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	log.Info("stopped")
	return nil
}

func openStore(ctx context.Context, backend, dsn string, migrate bool, log *slog.Logger) (store.Store, error) {
	switch backend {
	case "mem":
		log.Warn("using the in-memory store; nothing survives a restart")
		return store.NewMem(), nil
	case "postgres":
		if dsn == "" {
			return nil, fmt.Errorf("-store=postgres needs -dsn or JETWAY_DSN")
		}
		pg, err := store.OpenPostgres(ctx, dsn)
		if err != nil {
			return nil, err
		}
		if migrate {
			if err := store.MigrateSchema(ctx, pg); err != nil {
				pg.Close()
				return nil, fmt.Errorf("apply schema: %w", err)
			}
		}
		log.Info("connected to postgres")
		return pg, nil
	}
	return nil, fmt.Errorf("unknown store backend %q", backend)
}

// locatorSecret resolves the key behind record locator allocation.
//
// A generated secret is fine for a demo and wrong for a deployment: it changes
// on every restart, which remaps the locator space and will eventually reissue a
// locator that is already in use. Say so loudly rather than let it pass.
func locatorSecret(hexKey string, log *slog.Logger) ([]byte, error) {
	if hexKey != "" {
		b, err := hex.DecodeString(hexKey)
		if err != nil {
			return nil, fmt.Errorf("-locator-secret must be hex: %w", err)
		}
		if len(b) < 16 {
			return nil, fmt.Errorf("-locator-secret must be at least 16 bytes")
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
