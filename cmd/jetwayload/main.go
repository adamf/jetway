// Command jetwayload drives the integration scenarios under load.
//
// It runs the same scenarios as `go test ./internal/scenario`, against the same
// assembly `jetwayd` builds, and reports throughput and latency per scenario.
// Sharing the scenarios is the point: a load generator with its own code path
// measures how fast something nobody has checked can run.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/adamf/jetway/internal/scenario"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "jetwayload:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		workers  = flag.Int("workers", 8, "concurrent workers")
		each     = flag.Int("each", 0, "iterations per worker; ignored when -for is set")
		duration = flag.Duration("for", 0, "run for this long instead of a fixed count")
		dsn      = flag.String("dsn", os.Getenv("JETWAY_DSN"),
			"postgres DSN; empty runs against the in-memory store")
		only     = flag.String("only", "", "comma-separated scenario names; empty runs all safe ones")
		list     = flag.Bool("list", false, "list the scenarios and exit")
		asJSON   = flag.Bool("json", false, "emit the report as JSON")
		capacity = flag.Int("capacity", 0, "seats per class per flight in the simulated carriers")
		verbose  = flag.Bool("v", false, "let the node's own logging through")
	)
	flag.Parse()

	if *list {
		for _, s := range scenario.All() {
			flags := []string{}
			if s.Mutates {
				flags = append(flags, "mutates shared state")
			}
			if s.SkipUnderLoad {
				flags = append(flags, "skipped under load")
			}
			note := ""
			if len(flags) > 0 {
				note = "  [" + strings.Join(flags, "; ") + "]"
			}
			fmt.Printf("%-22s %s%s\n", s.Name, s.About, note)
		}
		return nil
	}

	// Empty means "everything safe to run concurrently", which Load decides.
	// Passing every scenario would include the ones that mutate shared state,
	// and they fail under concurrency for reasons that say nothing about the
	// system: a bounded sweep cannot raise one particular record when
	// thousands are due, and a scenario that cancels a flight breaks whichever
	// booking scenario is mid-flight beside it.
	var scenarios []scenario.Scenario
	if *only != "" {
		names := strings.Split(*only, ",")
		for i := range names {
			names[i] = strings.TrimSpace(names[i])
		}
		var err error
		if scenarios, err = scenario.ByName(names); err != nil {
			return err
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Fprintf(os.Stderr, "starting a node (%s store) and waiting for links...\n",
		backendName(*dsn))
	h, err := scenario.Start(ctx, scenario.Options{
		DSN: *dsn, Verbose: *verbose, Capacity: *capacity,
	})
	if err != nil {
		return err
	}
	defer h.Stop()

	rep, err := scenario.Load(ctx, h, scenario.LoadOptions{
		Workers: *workers, PerWorker: *each, Duration: *duration,
		Scenarios: scenarios,
		Progress: func(done, failed int64, elapsed time.Duration) {
			fmt.Fprintf(os.Stderr, "\r%d done, %d failed, %.0f/sec   ",
				done, failed, float64(done)/elapsed.Seconds())
		},
	})
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr)

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	fmt.Print(rep.String())
	// A non-zero exit on failures, so this can gate a pipeline rather than
	// only inform a human reading the table.
	if rep.Failed > 0 {
		return fmt.Errorf("%d of %d scenario runs failed", rep.Failed, rep.Total)
	}
	return nil
}

func backendName(dsn string) string {
	if dsn == "" {
		return "in-memory"
	}
	return "postgres"
}
