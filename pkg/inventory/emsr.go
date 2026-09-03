package inventory

import (
	"math"
	"sort"
)

// ClassDemand is one booking class's forecast for a cabin: the fare it
// sells at (any unit; only the ratios matter) and the demand still to come
// as a mean and a standard deviation.
type ClassDemand struct {
	Class  string
	Fare   float64
	Mean   float64
	StdDev float64
}

// EMSRb sets the nested authorisations for a cabin of the given seats by
// Belobaba's EMSR-b heuristic, the method revenue management departments
// have run since the late 1980s and the textbooks (Talluri and van Ryzin,
// chapter 2) give in full. For each class the classes above it are pooled
// into one virtual class with their summed demand and demand-weighted
// fare, and seats are protected for the pool up to the point where the
// probability of selling one more to it equals the ratio of the next fare
// to the pool's: the expected marginal seat revenue of the protected seat
// no longer beats the discount fare in hand. The authorisation of each
// class is the cabin less the protection above it, so the ladder nests:
// full fare sells to the last seat, the deepest discount only what the
// higher fares are not forecast to want.
//
// The method is specified; the numbers are the caller's. Classes come in
// any order and are sorted by fare, highest first; a class priced above
// the pool's average fare is protected against by nothing, because a seat
// sold to it is not the one being given away.
func EMSRb(seats int, classes []ClassDemand) []Level {
	if seats <= 0 || len(classes) == 0 {
		return nil
	}
	cs := make([]ClassDemand, len(classes))
	copy(cs, classes)
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].Fare > cs[j].Fare })

	out := make([]Level, 0, len(cs))
	out = append(out, Level{Class: cs[0].Class, Authorized: seats})
	var mean, variance, revenue float64
	for j := 0; j+1 < len(cs); j++ {
		mean += math.Max(cs[j].Mean, 0)
		variance += cs[j].StdDev * cs[j].StdDev
		revenue += cs[j].Fare * math.Max(cs[j].Mean, 0)
		protect := 0.0
		if mean > 0 {
			avgFare := revenue / mean
			next := cs[j+1].Fare
			if next < avgFare && avgFare > 0 {
				// P(pool demand > protect) = next / avgFare.
				p := next / avgFare
				protect = mean + math.Sqrt(variance)*normalQuantile(1-p)
			}
		}
		protect = math.Max(0, math.Min(float64(seats), protect))
		auth := seats - int(math.Round(protect))
		if prev := out[len(out)-1].Authorized; auth > prev {
			auth = prev
		}
		if auth < 0 {
			auth = 0
		}
		out = append(out, Level{Class: cs[j+1].Class, Authorized: auth})
	}
	return out
}

// normalQuantile is the standard normal's inverse distribution function,
// through the error function the standard library carries: Φ⁻¹(p) =
// √2·erf⁻¹(2p−1).
func normalQuantile(p float64) float64 {
	switch {
	case p <= 0:
		return math.Inf(-1)
	case p >= 1:
		return math.Inf(1)
	}
	return math.Sqrt2 * math.Erfinv(2*p-1)
}

// Forecast is the demand still to come for a cabin, by class, given the
// seats it has. It is what a revenue management system's forecaster
// produces from history; here it is the caller's.
type Forecast func(carrier, flightNum, wireDate, board, compartment string, seats int) []ClassDemand

// Controller is the revenue management department reduced to its
// controlling half: it turns a forecast into the nested authorisations
// the inventory sells under, so Inventory.Levels can be a Controller's
// Levels. Every question is answered from the current forecast, so a
// forecaster that reads the clock re-optimises the flight each time the
// inventory asks.
type Controller struct {
	Capacity Capacity
	Forecast Forecast
	// BidPrice, when set, values a cabin's marginal seat for network
	// control in place of the ladder's displacement cost: the caller's
	// network programme (NetworkBidPrices) supplies the leg duals, and
	// the additive ladder heuristic is the fallback whenever it answers
	// false. It is consulted before the inventory lock is taken.
	BidPrice func(carrier, flightNum, wireDate, board, compartment string) (float64, bool)
}

// Levels is the ladder for a cabin now, or nil when the flight or the
// cabin is unknown -- which leaves the inventory selling to the seat.
func (c *Controller) Levels(carrier, flightNum, wireDate, board, compartment string) []Level {
	if c == nil || c.Capacity == nil || c.Forecast == nil {
		return nil
	}
	comps, ok := c.Capacity(carrier, flightNum, wireDate, board)
	if !ok {
		return nil
	}
	seats, ok := comps[compartment]
	if !ok || seats <= 0 {
		return nil
	}
	return EMSRb(seats, c.Forecast(carrier, flightNum, wireDate, board, compartment, seats))
}
