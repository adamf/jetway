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
	"github.com/adamf/jetway/pkg/metrics"
	"strings"
	"sync"
	"time"

	"github.com/adamf/jetway/pkg/avail"
	"github.com/adamf/jetway/pkg/gateway"
	"github.com/adamf/jetway/pkg/pnr"
	"github.com/adamf/jetway/pkg/rescode"
)

// Level is one booking class's authorisation in a cabin: how many of the
// cabin's seats may be sold in this class and every class below it. Levels
// nest: the highest class carries the cabin's capacity, each lower class a
// number at or below the one above, and a class is open while its
// authorisation exceeds what is sold in it and beneath it. This is the
// serial nesting a revenue management system publishes; the numbers move
// as the flight fills and the department reconsiders.
type Level struct {
	Class      string
	Authorized int
}

// Levels returns the class ladder for a cabin, highest class first, or
// nil to sell every class while the cabin has a seat.
type Levels func(carrier, flightNum, wireDate, board, compartment string) []Level

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
	// Levels, when set, is the class ladder revenue management publishes
	// per cabin; nil sells every class to the last seat.
	Levels Levels

	mu         sync.Mutex
	sold       map[string]int // carrier/flight/date/board/compartment
	waitlisted map[string]int
	soldClass  map[string]int    // carrier/flight/date/board/compartment/class
	overrides  map[string]string // carrier/flight/date/board/class -> forced status
}

// New returns an empty inventory for a carrier.
func New(carrier string, capacity Capacity) *Inventory {
	return &Inventory{Carrier: carrier, Capacity: capacity, WaitlistShare: 0.1, MinWaitlist: 2,
		sold: map[string]int{}, waitlisted: map[string]int{}, soldClass: map[string]int{}, overrides: map[string]string{}}
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
	key, _, seats, ok = inv.cabin(carrier, flight, date, board, class)
	return key, seats, ok
}

func (inv *Inventory) cabin(carrier, flight, date, board, class string) (key, comp string, seats int, ok bool) {
	if inv.Capacity == nil {
		return "", "", 0, false
	}
	comps, ok := inv.Capacity(carrier, flight, date, board)
	if !ok || len(comps) == 0 {
		return "", "", 0, false
	}
	comp = CompartmentFor(class, comps)
	return poolKey(carrier, flight, date, board, comp), comp, comps[comp], true
}

// classOpen reports how many seats the class may still sell under the
// ladder: its authorisation less what is sold in it and every class below
// it. Without a ladder, or for a class not on it, the cabin's own count
// decides and this returns -1.
func (inv *Inventory) classOpen(carrier, flight, date, board, comp, class string) int {
	if inv.Levels == nil {
		return -1
	}
	ladder := inv.Levels(carrier, flight, date, board, comp)
	if len(ladder) == 0 {
		return -1
	}
	at := -1
	for i, l := range ladder {
		if strings.EqualFold(l.Class, class) {
			at = i
		}
	}
	if at < 0 {
		return -1
	}
	key := poolKey(carrier, flight, date, board, comp)
	below := 0
	for _, l := range ladder[at:] {
		below += inv.soldClass[key+"/"+strings.ToUpper(l.Class)]
	}
	return ladder[at].Authorized - below
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
		key, comp, seats, ok := inv.cabin(s.Carrier, s.FlightNum, s.WireDate, s.Board, s.Class)
		if !ok {
			// A flight the carrier does not operate on that date: unable,
			// and the requester's schedule is wrong, which UN says.
			out[s.Key()] = "UN"
			continue
		}
		// The class ladder closes a class before the cabin is full: a
		// closed class is unable, not waitlisted, because the seats exist
		// and are being held for a higher fare.
		if inv.sold[key]+s.Seats <= seats {
			if open := inv.classOpen(s.Carrier, s.FlightNum, s.WireDate, s.Board, comp, s.Class); open >= 0 && s.Seats > open {
				out[s.Key()] = "UC"
				continue
			}
		}
		switch {
		case inv.sold[key]+s.Seats <= seats:
			inv.sold[key] += s.Seats
			inv.soldClass[key+"/"+strings.ToUpper(s.Class)] += s.Seats
			out[s.Key()] = "KK"
		case inv.waitlisted[key]+s.Seats <= inv.waitlistFor(seats):
			inv.waitlisted[key] += s.Seats
			out[s.Key()] = "US"
		default:
			out[s.Key()] = "UC"
		}
	}
	for _, status := range out {
		metrics.Counter("jetway_inventory_decisions_total", "sell requests answered, by status code",
			metrics.Labels{"carrier": inv.Carrier, "status": status})
	}
	return out, nil
}

// Publish registers the inventory's aggregate gauges with a registry: seats
// sold, seats waitlisted and cabins with no seat left, per carrier. They are
// read at scrape time rather than kept, because a day is a hundred thousand
// cabins and the operator wants the shape, not each one; the per-cabin
// figure revenue management works from is Snapshot.
func (inv *Inventory) Publish(r *metrics.Registry) {
	r.OnCollect(func() {
		inv.mu.Lock()
		defer inv.mu.Unlock()
		sold, waiting, full := 0, 0, 0
		for key, n := range inv.sold {
			sold += n
			parts := strings.Split(key, "/")
			if len(parts) == 5 && inv.Capacity != nil {
				if comps, ok := inv.Capacity(parts[0], parts[1], parts[2], parts[3]); ok && n >= comps[parts[4]] {
					full++
				}
			}
		}
		for _, n := range inv.waitlisted {
			waiting += n
		}
		l := metrics.Labels{"carrier": inv.Carrier}
		r.Gauge("jetway_inventory_sold_seats", "seats sold across the carrier's cabins", l, float64(sold))
		r.Gauge("jetway_inventory_waitlisted_seats", "seats on waitlists across the carrier's cabins", l, float64(waiting))
		r.Gauge("jetway_inventory_full_cabins", "cabins with no seat left", l, float64(full))
	})
}

func (inv *Inventory) commit(s pnr.Segment, status string) {
	key, _, ok := inv.pool(s.Carrier, s.FlightNum, s.WireDate, s.Board, s.Class)
	if !ok {
		key = poolKey(s.Carrier, s.FlightNum, s.WireDate, s.Board, CompartmentFor(s.Class, nil))
	}
	switch status {
	case "KK", "KL", "TK", "HK":
		inv.sold[key] += s.Seats
		inv.soldClass[key+"/"+strings.ToUpper(s.Class)] += s.Seats
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
		ck := key + "/" + strings.ToUpper(s.Class)
		inv.soldClass[ck] = max(0, inv.soldClass[ck]-s.Seats)
	case "US", "UU", "TL", "HL":
		inv.waitlisted[key] = max(0, inv.waitlisted[key]-s.Seats)
	}
}

// Reset forgets every count, before a rebuild from the store.
func (inv *Inventory) Reset() {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	inv.sold, inv.waitlisted, inv.soldClass = map[string]int{}, map[string]int{}, map[string]int{}
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
		key, comp, seats, ok := inv.cabin(k.Carrier, k.FlightNum, wire, k.Board, k.Class)
		if !ok {
			// Not a flight the carrier operates that day: say nothing
			// rather than advertise a seat on it.
			continue
		}
		left := seats - inv.sold[key]
		if open := inv.classOpen(k.Carrier, k.FlightNum, wire, k.Board, comp, k.Class); open >= 0 && open < left {
			left = open
			if left <= 0 {
				// Closed by the ladder while the cabin has seats: not a
				// waitlist, the class is simply not for sale.
				e.Seats, e.SeatsKnown, e.Status = 0, true, avail.Closed
				out = append(out, e)
				continue
			}
		}
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

// SoldByClass is what a cabin has sold, class by class: the booked-to-date
// a forecaster adds its expected pickup to. Classes with nothing sold are
// absent.
func (inv *Inventory) SoldByClass(carrier, flightNum, wireDate, board, compartment string) map[string]int {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	prefix := poolKey(carrier, flightNum, wireDate, board, compartment) + "/"
	out := map[string]int{}
	for k, n := range inv.soldClass {
		if strings.HasPrefix(k, prefix) && n > 0 {
			out[strings.TrimPrefix(k, prefix)] += n
		}
	}
	return out
}
