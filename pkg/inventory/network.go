package inventory

import (
	"math"

	"github.com/adamf/jetway/pkg/pnr"
)

// Network revenue management, the bid-price kind. Leg-based control
// answers each leg on its own ladder, so a connecting passenger paying a
// through fare is accepted leg by leg even when the through fare is worth
// less than the two local passengers it displaces. Bid-price control
// values the last seat of each leg -- the fare of the cheapest class still
// open on it, the displacement cost -- and accepts an itinerary only when
// its fare covers the sum of the bid prices of the legs it uses. The bid
// prices here come from the same EMSR-b ladders that control the legs,
// which is the additive leg bid-price heuristic the textbooks give before
// the network linear programme; the numbers are the caller's, and the
// fares in the forecast must be money for the comparison to mean anything.

// LadderFares is the ladder for a cabin now and the fare of each class on
// it, for the bid price.
func (c *Controller) LadderFares(carrier, flightNum, wireDate, board, compartment string) ([]Level, map[string]float64) {
	if c == nil || c.Capacity == nil || c.Forecast == nil {
		return nil, nil
	}
	comps, ok := c.Capacity(carrier, flightNum, wireDate, board)
	if !ok {
		return nil, nil
	}
	seats, ok := comps[compartment]
	if !ok || seats <= 0 {
		return nil, nil
	}
	demand := c.Forecast(carrier, flightNum, wireDate, board, compartment, seats)
	fares := make(map[string]float64, len(demand))
	for _, d := range demand {
		fares[d.Class] = d.Fare
	}
	return EMSRb(seats, demand), fares
}

// bidPrice is the displacement cost of one more seat in a cabin: the fare
// of the cheapest class still open under the ladder given what is sold,
// or the top fare when the ladder is shut and only the cabin's own count
// still admits. Called under the lock; the ladder and fares came from
// before it.
func (inv *Inventory) bidPrice(ladder []Level, fares map[string]float64, key string) (float64, bool) {
	if len(ladder) == 0 || len(fares) == 0 {
		return 0, false
	}
	best := math.Inf(1)
	found := false
	for at, l := range ladder {
		below := 0
		for _, m := range ladder[at:] {
			below += inv.soldClass[key+"/"+m.Class]
		}
		if l.Authorized-below > 0 {
			if f, ok := fares[l.Class]; ok && f < best {
				best, found = f, true
			}
		}
	}
	if !found {
		// Nothing open on the ladder: the last seats are worth full fare.
		return fares[ladder[0].Class], true
	}
	return best, true
}

// itineraryFare is what the record pays for the given segments across its
// passengers, from its pricing, or false when the record is unpriced.
func itineraryFare(p *pnr.PNR, segs []*pnr.Segment) (float64, bool) {
	if p.Pricing == nil || len(p.Pricing.Passengers) == 0 {
		return 0, false
	}
	index := map[int]int{}
	for i := range p.Segments {
		index[p.Segments[i].Ref] = i
	}
	var total float64
	for _, pp := range p.Pricing.Passengers {
		for _, s := range segs {
			i, ok := index[s.Ref]
			if !ok || i >= len(pp.Segments) {
				return 0, false
			}
			total += float64(pp.Segments[i])
		}
	}
	return total, true
}
