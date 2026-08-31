// Command carriersim runs a simulated airline reservation system.
//
// It connects to a Jetway link server, decodes the reservation messages it
// receives in whichever dialect it is configured for, keeps its own passenger
// name records, answers from its own seat inventory, and replies. It exists so
// that a gateway can be exercised end to end without a real interline partner,
// and so that a new carrier profile can be developed against something that
// behaves like the other side of a link.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/adamf/jetway/pkg/gateway"
	"github.com/adamf/jetway/pkg/store"
	"github.com/adamf/jetway/pkg/transport"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "carriersim:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		carrier  = flag.String("carrier", "BA", "two-character airline designator")
		linkAddr = flag.String("link", "127.0.0.1:9100", "gateway link server address")
		format   = flag.String("format", "typeb", "wire format for this link: typeb or edifact")
		ttyAddr  = flag.String("tty", "", "our seven-character Type B address; derived if empty")
		capacity = flag.Int("capacity", 0, "seats per booking class; 0 derives a value per flight")
		waitlist = flag.Int("waitlist", 2, "seats that may be waitlisted once a class is sold out")
		closed   = flag.String("closed-classes", "Z", "comma-separated classes that are never available")
		httpAddr = flag.String("http", "", "optional address for a read-only status endpoint")
		verbose  = flag.Bool("v", false, "debug logging")
	)
	flag.Parse()

	if len(*carrier) != 2 {
		return fmt.Errorf("-carrier must be exactly two characters")
	}
	var wire store.Format
	switch *format {
	case "typeb":
		wire = store.FormatTypeB
	case "edifact":
		wire = store.FormatEDIFACT
	default:
		return fmt.Errorf("-format must be typeb or edifact")
	}
	tty := *ttyAddr
	if tty == "" {
		// LLLDDCC with a placeholder location and the reservations department.
		tty = "XXXRM" + strings.ToUpper(*carrier)
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st := store.NewMem()
	bus := gateway.NewBus(500)
	gw := gateway.New(gateway.Identity{
		Designator: strings.ToUpper(*carrier), TTYAddress: strings.ToUpper(tty),
		Name: strings.ToUpper(*carrier) + " res",
	}, st, bus, log, []byte("carriersim-"+*carrier))

	inv := gateway.NewInventory()
	inv.Carrier = strings.ToUpper(*carrier)
	inv.Capacity = *capacity
	inv.WaitlistCapacity = *waitlist
	inv.ClosedClasses = map[string]bool{}
	for _, c := range strings.Split(*closed, ",") {
		if c = strings.TrimSpace(strings.ToUpper(c)); c != "" {
			inv.ClosedClasses[c] = true
		}
	}
	gw.Responder = inv

	client := &transport.Client{
		Addr:   *linkAddr,
		Hello:  transport.Hello{Peer: strings.ToUpper(*carrier), Role: "carrier", Format: string(wire)},
		Framer: transport.DefaultFramer(),
		Log:    log,
	}
	gw.Sender = client
	gw.AddPeer(&gateway.Peer{Name: "gds", Format: wire})
	client.OnMessage = func(ctx context.Context, peer string, raw []byte) error {
		res, err := gw.Ingest(ctx, "gds", raw)
		if err != nil {
			return err
		}
		log.Info("processed", "status", res.Status, "locator", res.Locator,
			"changes", len(res.Changes), "replies", len(res.Replies))
		return nil
	}

	if *httpAddr != "" {
		go serveStatus(ctx, *httpAddr, st, inv, log)
	}

	log.Info("carrier simulator starting",
		"carrier", *carrier, "format", *format, "tty", tty, "link", *linkAddr)
	return client.Run(ctx)
}

// serveStatus exposes the carrier's own records and inventory, which is what
// you want when checking that both sides of a link agree.
func serveStatus(ctx context.Context, addr string, st store.Store, inv *gateway.Inventory, log *slog.Logger) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /pnrs", func(w http.ResponseWriter, r *http.Request) {
		recs, err := st.ListPNRs(r.Context(), 100)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"pnrs": recs}) //nolint:errcheck
	})
	mux.HandleFunc("GET /inventory", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"inventory": inv.Snapshot()}) //nolint:errcheck
	})
	mux.HandleFunc("GET /messages", func(w http.ResponseWriter, r *http.Request) {
		msgs, err := st.ListMessages(r.Context(), store.MessageFilter{Limit: 200})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"messages": msgs}) //nolint:errcheck
	})
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { <-ctx.Done(); srv.Close() }() //nolint:errcheck
	log.Info("carrier status endpoint listening", "addr", addr)
	if err := srv.ListenAndServe(); err != nil && ctx.Err() == nil {
		log.Error("status endpoint stopped", "err", err)
	}
}
