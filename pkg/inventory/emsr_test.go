package inventory

import (
	"math"
	"testing"
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
