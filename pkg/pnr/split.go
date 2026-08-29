package pnr

import (
	"fmt"
	"sort"
)

// Divide separates passengers onto a new record.
//
// It returns the child and mutates p into the parent. Neither is persisted;
// that is the caller's problem, and the ordering of those two writes matters
// enough to be the caller's decision.
//
// # What makes this fiddly
//
// Every reference on a record is positional. Passengers and segments are
// numbered by their place in the slice, and Recompute renumbers them, so SSRs,
// coupons and EMD associations all point at numbers that move the moment
// anybody is removed. Splitting without remapping those leaves a record whose
// special services belong to the wrong passenger, which is worse than refusing
// to split at all.
//
// # Seats
//
// A segment held for three passengers becomes a segment held for two and a
// segment held for one. Getting that arithmetic wrong either oversells the
// flight or quietly gives a seat back, and neither shows up until somebody
// travels.
func (p *PNR) Divide(paxRefs []int, childLocator string) (*PNR, error) {
	moving := map[int]bool{}
	for _, r := range paxRefs {
		moving[r] = true
	}
	if len(moving) == 0 {
		return nil, fmt.Errorf("pnr: a split needs at least one passenger")
	}
	if len(moving) >= len(p.Passengers) {
		// Moving everybody is not a split; it is a record with a new name, and
		// doing it would leave a parent holding seats for nobody.
		return nil, fmt.Errorf("pnr: cannot split all %d passengers off a record", len(p.Passengers))
	}
	for r := range moving {
		if !p.hasPassenger(r) {
			return nil, fmt.Errorf("pnr: record has no passenger %d", r)
		}
	}

	// Old segment reference to itself: segments are copied whole, so the child
	// keeps the same numbering and only the parent can shift. Capture both
	// mappings before anything moves.
	child := &PNR{
		RecordLocator: childLocator,
		Status:        p.Status,
		CreatedAt:     p.CreatedAt,
		UpdatedAt:     p.UpdatedAt,
		Origin:        p.Origin,
		ReceivedFrom:  p.ReceivedFrom,
		SplitFrom:     p.RecordLocator,
	}

	// Passengers, in their original order, with the old reference remembered.
	childPaxOld := make([]int, 0, len(moving))
	parentPaxOld := make([]int, 0, len(p.Passengers))
	for _, pax := range p.Passengers {
		if moving[pax.Ref] {
			child.Passengers = append(child.Passengers, pax)
			childPaxOld = append(childPaxOld, pax.Ref)
			continue
		}
		parentPaxOld = append(parentPaxOld, pax.Ref)
	}
	var keptPax []Passenger
	for _, pax := range p.Passengers {
		if !moving[pax.Ref] {
			keptPax = append(keptPax, pax)
		}
	}

	// Segments are copied whole. Both records hold the same itinerary; what
	// differs is how many seats each holds on it.
	moved, kept := len(child.Passengers), len(keptPax)
	for _, s := range p.Segments {
		c := s
		c.Seats = seatShare(s.Seats, moved, moved+kept)
		child.Segments = append(child.Segments, c)
	}
	for i := range p.Segments {
		p.Segments[i].Seats = seatShare(p.Segments[i].Seats, kept, moved+kept)
	}

	// Contacts and carrier-level information belong to the booking, so both
	// halves keep them.
	child.Contacts = append(child.Contacts, p.Contacts...)
	child.OSIs = append(child.OSIs, p.OSIs...)
	child.Ticketing = append(child.Ticketing, p.Ticketing...)
	child.Remarks = append(child.Remarks, p.Remarks...)

	// Per-passenger things follow their passenger.
	var keptSSRs []SSR
	for _, ssr := range p.SSRs {
		switch {
		case ssr.PaxRef == 0:
			// Not attached to anybody: it applies to the booking, so both.
			child.SSRs = append(child.SSRs, ssr)
			keptSSRs = append(keptSSRs, ssr)
		case moving[ssr.PaxRef]:
			child.SSRs = append(child.SSRs, ssr)
		default:
			keptSSRs = append(keptSSRs, ssr)
		}
	}
	var keptTickets []Ticket
	for _, t := range p.Tickets {
		if moving[t.PaxRef] {
			child.Tickets = append(child.Tickets, t)
			continue
		}
		keptTickets = append(keptTickets, t)
	}

	p.Passengers = keptPax
	p.SSRs = keptSSRs
	p.Tickets = keptTickets
	p.SplitTo = appendUnique(p.SplitTo, childLocator)

	// Renumber, then repair every reference that renumbering just invalidated.
	childMap := renumber(childPaxOld)
	parentMap := renumber(parentPaxOld)
	child.Recompute()
	p.Recompute()
	child.remapPassengerRefs(childMap)
	p.remapPassengerRefs(parentMap)

	return child, nil
}

// seatShare divides a segment's seats between the two halves.
//
// A segment whose seat count does not match the passenger count is left
// proportional rather than corrected: the count came from a carrier and this is
// not the place to argue with it.
func seatShare(seats, share, total int) int {
	if total <= 0 || seats <= 0 {
		return seats
	}
	if seats == total {
		return share
	}
	n := seats * share / total
	if n < 1 {
		n = 1
	}
	return n
}

// renumber maps old passenger references to their new position.
func renumber(old []int) map[int]int {
	sort.Ints(old)
	m := make(map[int]int, len(old))
	for i, r := range old {
		m[r] = i + 1
	}
	return m
}

// remapPassengerRefs points every per-passenger reference at its new number.
func (p *PNR) remapPassengerRefs(m map[int]int) {
	for i := range p.SSRs {
		if n, ok := m[p.SSRs[i].PaxRef]; ok {
			p.SSRs[i].PaxRef = n
		}
	}
	for i := range p.Tickets {
		if n, ok := m[p.Tickets[i].PaxRef]; ok {
			p.Tickets[i].PaxRef = n
		}
	}
}

func (p *PNR) hasPassenger(ref int) bool {
	for _, x := range p.Passengers {
		if x.Ref == ref {
			return true
		}
	}
	return false
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}
