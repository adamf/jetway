package inventory

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/adamf/jetway/pkg/avail"
	"github.com/adamf/jetway/pkg/pnr"
)

// Two classes, checked against the normal table rather than against the
// code: with the discount at half the full fare the protection is the
// mean (Φ⁻¹(0.5) = 0); with the discount at 84.13% of full, 1−p = 0.1587
// and Φ⁻¹ = −1, one standard deviation less.
func TestEMSRbTwoClassesAgainstTheNormalTable(t *testing.T) {
	full := ClassDemand{Class: "Y", Fare: 100, Mean: 20, StdDev: 5}
	got := EMSRb(100, []ClassDemand{full, {Class: "Q", Fare: 50, Mean: 60, StdDev: 10}})
	if len(got) != 2 || got[0].Class != "Y" || got[0].Authorized != 100 || got[1].Class != "Q" || got[1].Authorized != 80 {
		t.Errorf("half fare protects the mean: %+v", got)
	}
	got = EMSRb(100, []ClassDemand{full, {Class: "Q", Fare: 84.13, Mean: 60, StdDev: 10}})
	if got[1].Authorized != 85 {
		t.Errorf("a discount close to full fare protects one sigma less: %+v", got)
	}
	// A discount at a fifth of full: 1−p = 0.8, Φ⁻¹ = 0.8416, protect 24.
	got = EMSRb(100, []ClassDemand{full, {Class: "Q", Fare: 20, Mean: 60, StdDev: 10}})
	if got[1].Authorized != 76 {
		t.Errorf("a deep discount protects more than the mean: %+v", got)
	}
}

// Three classes pool: the protection against the cheapest is set for Y
// and M together at their demand-weighted fare, and the ladder nests.
func TestEMSRbPoolsTheClassesAbove(t *testing.T) {
	cs := []ClassDemand{
		{Class: "Y", Fare: 400, Mean: 10, StdDev: 3},
		{Class: "M", Fare: 200, Mean: 30, StdDev: 6},
		{Class: "K", Fare: 100, Mean: 90, StdDev: 12},
	}
	got := EMSRb(100, cs)
	// Pool of Y and M: mean 40, sigma sqrt(9+36)=6.708, average fare
	// (4000+6000)/40 = 250; p = 100/250 = 0.4; Φ⁻¹(0.6) = 0.2533;
	// protect 41.7 -> 42; K gets 58.
	if got[2].Class != "K" || got[2].Authorized != 58 {
		t.Errorf("K: %+v", got)
	}
	// M alone against Y: p = 200/400 = 0.5, protect 10; M gets 90.
	if got[1].Class != "M" || got[1].Authorized != 90 {
		t.Errorf("M: %+v", got)
	}
	for i := 1; i < len(got); i++ {
		if got[i].Authorized > got[i-1].Authorized {
			t.Errorf("ladder does not nest: %+v", got)
		}
	}
	// Order in does not matter: the ladder is by fare.
	rev := EMSRb(100, []ClassDemand{cs[2], cs[0], cs[1]})
	for i := range got {
		if rev[i] != got[i] {
			t.Errorf("order-dependent: %+v vs %+v", rev, got)
		}
	}
}

func TestEMSRbEdges(t *testing.T) {
	// A cheaper class priced above the pool's average is protected against
	// by nothing; deterministic demand protects exactly the mean; demand
	// beyond the cabin closes the discount entirely.
	got := EMSRb(100, []ClassDemand{{Class: "Y", Fare: 100, Mean: 20}, {Class: "B", Fare: 100, Mean: 5}})
	if got[1].Authorized != 100 {
		t.Errorf("a class at the same fare as the pool is protected against by nothing: %+v", got)
	}
	got = EMSRb(100, []ClassDemand{{Class: "Y", Fare: 100, Mean: 30}, {Class: "Q", Fare: 40, Mean: 100}})
	if got[1].Authorized != 70 {
		t.Errorf("deterministic: %+v", got)
	}
	got = EMSRb(50, []ClassDemand{{Class: "Y", Fare: 100, Mean: 80, StdDev: 10}, {Class: "Q", Fare: 40, Mean: 100}})
	if got[1].Authorized != 0 {
		t.Errorf("full-fare demand beyond the cabin closes the discount: %+v", got)
	}
	if EMSRb(0, []ClassDemand{{Class: "Y", Fare: 1, Mean: 1}}) != nil || EMSRb(10, nil) != nil {
		t.Error("nothing to control")
	}
}

func TestNormalQuantile(t *testing.T) {
	for p, want := range map[float64]float64{0.5: 0, 0.8413: 1, 0.1587: -1, 0.975: 1.96, 0.9: 1.2816} {
		if got := normalQuantile(p); math.Abs(got-want) > 0.002 {
			t.Errorf("Φ⁻¹(%v) = %v, want %v", p, got, want)
		}
	}
}

// The controller wires a forecast into the inventory's ladder and the
// inventory sells under it: the deep discount closes while full fare is
// open, and re-forecasting moves the ladder.
func TestControllerDrivesTheInventory(t *testing.T) {
	inv := New("WN", b737())
	strong := true
	ctl := &Controller{Capacity: b737(), Forecast: func(carrier, flight, date, board, comp string, seats int) []ClassDemand {
		if comp != "Y" {
			return nil
		}
		yMean := 20.0
		if strong {
			yMean = 120
		}
		return []ClassDemand{{Class: "Y", Fare: 300, Mean: yMean, StdDev: 10}, {Class: "K", Fare: 90, Mean: 200, StdDev: 20}}
	}}
	inv.Levels = ctl.Levels
	// Strong full-fare demand: K is nearly shut (174 - (120 + 10*Φ⁻¹(0.7)) = 174 - 125 = 49).
	sold := 0
	for ; sold < 174; sold++ {
		if got := decide(t, inv, seg("2554", "K", "HN", 1)); got != "KK" {
			break
		}
	}
	if sold != 49 {
		t.Fatalf("K sold %d under a strong forecast", sold)
	}
	if got := decide(t, inv, seg("2554", "Y", "HN", 1)); got != "KK" {
		t.Fatalf("Y still sells: %s", got)
	}
	// The forecaster revises full-fare demand down: K reopens at once.
	strong = false
	if got := decide(t, inv, seg("2554", "K", "HN", 1)); got != "KK" {
		t.Fatalf("K after the forecast eased: %s", got)
	}
}

// A forecaster adds its pickup to what is booked: the inventory says what
// each class has sold, and nothing about classes that have not.
func TestSoldByClassReportsTheBookedToDate(t *testing.T) {
	inv := New("WN", b737())
	for i := 0; i < 3; i++ {
		decide(t, inv, seg("2554", "K", "HN", 1))
	}
	decide(t, inv, seg("2554", "Y", "HN", 2))
	got := inv.SoldByClass("WN", "2554", "26NOV", "BNA", "Y")
	if got["K"] != 3 || got["Y"] != 2 || len(got) != 2 {
		t.Errorf("sold by class: %v", got)
	}
	if n := len(inv.SoldByClass("WN", "9999", "26NOV", "BNA", "Y")); n != 0 {
		t.Errorf("an unsold flight reports %d classes", n)
	}
}

// A forecaster may ask the inventory what it has sold while the inventory
// asks it for the ladder: the ladder is fetched before the lock, so a
// decision and an availability answer both complete. Asked under the lock,
// every decision deadlocked and no booking in the world settled.
func TestForecasterMayReadSoldByClassWithoutDeadlock(t *testing.T) {
	inv := New("WN", b737())
	ctl := &Controller{Capacity: b737(), Forecast: func(carrier, flight, date, board, comp string, seats int) []ClassDemand {
		sold := inv.SoldByClass(carrier, flight, date, board, comp)
		return []ClassDemand{{Class: "Y", Fare: 300, Mean: 20 + float64(sold["Y"]), StdDev: 5}, {Class: "K", Fare: 90, Mean: 50 + float64(sold["K"]), StdDev: 10}}
	}}
	inv.Levels = ctl.Levels
	done := make(chan string, 1)
	go func() {
		got := decide(t, inv, seg("2554", "K", "HN", 1))
		inv.Availability([]avail.Key{avail.NewKey("WN", "2554", time.Date(2025, 11, 26, 0, 0, 0, 0, time.UTC), "BNA", "DCA", "K")}, time.Now())
		done <- got
	}()
	select {
	case got := <-done:
		if got != "KK" {
			t.Errorf("K should sell: %s", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a forecaster reading SoldByClass deadlocked the inventory")
	}
}

// Bid-price control: two legs each open to a K passenger on its own, but
// a connecting passenger paying a through fare below the two legs' bid
// prices is refused, while a local K passenger on either leg still sells,
// and an unpriced connecting record is left to the ladders.
func TestBidPriceControlRefusesAThroughFareBelowTheDisplacement(t *testing.T) {
	cap := func(carrier, flight, date, board string) (map[string]int, bool) {
		if carrier == "WN" && (flight == "10" || flight == "20") {
			return map[string]int{"Y": 100}, true
		}
		return nil, false
	}
	inv := New("WN", cap)
	ctl := &Controller{Capacity: cap, Forecast: func(carrier, flight, date, board, comp string, seats int) []ClassDemand {
		// Full-fare demand alone nearly fills the cabin: K is shut on both
		// legs and M has a handful of seats, so the cheapest open class is
		// M at 20000 and the bid price per leg 20000.
		return []ClassDemand{{Class: "Y", Fare: 40000, Mean: 95, StdDev: 3}, {Class: "M", Fare: 20000, Mean: 20, StdDev: 3}, {Class: "K", Fare: 9000, Mean: 40, StdDev: 5}}
	}}
	inv.Levels = ctl.Levels
	inv.Network = ctl
	leg := func(ref int, flight, board, off string) pnr.Segment {
		return pnr.Segment{Ref: ref, Type: pnr.SegmentAir, Carrier: "WN", FlightNum: flight, WireDate: "26NOV", Board: board, Off: off, Class: "M", Status: "HN", Seats: 1}
	}
	// A connecting M passenger paying a through fare of 30000 for two legs
	// each worth 20000 at the margin: refused on both.
	through := &pnr.PNR{Segments: []pnr.Segment{leg(1, "10", "BNA", "MDW"), leg(2, "20", "MDW", "LGA")},
		Passengers: []pnr.Passenger{{Ref: 1, Surname: "THRU", Given: "A"}},
		Pricing:    &pnr.Pricing{Passengers: []pnr.PassengerPricing{{Ref: 1, Segments: []int64{15000, 15000}}}}}
	out, err := inv.Decide(context.Background(), through, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out[through.Segments[0].Key()] != "UC" || out[through.Segments[1].Key()] != "UC" {
		t.Errorf("a through fare below the bid prices should be refused: %v", out)
	}
	// The same passenger paying 45000 clears the bar.
	through.Pricing.Passengers[0].Segments = []int64{25000, 20000}
	out, _ = inv.Decide(context.Background(), through, nil)
	if out[through.Segments[0].Key()] != "KK" || out[through.Segments[1].Key()] != "KK" {
		t.Errorf("a through fare covering the bid prices should sell: %v", out)
	}
	// A local M passenger on one leg is a leg decision, no bid test.
	local := &pnr.PNR{Segments: []pnr.Segment{leg(1, "10", "BNA", "MDW")}}
	if out, _ := inv.Decide(context.Background(), local, nil); out[local.Segments[0].Key()] != "KK" {
		t.Errorf("a local passenger sells on the ladder: %v", out)
	}
	// An unpriced connecting record cannot be valued: the ladders decide.
	unpriced := &pnr.PNR{Segments: []pnr.Segment{leg(1, "10", "BNA", "MDW"), leg(2, "20", "MDW", "LGA")}}
	if out, _ := inv.Decide(context.Background(), unpriced, nil); out[unpriced.Segments[0].Key()] != "KK" {
		t.Errorf("unpriced record: %v", out)
	}
}

// The network programme on a two-leg example worked by hand: leg A and leg
// B each seat 100; a local on A pays 200 (demand 60), a local on B 150
// (demand 80), a through passenger over both 300 (demand 50). Best is 60
// locals on A, 60 on B and 40 through, worth 33,000; a marginal seat on
// either leg is worth 150, so both bid prices are 150 and the through fare
// of 300 just covers them.
func TestNetworkBidPricesSolveTheTwoLegExample(t *testing.T) {
	bids, x, err := NetworkBidPrices(map[string]float64{"A": 100, "B": 100}, []Itinerary{
		{Legs: []string{"A"}, Fare: 200, Demand: 60},
		{Legs: []string{"B"}, Fare: 150, Demand: 80},
		{Legs: []string{"A", "B"}, Fare: 300, Demand: 50},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []float64{60, 60, 40}
	for i := range want {
		if math.Abs(x[i]-want[i]) > 1e-6 {
			t.Errorf("allocation %v, want %v", x, want)
			break
		}
	}
	if math.Abs(bids["A"]-150) > 1e-6 || math.Abs(bids["B"]-150) > 1e-6 {
		t.Errorf("bid prices %v, want 150 and 150", bids)
	}
	// A leg with room to spare is worth nothing at the margin.
	bids, _, _ = NetworkBidPrices(map[string]float64{"A": 1000, "B": 100}, []Itinerary{
		{Legs: []string{"A"}, Fare: 200, Demand: 60}, {Legs: []string{"B"}, Fare: 150, Demand: 80}, {Legs: []string{"A", "B"}, Fare: 300, Demand: 50},
	})
	if bids["A"] != 0 || math.Abs(bids["B"]-150) > 1e-6 {
		t.Errorf("slack leg prices at zero: %v", bids)
	}
	// The simplex itself, on the textbook toy: max 3x+2y, x+y<=4, x+3y<=6 -> x=4, y=0, 12; duals 3 and 0.
	x2, y2, obj, err := Simplex([]float64{3, 2}, [][]float64{{1, 1}, {1, 3}}, []float64{4, 6})
	if err != nil || math.Abs(obj-12) > 1e-9 || math.Abs(x2[0]-4) > 1e-9 || math.Abs(x2[1]) > 1e-9 || math.Abs(y2[0]-3) > 1e-9 || y2[1] != 0 {
		t.Errorf("simplex: x=%v y=%v obj=%v err=%v", x2, y2, obj, err)
	}
	if _, _, _, err := Simplex([]float64{1}, [][]float64{{-1}}, []float64{1}); err == nil {
		t.Error("an unbounded programme must say so")
	}
}

// The network's own bid prices, when the controller has them, replace the
// ladder's displacement cost: the same through passenger the ladders
// refuse sells when the programme says the legs are cheap, and one the
// ladders would sell is refused when the programme says they are dear.
func TestControllerBidPricesOverrideTheLadders(t *testing.T) {
	cap := func(carrier, flight, date, board string) (map[string]int, bool) {
		return map[string]int{"Y": 100}, true
	}
	inv := New("WN", cap)
	price := 1000.0
	ctl := &Controller{Capacity: cap, Forecast: func(carrier, flight, date, board, comp string, seats int) []ClassDemand {
		return []ClassDemand{{Class: "Y", Fare: 40000, Mean: 95, StdDev: 3}, {Class: "M", Fare: 20000, Mean: 20, StdDev: 3}, {Class: "K", Fare: 9000, Mean: 40, StdDev: 5}}
	}, BidPrice: func(carrier, flight, date, board, comp string) (float64, bool) { return price, true }}
	inv.Levels = ctl.Levels
	inv.Network = ctl
	leg := func(ref int, flight, board, off string) pnr.Segment {
		return pnr.Segment{Ref: ref, Type: pnr.SegmentAir, Carrier: "WN", FlightNum: flight, WireDate: "26NOV", Board: board, Off: off, Class: "M", Status: "HN", Seats: 1}
	}
	through := &pnr.PNR{Segments: []pnr.Segment{leg(1, "10", "BNA", "MDW"), leg(2, "20", "MDW", "LGA")},
		Passengers: []pnr.Passenger{{Ref: 1, Surname: "THRU", Given: "A"}},
		Pricing:    &pnr.Pricing{Passengers: []pnr.PassengerPricing{{Ref: 1, Segments: []int64{15000, 15000}}}}}
	out, _ := inv.Decide(context.Background(), through, nil)
	if out[through.Segments[0].Key()] != "KK" {
		t.Errorf("legs worth 1000 each under the programme: 30000 should sell, got %v", out)
	}
	price = 25000
	through.Pricing.Passengers[0].Segments = []int64{25000, 20000}
	out, _ = inv.Decide(context.Background(), through, nil)
	if out[through.Segments[0].Key()] != "UC" {
		t.Errorf("legs worth 25000 each: 45000 should be refused, got %v", out)
	}
}
