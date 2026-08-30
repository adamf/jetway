package scenario

import (
	"context"
	"fmt"
	"time"
)

// Scenario is one end-to-end exchange through a node, with its own check.
//
// A scenario asserts by returning an error rather than by taking a *testing.T,
// which is what lets the load driver run the identical code. If it could only
// fail a test, the load run would be exercising an unchecked path -- measuring
// how fast something wrong can go.
type Scenario struct {
	// Name identifies the scenario in test output and in the load report.
	Name string
	// What it demonstrates, in one line, for the load report's benefit.
	About string
	// Run performs the exchange. seq is unique per invocation across the whole
	// run, and every scenario must use it to keep its identifiers distinct:
	// the load driver runs the same scenario thousands of times concurrently,
	// and a scenario that books the same passenger twice is measuring lock
	// contention it invented.
	Run func(ctx context.Context, h *Harness, seq int) error
	// Mutates marks a scenario that changes shared state -- carrier inventory,
	// a queue's contents -- in a way that makes it unsafe to run concurrently
	// with itself. The load driver keeps these to a single worker.
	Mutates bool
	// SkipUnderLoad marks a scenario that is meaningful once but meaningless
	// repeated: it asserts on a global count, or waits on a timer.
	SkipUnderLoad bool
}

// All returns every scenario, in the order a reader should meet them.
func All() []Scenario {
	return []Scenario{
		BookDomestic(),
		BookInterline(),
		BookEDIFACT(),
		FreeSale(),
		TypeBRoundTrip(),
		DuplicateSuppressed(),
		CancelBooking(),
		IssueTicket(),
		IssueEMD(),
		SplitBooking(),
		ScheduleChange(),
		TicketingDeadline(),
		UndecodableToDLQ(),
		AvailabilityCached(),
	}
}

// ByName returns the named scenarios, or an error naming what is available.
func ByName(names []string) ([]Scenario, error) {
	if len(names) == 0 {
		return All(), nil
	}
	index := map[string]Scenario{}
	var known []string
	for _, s := range All() {
		index[s.Name] = s
		known = append(known, s.Name)
	}
	var out []Scenario
	for _, n := range names {
		s, ok := index[n]
		if !ok {
			return nil, fmt.Errorf("no scenario %q; have %v", n, known)
		}
		out = append(out, s)
	}
	return out, nil
}

// eventually retries a condition until it holds or the deadline passes.
//
// The carriers answer over real TCP sessions, so a reply is not synchronous
// with the request that provoked it. Polling for the outcome is honest about
// that; a fixed sleep would be either flaky or slow, and under load it would
// be both.
func eventually(ctx context.Context, within time.Duration, what string, cond func() (bool, error)) error {
	deadline := time.Now().Add(within)
	var last error
	for {
		ok, err := cond()
		if err != nil {
			last = err
		} else if ok {
			return nil
		}
		if time.Now().After(deadline) {
			if last != nil {
				return fmt.Errorf("%s did not happen within %s: %w", what, within, last)
			}
			return fmt.Errorf("%s did not happen within %s", what, within)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// settle is how long a scenario waits for a carrier to answer.
const settle = 10 * time.Second

// concurrencySafe drops the scenarios that cannot run alongside themselves.
//
// Two kinds are excluded. One mutates shared state -- carrier inventory, a
// flight's schedule -- so a second copy would be asserting on what the first
// did. The other is meaningful once and meaningless repeated, because it
// checks a global rather than its own record.
func concurrencySafe(in []Scenario) []Scenario {
	var out []Scenario
	for _, s := range in {
		if s.Mutates || s.SkipUnderLoad {
			continue
		}
		out = append(out, s)
	}
	return out
}
