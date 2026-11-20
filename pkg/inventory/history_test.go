package inventory

import (
	"math"
	"math/rand"
	"testing"
)

// Table 1 of Fiig, Isler, Hopperstad and Belobaba, "Optimization of mixed
// fare structures: Theory and applications", Journal of Revenue and
// Pricing Management 9 (2010), page 156: six fares, the demand each draws
// when demand is independent, and the total revenue and marginal revenue
// of each strategy when the fares are fully differentiated and when they
// are not differentiated at all.
var (
	fiigFares  = []float64{1200, 1000, 800, 600, 400, 200}
	fiigDemand = []float64{31.2, 10.9, 14.8, 19.9, 26.9, 36.3}
	fiigQ      = []float64{31.2, 42.1, 56.9, 76.8, 103.7, 140.0}
	// Total revenue, differentiated and undifferentiated.
	fiigTRDiff   = []float64{37486, 48415, 60217, 72165, 82918, 90175}
	fiigTRUndiff = []float64{37486, 42167, 45536, 46100, 41486, 28000}
	// The paper's marginal revenues for the undifferentiated fares: the
	// fare less a modifier of 572.
	fiigMR = []float64{1200, 428, 228, 28, -172, -372}
)

func TestMarginalRevenueAgainstFiigTable1(t *testing.T) {
	// Differentiated fares: the transformation is the identity.
	demand, fare := MarginalRevenue(fiigQ, fiigTRDiff)
	for k := range fiigQ {
		if math.Abs(demand[k]-fiigDemand[k]) > 0.05 {
			t.Errorf("differentiated: strategy %d adds %.1f, the table has %.1f", k+1, demand[k], fiigDemand[k])
		}
		// The table's revenues come from demands it prints to one decimal.
		if math.Abs(fare[k]-fiigFares[k]) > 4 {
			t.Errorf("differentiated: strategy %d is worth %.0f, the table has %.0f", k+1, fare[k], fiigFares[k])
		}
	}
	// Undifferentiated: every traveller buys the lowest fare on sale.
	demand, fare = MarginalRevenue(fiigQ, fiigTRUndiff)
	for k := range fiigQ {
		if math.Abs(demand[k]-fiigDemand[k]) > 0.05 {
			t.Errorf("undifferentiated: strategy %d adds %.1f, the table has %.1f", k+1, demand[k], fiigDemand[k])
		}
		if math.Abs(fare[k]-fiigMR[k]) > 4 {
			t.Errorf("undifferentiated: strategy %d is worth %.0f, the table has %.0f", k+1, fare[k], fiigMR[k])
		}
	}
}

// Table 2 of the same paper, page 164: the booking limits of standard
// EMSRb on the six fares, and of EMSRb on the adjusted fares, for a
// cabin of 100.
func TestEMSRbAgainstFiigTable2(t *testing.T) {
	sd := []float64{11.2, 6.6, 7.7, 8.9, 10.4, 12.0}
	limits := func(fares []float64) []int {
		var cs []ClassDemand
		for k := range fares {
			cs = append(cs, ClassDemand{Class: string(rune('1' + k)), Fare: fares[k], Mean: fiigDemand[k], StdDev: sd[k]})
		}
		var out []int
		for _, l := range EMSRb(100, cs) {
			out = append(out, l.Authorized)
		}
		return out
	}
	for name, c := range map[string]struct {
		fares []float64
		want  []int
	}{
		"EMSRb":    {fiigFares, []int{100, 80, 65, 46, 20, 0}},
		"EMSRb-MR": {fiigMR, []int{100, 65, 48, 16, 0, 0}},
	} {
		got := limits(c.fares)
		for k := range c.want {
			if got[k] != c.want[k] {
				t.Errorf("%s: limits %v, the table has %v", name, got, c.want)
				break
			}
		}
	}
}

func TestMarginalRevenueOffTheHull(t *testing.T) {
	nan := math.NaN()
	for name, c := range map[string]struct {
		q, tr, demand, fare []float64
	}{
		// The second strategy sells 10 more seats and earns less: under
		// the hull, with no efficient strategy after it.
		"dilution": {[]float64{10, 20}, []float64{100, 90}, []float64{10, 10}, []float64{10, -1}},
		// The second strategy draws what the first draws for less.
		"dominated": {[]float64{10, 10}, []float64{100, 80}, []float64{10, 0}, []float64{10, math.Inf(-1)}},
		// The second strategy is under the line from the first to the
		// third, so it opens with the third.
		"skipped": {[]float64{10, 12, 30}, []float64{100, 101, 190}, []float64{10, 0, 20}, []float64{10, 4.5, 4.5}},
		// A strategy that draws nothing has no adjusted fare.
		"nothing": {[]float64{0, 10}, []float64{0, 100}, []float64{0, 10}, []float64{nan, 10}},
		// A strategy that draws exactly what the one before it draws.
		"the same": {[]float64{10, 10}, []float64{100, 100}, []float64{10, 0}, []float64{10, 10}},
	} {
		demand, fare := MarginalRevenue(c.q, c.tr)
		for k := range c.q {
			same := fare[k] == c.fare[k] || (math.IsNaN(fare[k]) && math.IsNaN(c.fare[k])) || math.Abs(fare[k]-c.fare[k]) < 1e-9
			if math.Abs(demand[k]-c.demand[k]) > 1e-9 || !same {
				t.Errorf("%s: strategy %d adds %.2f at %.2f, want %.2f at %.2f", name, k, demand[k], fare[k], c.demand[k], c.fare[k])
			}
		}
	}
}

// A hand-worked record. One time frame, classes Y at 300 and Q at 100,
// one departure. For half the frame Q was on sale and 6 seats sold in
// it; for the other half only Y was, and 2 seats sold in Y.
//
// Read as sales: Y has 2 and Q has 6.
//
// Unconstrained: Y was on sale all the frame and sold 2, so its demand is
// 2. Q was on sale half the frame and sold 6, so its demand is 12.
//
// Fare adjusted: with only Y on sale the frame draws 2 in half the time,
// so 4 at a revenue of 1200. With Q on sale it draws 6 in half the time,
// so 12 at a revenue of 1200. Opening Q adds 8 seats and no revenue. Q is
// worth nothing and stays closed.
func TestHistoryReadsItsRecordThreeWays(t *testing.T) {
	fare := func(c string) float64 { return map[string]float64{"Y": 300, "Q": 100}[c] }
	build := func(m Method) *History {
		h := NewHistory([]string{"Y", "Q"}, 1)
		h.Method = m
		l := h.Begin()
		l.Expose(0, "Q", 30)
		l.Sale(0, "Q", "Q", 6, 600)
		l.Expose(0, "Y", 30)
		l.Sale(0, "Y", "Y", 2, 600)
		h.Record(l)
		return h
	}
	mean := func(h *History, class string) float64 {
		for _, d := range h.ToCome(0, 1, fare) {
			if d.Class == class {
				return d.Mean
			}
		}
		return -1
	}
	if h := build(SalesAsDemand); mean(h, "Y") != 2 || mean(h, "Q") != 6 {
		t.Errorf("sales as demand: Y %.1f and Q %.1f, want 2 and 6", mean(h, "Y"), mean(h, "Q"))
	}
	if h := build(Unconstrained); mean(h, "Y") != 2 || mean(h, "Q") != 12 {
		t.Errorf("unconstrained: Y %.1f and Q %.1f, want 2 and 12", mean(h, "Y"), mean(h, "Q"))
	}
	h := build(FareAdjusted)
	if mean(h, "Y") != 4 || mean(h, "Q") != 0 {
		t.Errorf("fare adjusted: Y %.1f and Q %.1f, want 4 and 0", mean(h, "Y"), mean(h, "Q"))
	}
	c := h.Controls(0, 1, fare)
	if c[0].Closed || c[0].Protect != 0 || c[0].Value != 300 {
		t.Errorf("Y is %+v, want open at 300 with nothing held back", c[0])
	}
	if !c[1].Closed {
		t.Errorf("Q is %+v, want closed: it adds seats and no revenue", c[1])
	}
	// Half the frame gone, half the demand to come.
	if d := h.ToCome(0, 0.5, fare); d[0].Mean != 2 {
		t.Errorf("half a frame to come has %.1f of Y, want 2", d[0].Mean)
	}

	// The record round trip.
	b, err := h.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	var back History
	if err := back.UnmarshalBinary(b); err != nil {
		t.Fatal(err)
	}
	if back.Departures() != 1 || mean(&back, "Y") != 4 {
		t.Errorf("after a round trip: %d departures and Y %.1f", back.Departures(), mean(&back, "Y"))
	}
	if err := back.UnmarshalBinary(b[:len(b)-1]); err == nil {
		t.Error("a cut record was read")
	}
}

// A strategy that is no longer used is forgotten, and its class is then
// tried again at its fare. Without this a class closed once on a thin
// record would stay closed.
func TestHistoryForgetsWhatItNoLongerSees(t *testing.T) {
	fare := func(c string) float64 { return map[string]float64{"Y": 300, "Q": 100}[c] }
	h := NewHistory([]string{"Y", "Q"}, 1)
	l := h.Begin()
	l.Expose(0, "Q", 30)
	l.Sale(0, "Q", "Q", 6, 600)
	l.Expose(0, "Y", 30)
	l.Sale(0, "Y", "Y", 2, 600)
	h.Record(l)
	if !h.Controls(0, 1, fare)[1].Closed {
		t.Fatal("Q should start closed")
	}
	// Departures on which only Y was on sale.
	for i := 0; i < 40; i++ {
		l := h.Begin()
		l.Expose(0, "Y", 60)
		l.Sale(0, "Y", "Y", 4, 1200)
		h.Record(l)
	}
	c := h.Controls(0, 1, fare)[1]
	if c.Closed || c.Value != 100 {
		t.Errorf("after 40 departures without it Q is %+v, want open at its fare", c)
	}
	// It is held back for the Y demand of 4, which is worth 3 times as
	// much. Seats are held until the chance of selling one more to Y is 1
	// in 3: 4 + 2 x 0.4307, where 2 is the spread of a Poisson demand of
	// 4 and 0.4307 the normal deviate that leaves a third above it.
	if math.Abs(c.Protect-4.861) > 0.005 {
		t.Errorf("Q is held back %.3f seats for a Y demand of 4, want 4.861", c.Protect)
	}
}

// Example 5 of Kleywegt, Cooper and Homem-de-Mello, "Models of the
// Spiral-Down Effect in Revenue Management" (TRISTAN V, 2004; the paper
// is Operations Research 54(5), 2006). One flight of 100 seats, class 1
// at 1 and class 2 at 0.7, over a horizon of 100. Customers arrive at a
// rate of 1.5. In the first third they buy class 2 only. In the second
// third they buy either class, and class 2 if it is on sale. In the last
// third they buy class 1 only.
//
// The revenue manager believes the first half buys class 2 and the second
// half class 1. Littlewood's rule on that belief protects 70 seats, the
// 0.3 quantile of a Poisson demand of 75. The best protection level is
// 95. If the manager then reads class 1's sales as class 1's demand, the
// protection levels spiral down to nothing.
type cooper struct {
	rng *rand.Rand
}

// fly sells one departure under a protection level and returns the
// revenue and the seats sold in class 1. A nil log records nothing.
func (c cooper) fly(protect int, l *Log) (revenue float64, high int) {
	const seats, horizon = 100, 100.0
	limit := seats - protect // what class 2 may take
	low := 0
	state := func() string {
		switch {
		case high+low >= seats:
			return ""
		case low < limit:
			return "2"
		}
		return "1"
	}
	frame := func(at float64) int { return min(int(at*3/horizon), 2) }
	expose := func(from, to float64) {
		for from < to {
			f := frame(from)
			end := math.Min(to, float64(f+1)*horizon/3)
			if l != nil {
				l.Expose(f, state(), end-from)
			}
			from = end
		}
	}
	at := 0.0
	for {
		next := at + c.rng.ExpFloat64()/1.5
		if next >= horizon {
			expose(at, horizon)
			return
		}
		expose(at, next)
		at = next
		s, f := state(), frame(at)
		buysLow, buysHigh := at < 2*horizon/3, at >= horizon/3
		switch {
		case s == "2" && buysLow:
			low++
			revenue += 0.7
			if l != nil {
				l.Sale(f, s, "2", 1, 0.7)
			}
		case s != "" && buysHigh:
			high++
			revenue++
			if l != nil {
				l.Sale(f, s, "1", 1, 1)
			}
		}
	}
}

func TestSpiralDownAgainstCooperExample5(t *testing.T) {
	fare := func(c string) float64 { return map[string]float64{"1": 1, "2": 0.7}[c] }

	// The manager's own model: class 1 demand is a Poisson of 75.
	levels := EMSRb(100, []ClassDemand{{Class: "1", Fare: 1, Mean: 75, StdDev: math.Sqrt(75)}, {Class: "2", Fare: 0.7, Mean: 75, StdDev: math.Sqrt(75)}})
	if got := 100 - levels[1].Authorized; got != 70 {
		t.Errorf("the manager's model protects %d seats, the paper has 70", got)
	}

	// The revenue each protection level earns, and the best level.
	c := cooper{rand.New(rand.NewSource(5))}
	earn := func(protect int) float64 {
		var sum float64
		const runs = 4000
		for i := 0; i < runs; i++ {
			r, _ := c.fly(protect, nil)
			sum += r
		}
		return sum / runs
	}
	best, most := 0, 0.0
	for p := 0; p <= 100; p += 5 {
		if r := earn(p); r > most {
			best, most = p, r
		}
	}
	if best != 95 {
		t.Errorf("the best protection level is %d, the paper has 95", best)
	}
	// The paper's figure 1: about 71 with nothing protected and a little
	// over 96 at the best.
	if r := earn(0); r < 70 || r > 72 {
		t.Errorf("with nothing protected the flight earns %.1f, the figure has about 71", r)
	}
	if most < 96 || most > 97 {
		t.Errorf("at the best level the flight earns %.1f, the figure has about 96.5", most)
	}

	// 600 departures from the manager's 70, the forecast read three ways.
	run := func(m Method) (protect int, earned float64) {
		c := cooper{rand.New(rand.NewSource(7))}
		h := NewHistory([]string{"1", "2"}, 3)
		h.Method = m
		protect = 70
		const flights, tail = 600, 100
		for i := 0; i < flights; i++ {
			l := h.Begin()
			r, _ := c.fly(protect, l)
			h.Record(l)
			if i >= flights-tail {
				earned += r / tail
			}
			ctl := h.Controls(0, 1, fare)[1]
			protect = min(100, int(math.Round(ctl.Protect)))
			if ctl.Closed {
				protect = 100
			}
		}
		return
	}
	sold, soldEarns := run(SalesAsDemand)
	if sold > 10 {
		t.Errorf("reading sales as demand, the protection level ends at %d; it should have spiralled down", sold)
	}
	if soldEarns > 75 {
		t.Errorf("reading sales as demand, the flight ends earning %.1f; it should be near the 71 of no protection", soldEarns)
	}
	adjusted, adjustedEarns := run(FareAdjusted)
	if adjusted < 92 || adjusted > 98 {
		t.Errorf("with fare adjustment the protection level ends at %d, the best is 95", adjusted)
	}
	if adjustedEarns < most-1 {
		t.Errorf("with fare adjustment the flight ends earning %.1f, the best level earns %.1f", adjustedEarns, most)
	}
	t.Logf("from 70: sales as demand ends at %d earning %.1f; fare adjusted ends at %d earning %.1f; the best is %d earning %.1f",
		sold, soldEarns, adjusted, adjustedEarns, best, most)
	unconstrained, unconstrainedEarns := run(Unconstrained)
	t.Logf("unconstrained class by class ends at %d earning %.1f", unconstrained, unconstrainedEarns)
}

// Hand-worked. A cabin of 100 has sold 10 in Y and 20 in M. Y is held
// back for nothing, so it may take the cabin. M is held back 30 seats and
// 10 are sold above it: 100 - 10 - 30 = 60. Q is held back 55.4, which
// rounds to 55, and 30 are sold above it: 100 - 30 - 55 = 15. N is closed.
// A class cannot be authorised more than the class above it.
func TestAuthorizationsFromControls(t *testing.T) {
	got := Authorizations(100, map[string]int{"Y": 10, "M": 20}, []Control{
		{Class: "Y"}, {Class: "M", Protect: 30}, {Class: "Q", Protect: 55.4}, {Class: "N", Closed: true},
	})
	want := []Level{{"Y", 100}, {"M", 60}, {"Q", 15}, {"N", 0}}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("authorisations %v, want %v", got, want)
		}
	}
	got = Authorizations(100, nil, []Control{{Class: "Y", Protect: 20}, {Class: "M", Protect: 5}})
	if got[0].Authorized != 80 || got[1].Authorized != 80 {
		t.Errorf("authorisations %v: M is held back less than Y and may still not take more", got)
	}
}
