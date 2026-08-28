// Package metrics exposes counters, gauges and histograms in Prometheus text
// format.
//
// Hand-rolled rather than pulled in, for the same reason the rest of the
// dependency tree is small: carriers audit it, and the exposition format is a
// few hundred lines while the client library brings a protobuf stack. The
// interface is deliberately shaped like the standard one, so swapping in
// prometheus/client_golang later is a mechanical change.
package metrics

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// Labels are the dimensions on a metric sample.
type Labels map[string]string

// key renders labels into a stable, comparable string.
func (l Labels) key() string {
	if len(l) == 0 {
		return ""
	}
	names := make([]string, 0, len(l))
	for k := range l {
		names = append(names, k)
	}
	sort.Strings(names)
	var b strings.Builder
	for i, n := range names {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(n)
		b.WriteByte('=')
		// Quoted so that two different label sets can never render to the same
		// key, which would silently merge two series into one.
		b.WriteString(strconv.Quote(l[n]))
	}
	return b.String()
}

func (l Labels) render() string {
	if len(l) == 0 {
		return ""
	}
	names := make([]string, 0, len(l))
	for k := range l {
		names = append(names, k)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, n := range names {
		parts = append(parts, fmt.Sprintf("%s=%q", n, l[n]))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

type kind int

const (
	kindCounter kind = iota
	kindGauge
	kindHistogram
)

func (k kind) String() string {
	switch k {
	case kindGauge:
		return "gauge"
	case kindHistogram:
		return "histogram"
	}
	return "counter"
}

type series struct {
	labels Labels
	value  atomic.Uint64 // float64 bits for gauges, integer count for counters

	// Histogram state.
	mu      sync.Mutex
	buckets []float64
	counts  []uint64
	sum     float64
	total   uint64
}

type family struct {
	name    string
	help    string
	kind    kind
	buckets []float64

	mu     sync.RWMutex
	series map[string]*series
}

// Registry holds every metric this process exposes.
type Registry struct {
	mu         sync.RWMutex
	families   map[string]*family
	order      []string
	collectors []func()
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{families: map[string]*family{}}
}

// Default is the registry the gateway uses.
var Default = NewRegistry()

func (r *Registry) family(name, help string, k kind, buckets []float64) *family {
	r.mu.RLock()
	f := r.families[name]
	r.mu.RUnlock()
	if f != nil {
		return f
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if f = r.families[name]; f != nil {
		return f
	}
	f = &family{name: name, help: help, kind: k, buckets: buckets, series: map[string]*series{}}
	r.families[name] = f
	r.order = append(r.order, name)
	return f
}

func (f *family) get(l Labels) *series {
	k := l.key()
	f.mu.RLock()
	s := f.series[k]
	f.mu.RUnlock()
	if s != nil {
		return s
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if s = f.series[k]; s != nil {
		return s
	}
	s = &series{labels: l}
	if f.kind == kindHistogram {
		s.buckets = f.buckets
		s.counts = make([]uint64, len(f.buckets))
	}
	f.series[k] = s
	return s
}

// Counter increments a monotonic counter.
func (r *Registry) Counter(name, help string, l Labels) { r.Add(name, help, l, 1) }

// Add increments a monotonic counter by n.
func (r *Registry) Add(name, help string, l Labels, n uint64) {
	r.family(name, help, kindCounter, nil).get(l).value.Add(n)
}

// Gauge sets an instantaneous value.
func (r *Registry) Gauge(name, help string, l Labels, v float64) {
	r.family(name, help, kindGauge, nil).get(l).value.Store(floatBits(v))
}

// DefaultBuckets covers message processing latency, which for a gateway runs
// from well under a millisecond to the seconds a slow database makes it.
var DefaultBuckets = []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}

// Observe records a sample in a histogram. Seconds is the conventional unit.
func (r *Registry) Observe(name, help string, l Labels, v float64) {
	s := r.family(name, help, kindHistogram, DefaultBuckets).get(l)
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, b := range s.buckets {
		if v <= b {
			s.counts[i]++
		}
	}
	s.sum += v
	s.total++
}

// OnCollect registers a callback run immediately before each scrape, for values
// that are cheaper to read on demand than to keep updated.
func (r *Registry) OnCollect(fn func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.collectors = append(r.collectors, fn)
}

func floatBits(v float64) uint64 { return math.Float64bits(v) }

func fromBits(b uint64) float64 { return math.Float64frombits(b) }

// Write renders the registry in Prometheus text exposition format.
func (r *Registry) Write(w *strings.Builder) {
	r.mu.RLock()
	collectors := append([]func(){}, r.collectors...)
	r.mu.RUnlock()

	// Collectors run first: one may create a metric family that does not exist
	// yet, and snapshotting before they run would omit it from this scrape.
	for _, fn := range collectors {
		fn()
	}

	r.mu.RLock()
	order := append([]string{}, r.order...)
	fams := make(map[string]*family, len(r.families))
	for k, v := range r.families {
		fams[k] = v
	}
	r.mu.RUnlock()

	for _, name := range order {
		f := fams[name]
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", f.name, f.help, f.name, f.kind)

		f.mu.RLock()
		keys := make([]string, 0, len(f.series))
		for k := range f.series {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			s := f.series[k]
			switch f.kind {
			case kindHistogram:
				s.mu.Lock()
				// counts is already cumulative: Observe increments every bucket
				// whose upper bound the sample falls under, which is what the
				// exposition format wants.
				for i, b := range s.buckets {
					fmt.Fprintf(w, "%s_bucket%s %d\n", f.name,
						withLE(s.labels, strconv.FormatFloat(b, 'g', -1, 64)), s.counts[i])
				}
				fmt.Fprintf(w, "%s_bucket%s %d\n", f.name, withLE(s.labels, "+Inf"), s.total)
				fmt.Fprintf(w, "%s_sum%s %g\n", f.name, s.labels.render(), s.sum)
				fmt.Fprintf(w, "%s_count%s %d\n", f.name, s.labels.render(), s.total)
				s.mu.Unlock()
			case kindGauge:
				fmt.Fprintf(w, "%s%s %g\n", f.name, s.labels.render(), fromBits(s.value.Load()))
			default:
				fmt.Fprintf(w, "%s%s %d\n", f.name, s.labels.render(), s.value.Load())
			}
		}
		f.mu.RUnlock()
	}
}

func withLE(l Labels, le string) string {
	m := make(Labels, len(l)+1)
	for k, v := range l {
		m[k] = v
	}
	m["le"] = le
	return m.render()
}

// String renders the whole registry.
func (r *Registry) String() string {
	var b strings.Builder
	r.Write(&b)
	return b.String()
}

// Package-level shorthands against the default registry.
func Counter(name, help string, l Labels)          { Default.Counter(name, help, l) }
func Add(name, help string, l Labels, n uint64)    { Default.Add(name, help, l, n) }
func Gauge(name, help string, l Labels, v float64) { Default.Gauge(name, help, l, v) }
func Observe(name, help string, l Labels, v float64) {
	Default.Observe(name, help, l, v)
}
