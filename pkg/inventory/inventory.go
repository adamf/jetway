// Package inventory is a carrier's seat inventory: what each flight offers
// per cabin, what is sold, what is waitlisted, and the answer to a request
// for more.
//
// It answers the two questions a carrier's reservations system is asked
// from outside. A sell arrives as a request status on a segment and gets a
// decision -- confirmed, waitlisted, or unable -- under the same lock that
// commits the seats, so the decision and the count can never disagree. An
// availability broadcast asks what the carrier would grant without being
// asked, per class and date, and gets the seats left.
//
// Capacity is per compartment, not per booking class: a 737 has 174 seats in
// one cabin whatever letter the fare was sold under, and every class in the
// cabin draws on the same pool. Booking classes map to compartments the
// way the departure control system maps them (F, A, P, R to first; J, C, D,
// I, Z to business; the rest to economy), falling back to the cabins the
// aircraft actually has. Nested authorisation levels per class -- the yield
// management a real inventory layers on top -- are not modelled: this is
// the physical inventory, and a class is open while its cabin has a seat.
//
// The count is rebuilt from the book of record, not remembered: Seed adds
// what a stored record holds, and a carrier restarting rebuilds the day
// from its store before it answers anything. What was sold before the
// system came up is not free just because the system forgot it.
package inventory

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/adamf/jetway/pkg/avail"
	"github.com/adamf/jetway/pkg/gateway"
	"github.com/adamf/jetway/pkg/pnr"
	"github.com/adamf/jetway/pkg/rescode"
)

// Capacity says how many seats a flight leg offers per compartment on a
// date. The boarding point names the leg, because a carrier flies one
// number over several legs in a day and each is its own aircraft; "" is
// accepted when the caller does not know it and means the first leg the
// carrier can name. ok false means the carrier does not fly it.
type Capacity func(carrier, flightNum, wireDate, board string) (compartments map[string]int, ok bool)

// Inventory is one carrier's seats. It implements gateway.Responder and
// gateway.Releaser.
type Inventory struct {
	// Carrier is the designator whose segments this inventory answers;
	// other carriers' segments on a shared record are not its to decide.
	Carrier string
	// Capacity is the schedule's side of the question.
	Capacity Capacity
	// WaitlistShare is how deep a waitlist a cabin takes once full, as a
	// share of its seats, at least MinWaitlist. Default a tenth and two.
	WaitlistShare float64
	MinWaitlist   int
	// ClosedClasses never sell, whatever the cabin holds.
	ClosedClasses map[string]bool

	mu         sync.Mutex
	sold       map[string]int // carrier/flight/date/compartment
	waitlisted map[string]int
	overrides  map[string]string // carrier/flight/date/class -> forced status
}

// New returns an empty inventory for a carrier.
func New(carrier string, capacity Capacity) *Inventory {
	return &Inventory{Carrier: carrier, Capacity: capacity, WaitlistShare: 0.1, MinWaitlist: 2,
		sold: map[string]int{}, waitlisted: map[string]int{}, overrides: map[string]string{}}
}

// CompartmentFor maps a booking class onto the cabin it sells from, given
// the cabins the aircraft has.
func CompartmentFor(class string, compartments map[string]int) string {
	var want []string
	switch class {
	case "F", "A", "P", "R":
		want = []string{"F", "C", "Y"}
	case "J", "C", "D", "I", "Z":
		want = []string{"C", "Y", "F"}
	default:
		want = []string{"Y", "C", "F"}
	}
	for _, w := range want {
		if _, ok := compartments[w]; ok {
			return w
		}
	}
	return "Y"
}

func poolKey(carrier, flight, date, board, comp string) string {
	return strings.Join([]string{carrier, strings.TrimLeft(flight, "0"), strings.ToUpper(date), board, comp}, "/")
}

func classKey(carrier, flight, date, board, class string) string {
	return strings.Join([]string{carrier, strings.TrimLeft(flight, "0"), strings.ToUpper(date), board, class}, "/")
}

func (inv *Inventory) waitlistFor(seats int) int {
	w := int(float64(seats) * inv.WaitlistShare)
	if w < inv.MinWaitlist {
		w = inv.MinWaitlist
	}
	return w
}

// pool resolves a segment to its cabin and the cabin's seats.
func (inv *Inventory) pool(carrier, flight, date, board, class string) (key string, seats int, ok bool) {
	if inv.Capacity == nil {
		return "", 0, false
	}
	comps, ok := inv.Capacity(carrier, flight, date, board)
	if !ok || len(comps) == 0 {
		return "", 0, false
	}
	comp := CompartmentFor(class, comps)
	return poolKey(carrier, flight, date, board, comp), comps[comp], true
}

// Decide implements gateway.Responder: a status per segment awaiting one.
func (inv *Inventory) Decide(ctx context.Context, p *pnr.PNR, peer *gateway.Peer) (map[string]string, error) {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	out := map[string]string{}
	for _, s := range p.Segments {
		if s.Type != pnr.SegmentAir || (inv.Carrier != "" && s.Carrier != inv.Carrier) {
			continue
		}
		if !awaitingDecision(s.Status) {
			continue
		}
		if forced, ok := inv.overrides[classKey(s.Carrier, s.FlightNum, s.WireDate, s.Board, s.Class)]; ok {
			out[s.Key()] = forced
			inv.commit(s, forced)
			continue
		}
		if inv.ClosedClasses[s.Class] {
			out[s.Key()] = "UC"
			continue
		}
		key, seats, ok := inv.pool(s.Carrier, s.FlightNum, s.WireDate, s.Board, s.Class)
		if !ok {
			// A flight the carrier does not operate on that date: unable,
			// and the requester's schedule is wrong, which UN says.
			out[s.Key()] = "UN"
			continue
		}
		switch {
		case inv.sold[key]+s.Seats <= seats:
			inv.sold[key] += s.Seats
			out[s.Key()] = "KK"
		case inv.waitlisted[key]+s.Seats <= inv.waitlistFor(seats):
			inv.waitlisted[key] += s.Seats
			out[s.Key()] = "US"
		default:
			out[s.Key()] = "UC"
		}
	}
	return out, nil
}

func (inv *Inventory) commit(s pnr.Segment, status string) {
	key, _, ok := inv.pool(s.Carrier, s.FlightNum, s.WireDate, s.Board, s.Class)
	if !ok {
		key = poolKey(s.Carrier, s.FlightNum, s.WireDate, s.Board, CompartmentFor(s.Class, nil))
	}
	switch status {
	case "KK", "KL", "TK", "HK":
		inv.sold[key] += s.Seats
	case "US", "UU", "TL", "HL":
		inv.waitlisted[key] += s.Seats
	}
}

// Seed counts a segment a stored record already holds: a confirmed one into
// the cabin's sold seats, a waitlisted one into its waitlist. It is how the
// inventory is rebuilt from the book of record.
func (inv *Inventory) Seed(s pnr.Segment) {
	if s.Type != pnr.SegmentAir || (inv.Carrier != "" && s.Carrier != inv.Carrier) {
		return
	}
	inv.mu.Lock()
	defer inv.mu.Unlock()
	inv.commit(s, s.Status)
}

// Release implements gateway.Releaser: a confirmed segment cancelled gives
// its seats back; a waitlisted one leaves the waitlist.
func (inv *Inventory) Release(ctx context.Context, s pnr.Segment, was string) {
	if s.Type != pnr.SegmentAir || (inv.Carrier != "" && s.Carrier != inv.Carrier) {
		return
	}
	inv.mu.Lock()
	defer inv.mu.Unlock()
	key, _, ok := inv.pool(s.Carrier, s.FlightNum, s.WireDate, s.Board, s.Class)
	if !ok {
		key = poolKey(s.Carrier, s.FlightNum, s.WireDate, s.Board, CompartmentFor(s.Class, nil))
	}
	switch was {
	case "KK", "KL", "TK", "HK":
		inv.sold[key] = max(0, inv.sold[key]-s.Seats)
	case "US", "UU", "TL", "HL":
		inv.waitlisted[key] = max(0, inv.waitlisted[key]-s.Seats)
	}
}

// Reset forgets every count, before a rebuild from the store.
func (inv *Inventory) Reset() {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	inv.sold, inv.waitlisted = map[string]int{}, map[string]int{}
}

// SetOverride forces the outcome for one class on one flight and date, the
// way a demo shows a refusal or a waitlist on demand.
func (inv *Inventory) SetOverride(carrier, flight, wireDate, board, class, status string) {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	inv.overrides[classKey(carrier, flight, wireDate, board, class)] = status
}

// Availability reports what the carrier would grant per key: the seats left
// in the class's cabin, and open, waitlist or closed accordingly.
func (inv *Inventory) Availability(keys []avail.Key, asOf time.Time) []avail.Entry {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	out := make([]avail.Entry, 0, len(keys))
	for _, k := range keys {
		if inv.Carrier != "" && k.Carrier != inv.Carrier {
			continue
		}
		e := avail.Entry{Key: k, Source: avail.SourceAVS, AsOf: asOf}
		date, err := time.Parse("2006-01-02", k.Date)
		if err != nil {
			continue
		}
		wire := pnr.FormatDate(date)
		if inv.ClosedClasses[k.Class] {
			e.Status = avail.Closed
			out = append(out, e)
			continue
		}
		if forced, ok := inv.overrides[classKey(k.Carrier, k.FlightNum, wire, k.Board, k.Class)]; ok {
			switch forced {
			case "UC", "UN":
				e.Status = avail.Closed
			case "US", "UU":
				e.Status = avail.Waitlist
			default:
				e.Status = avail.Open
			}
			out = append(out, e)
			continue
		}
		key, seats, ok := inv.pool(k.Carrier, k.FlightNum, wire, k.Board, k.Class)
		if !ok {
			// Not a flight the carrier operates that day: say nothing
			// rather than advertise a seat on it.
			continue
		}
		left := seats - inv.sold[key]
		e.Seats, e.SeatsKnown = max(left, 0), true
		switch {
		case left > 0:
			e.Status = avail.Open
		case inv.waitlisted[key] < inv.waitlistFor(seats):
			e.Status = avail.Waitlist
		default:
			e.Status = avail.Closed
		}
		out = append(out, e)
	}
	return out
}

// Pool is one cabin's count, for the console.
type Pool struct {
	Flight, Date, Board, Compartment string
	Seats, Sold, Waitlisted          int
}

// Snapshot lists every cabin with anything sold or waitlisted.
func (inv *Inventory) Snapshot() []Pool {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	seen := map[string]bool{}
	var out []Pool
	add := func(key string) {
		if seen[key] {
			return
		}
		seen[key] = true
		parts := strings.Split(key, "/")
		if len(parts) != 5 {
			return
		}
		p := Pool{Flight: parts[0] + parts[1], Date: parts[2], Board: parts[3], Compartment: parts[4], Sold: inv.sold[key], Waitlisted: inv.waitlisted[key]}
		if inv.Capacity != nil {
			if comps, ok := inv.Capacity(parts[0], parts[1], parts[2], parts[3]); ok {
				p.Seats = comps[parts[4]]
			}
		}
		out = append(out, p)
	}
	for k := range inv.sold {
		add(k)
	}
	for k := range inv.waitlisted {
		add(k)
	}
	return out
}

// awaitingDecision reports whether a segment status means somebody is
// waiting on us: HN and PN, and any request code that reached the record
// unmapped.
func awaitingDecision(status string) bool {
	if status == "HN" || status == "PN" {
		return true
	}
	return rescode.ActionCode(status).NeedsReply()
}
