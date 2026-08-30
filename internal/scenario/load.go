package scenario

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// LoadOptions configure a load run.
type LoadOptions struct {
	// Workers is how many run concurrently.
	Workers int
	// PerWorker is how many iterations each worker performs. Ignored when
	// Duration is set.
	PerWorker int
	// Duration runs until the time is up rather than for a fixed count.
	Duration time.Duration
	// Scenarios to run. Empty uses every scenario that is safe under
	// concurrency.
	Scenarios []Scenario
	// Progress, when set, is called about once a second with the running
	// totals, so a long run says something rather than sitting silent.
	Progress func(done, failed int64, elapsed time.Duration)
}

// ScenarioReport is one scenario's results across a run.
type ScenarioReport struct {
	Name   string
	About  string
	Runs   int
	Failed int
	// Errors holds distinct failure messages, capped: a thousand copies of one
	// message is not a thousand times more informative.
	Errors []string

	Min, Max time.Duration
	P50      time.Duration
	P95      time.Duration
	P99      time.Duration
	Mean     time.Duration
}

// Report is the whole run.
type Report struct {
	Workers   int
	Elapsed   time.Duration
	Total     int
	Failed    int
	Scenarios []ScenarioReport
}

// Throughput is completed scenarios per second across the run.
func (r Report) Throughput() float64 {
	if r.Elapsed <= 0 {
		return 0
	}
	return float64(r.Total) / r.Elapsed.Seconds()
}

// maxDistinctErrors bounds what a report keeps per scenario.
const maxDistinctErrors = 5

var seqCounter atomic.Int64

// nextSeq hands out an identifier unique across the process.
//
// Every scenario uses it to keep its passengers and messages distinct. Without
// it a load run would book the same passenger from every worker and spend its
// time measuring contention it created rather than throughput the system has.
func nextSeq() int { return int(seqCounter.Add(1)) }

// Load runs the scenarios concurrently and measures them.
//
// The scenarios are the ones from the integration suite, unchanged. That is
// the whole design: a load generator with its own private code path measures
// how fast something nobody has checked can run.
func Load(ctx context.Context, h *Harness, opts LoadOptions) (*Report, error) {
	scenarios := opts.Scenarios
	if len(scenarios) == 0 {
		scenarios = concurrencySafe(All())
	}
	if len(scenarios) == 0 {
		return nil, fmt.Errorf("scenario: nothing to run")
	}
	workers := opts.Workers
	if workers <= 0 {
		workers = 4
	}
	perWorker := opts.PerWorker
	if perWorker <= 0 && opts.Duration <= 0 {
		perWorker = 10
	}

	type sample struct {
		idx int
		d   time.Duration
		err error
	}

	var (
		mu      sync.Mutex
		times   = make([][]time.Duration, len(scenarios))
		errs    = make([]map[string]int, len(scenarios))
		runs    = make([]int, len(scenarios))
		failed  = make([]int, len(scenarios))
		done    atomic.Int64
		failedN atomic.Int64
		dealt   atomic.Int64
	)
	for i := range errs {
		errs[i] = map[string]int{}
	}
	record := func(s sample) {
		mu.Lock()
		defer mu.Unlock()
		runs[s.idx]++
		times[s.idx] = append(times[s.idx], s.d)
		if s.err != nil {
			failed[s.idx]++
			errs[s.idx][s.err.Error()]++
		}
	}

	runCtx := ctx
	var cancel context.CancelFunc
	if opts.Duration > 0 {
		runCtx, cancel = context.WithTimeout(ctx, opts.Duration)
		defer cancel()
	}

	start := time.Now()
	if opts.Progress != nil {
		stop := make(chan struct{})
		defer close(stop)
		go func() {
			t := time.NewTicker(time.Second)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case <-runCtx.Done():
					return
				case <-t.C:
					opts.Progress(done.Load(), failedN.Load(), time.Since(start))
				}
			}
		}()
	}

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; opts.Duration > 0 || i < perWorker; i++ {
				if runCtx.Err() != nil {
					return
				}
				// Deal scenarios from one shared counter rather than from
				// each worker's own index. Striding per worker looks fair and
				// is not: with eight workers and three iterations it covers
				// indices 0..9 and never runs the tenth scenario at all, so a
				// report claims a clean run over work it did not do.
				idx := int(dealt.Add(1)-1) % len(scenarios)
				sc := scenarios[idx]
				t0 := time.Now()
				err := sc.Run(runCtx, h, nextSeq())
				d := time.Since(t0)
				// A run cut short by the duration expiring is not a failure of
				// the scenario; it is the clock.
				if err != nil && runCtx.Err() != nil && ctx.Err() == nil {
					return
				}
				record(sample{idx: idx, d: d, err: err})
				done.Add(1)
				if err != nil {
					failedN.Add(1)
				}
			}
		}(w)
	}
	wg.Wait()
	elapsed := time.Since(start)

	rep := &Report{Workers: workers, Elapsed: elapsed}
	for i, sc := range scenarios {
		sr := ScenarioReport{Name: sc.Name, About: sc.About, Runs: runs[i], Failed: failed[i]}
		if len(times[i]) > 0 {
			sr.Min, sr.Max, sr.P50, sr.P95, sr.P99, sr.Mean = summarise(times[i])
		}
		for msg := range errs[i] {
			if len(sr.Errors) >= maxDistinctErrors {
				break
			}
			sr.Errors = append(sr.Errors, msg)
		}
		sort.Strings(sr.Errors)
		rep.Total += sr.Runs
		rep.Failed += sr.Failed
		rep.Scenarios = append(rep.Scenarios, sr)
	}
	return rep, nil
}

// summarise returns the distribution. It sorts a copy, because the caller's
// slice is the record of what happened in order.
func summarise(in []time.Duration) (min, max, p50, p95, p99, mean time.Duration) {
	d := append([]time.Duration(nil), in...)
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	var total time.Duration
	for _, v := range d {
		total += v
	}
	at := func(q float64) time.Duration {
		i := int(q * float64(len(d)-1))
		return d[i]
	}
	return d[0], d[len(d)-1], at(0.50), at(0.95), at(0.99), total / time.Duration(len(d))
}

// String renders the report as a table.
func (r Report) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d workers, %s elapsed, %d runs, %d failed, %.1f scenarios/sec\n\n",
		r.Workers, r.Elapsed.Round(time.Millisecond), r.Total, r.Failed, r.Throughput())
	fmt.Fprintf(&b, "%-22s %6s %7s %10s %10s %10s %10s\n",
		"scenario", "runs", "failed", "p50", "p95", "p99", "max")
	fmt.Fprintf(&b, "%s\n", strings.Repeat("-", 82))
	for _, s := range r.Scenarios {
		fmt.Fprintf(&b, "%-22s %6d %7d %10s %10s %10s %10s\n",
			s.Name, s.Runs, s.Failed,
			round(s.P50), round(s.P95), round(s.P99), round(s.Max))
	}
	if r.Failed > 0 {
		b.WriteString("\nfailures:\n")
		for _, s := range r.Scenarios {
			for _, e := range s.Errors {
				fmt.Fprintf(&b, "  %s: %s\n", s.Name, e)
			}
		}
	}
	return b.String()
}

func round(d time.Duration) string {
	switch {
	case d == 0:
		return "-"
	case d < time.Millisecond:
		return d.Round(time.Microsecond).String()
	case d < time.Second:
		return d.Round(100 * time.Microsecond).String()
	default:
		return d.Round(time.Millisecond).String()
	}
}
