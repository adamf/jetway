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

	"github.com/adamf/jetway/internal/config"
	"github.com/adamf/jetway/internal/ingress"
	"github.com/adamf/jetway/internal/node"
	"github.com/adamf/jetway/internal/telemetry"
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

	// The assembly lives in internal/node so that the scenario suite drives
	// this node rather than a second copy of it.
	nd, err := node.Build(ctx, cfg, log, node.Options{LocatorSecret: secret})
	if err != nil {
		return err
	}
	defer nd.Close()

	if err := nd.Start(ctx); err != nil {
		return err
	}
	if err := nd.Serve(ctx, drainTimeout); err != nil && !errors.Is(err, http.ErrServerClosed) {
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

// matipSenders lets registerPeers reach MATIP sessions for replies.
var matipSenders = map[string]*ingress.MATIP{}

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
