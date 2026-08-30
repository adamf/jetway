package scenario

import (
	"context"
	"os"
	"testing"
	"time"
)

// The integration suite. Every scenario runs once against a real node with
// real carrier links, and the same scenarios are what jetwayload drives under
// concurrency -- so a path that is fast is also a path something checked.
func TestScenarios(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	h, err := Start(ctx, Options{DSN: os.Getenv("JETWAY_TEST_DSN"), Verbose: testing.Verbose()})
	if err != nil {
		t.Fatalf("start a node: %v", err)
	}
	defer h.Stop()

	for _, sc := range All() {
		sc := sc
		t.Run(sc.Name, func(t *testing.T) {
			if err := sc.Run(ctx, h, nextSeq()); err != nil {
				t.Fatalf("%s: %v\n(%s)", sc.Name, err, sc.About)
			}
		})
	}
}

// Concurrency is where the interesting failures are, and it is the mode the
// load driver runs in, so the suite exercises it rather than trusting it.
func TestScenariosConcurrently(t *testing.T) {
	if testing.Short() {
		t.Skip("concurrency pass takes a few seconds")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	h, err := Start(ctx, Options{DSN: os.Getenv("JETWAY_TEST_DSN")})
	if err != nil {
		t.Fatalf("start a node: %v", err)
	}
	defer h.Stop()

	rep, err := Load(ctx, h, LoadOptions{
		Workers: 8, PerWorker: 3,
		Scenarios: concurrencySafe(All()),
	})
	if err != nil {
		t.Fatalf("load pass: %v", err)
	}
	t.Log("\n" + rep.String())
	if rep.Failed > 0 {
		for _, s := range rep.Scenarios {
			for _, e := range s.Errors {
				t.Errorf("%s: %s", s.Name, e)
			}
		}
	}
}
