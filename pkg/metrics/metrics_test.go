package metrics

import (
	"math"
	"strings"
	"sync"
	"testing"
)

func TestCounterAndLabels(t *testing.T) {
	r := NewRegistry()
	r.Counter("jetway_messages_total", "messages seen", Labels{"peer": "BA", "direction": "in"})
	r.Counter("jetway_messages_total", "messages seen", Labels{"peer": "BA", "direction": "in"})
	r.Counter("jetway_messages_total", "messages seen", Labels{"peer": "AA", "direction": "in"})

	out := r.String()
	if !strings.Contains(out, `jetway_messages_total{direction="in",peer="BA"} 2`) {
		t.Errorf("BA counter wrong:\n%s", out)
	}
	if !strings.Contains(out, `jetway_messages_total{direction="in",peer="AA"} 1`) {
		t.Errorf("AA counter wrong:\n%s", out)
	}
	if !strings.Contains(out, "# TYPE jetway_messages_total counter") {
		t.Errorf("missing TYPE line:\n%s", out)
	}
	// Labels must render in a stable order or every scrape looks like a change.
	if strings.Count(out, "jetway_messages_total{") != 2 {
		t.Errorf("expected exactly two series:\n%s", out)
	}
}

func TestGaugeRoundTripsFloats(t *testing.T) {
	r := NewRegistry()
	for _, v := range []float64{0, 1, -3.5, 1e9, 0.000001, math.MaxInt32} {
		r.Gauge("g", "help", nil, v)
		if got := fromBits(r.families["g"].get(nil).value.Load()); got != v {
			t.Errorf("gauge %v round-tripped to %v", v, got)
		}
	}
}

func TestHistogramBucketsAreCumulative(t *testing.T) {
	r := NewRegistry()
	for _, v := range []float64{0.0005, 0.003, 0.02, 7} {
		r.Observe("lat", "latency", Labels{"stage": "decode"}, v)
	}
	out := r.String()
	// 0.0005 falls in every bucket from .001 up.
	if !strings.Contains(out, `lat_bucket{le="0.001",stage="decode"} 1`) {
		t.Errorf("first bucket wrong:\n%s", out)
	}
	if !strings.Contains(out, `lat_bucket{le="0.005",stage="decode"} 2`) {
		t.Errorf("cumulative bucket wrong:\n%s", out)
	}
	if !strings.Contains(out, `lat_bucket{le="+Inf",stage="decode"} 4`) {
		t.Errorf("+Inf bucket must equal the total:\n%s", out)
	}
	if !strings.Contains(out, `lat_count{stage="decode"} 4`) {
		t.Errorf("count wrong:\n%s", out)
	}
	if !strings.Contains(out, "# TYPE lat histogram") {
		t.Errorf("missing TYPE line:\n%s", out)
	}
}

func TestLabelValuesAreEscaped(t *testing.T) {
	r := NewRegistry()
	r.Counter("c", "help", Labels{"err": `bad "quote" and \slash`})
	out := r.String()
	if !strings.Contains(out, `err="bad \"quote\" and \\slash"`) {
		t.Errorf("label not escaped:\n%s", out)
	}
}

func TestOnCollect(t *testing.T) {
	r := NewRegistry()
	r.OnCollect(func() { r.Gauge("depth", "queue depth", nil, 42) })
	if !strings.Contains(r.String(), "depth 42") {
		t.Errorf("collector did not run:\n%s", r.String())
	}
}

func TestConcurrentUse(t *testing.T) {
	r := NewRegistry()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				r.Counter("c", "help", Labels{"peer": "BA"})
				r.Observe("h", "help", nil, float64(j)/1000)
				r.Gauge("g", "help", nil, float64(i))
				_ = r.String()
			}
		}(i)
	}
	wg.Wait()
	if !strings.Contains(r.String(), `c{peer="BA"} 5000`) {
		t.Errorf("lost increments under concurrency:\n%s", r.String())
	}
}
