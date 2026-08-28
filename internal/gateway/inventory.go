package gateway

import (
	"context"
	"hash/fnv"
	"strings"
	"sync"

	"github.com/adamf/jetway/pkg/pnr"
	"github.com/adamf/jetway/pkg/rescode"
)

// Inventory is a seat inventory for a simulated carrier.
//
// It is a stand-in for a real availability and inventory system, which is the
// component this project explicitly does not try to be. What matters here is
// that the interface a carrier plugs into is small and synchronous: given a
// record, return a status code per segment. A production deployment replaces
// this with a call into the carrier's own inventory and changes nothing else.
type Inventory struct {
	mu sync.Mutex
	// sold counts seats already confirmed per flight key.
	sold map[string]int
	// waitlisted counts seats on the waitlist per flight key.
	waitlisted map[string]int
	// overrides force an outcome for a flight key, which is how a demo shows a
	// refusal or a waitlist on demand.
	overrides map[string]string

	// Carrier is the designator this inventory holds seats for. Segments for
	// other carriers on a shared record are not this system's to answer.
	Carrier string

	// Capacity is the seats offered per booking class on a flight. Zero uses a
	// value derived from the flight number so that different flights behave
	// differently without configuration.
	Capacity int
	// WaitlistCapacity is how many seats may be waitlisted once sold out.
	WaitlistCapacity int
	// ClosedClasses are booking classes that are never available, so a demo can
	// reliably show an unable response.
	ClosedClasses map[string]bool
}

// NewInventory returns an inventory with demo-friendly defaults.
func NewInventory() *Inventory {
	return &Inventory{
		sold: map[string]int{}, waitlisted: map[string]int{}, overrides: map[string]string{},
		WaitlistCapacity: 2,
		// Z is conventionally a discounted class; here it is simply always
		// closed, so the console has a reliable way to show a refusal.
		ClosedClasses: map[string]bool{"Z": true},
	}
}

// flightKey identifies a flight, date and class.
func flightKey(s pnr.Segment) string {
	return strings.Join([]string{s.Carrier, s.FlightNum, s.WireDate, s.Class}, "/")
}

// capacityFor derives a stable seat count for a flight when none is configured.
// Deriving it from the flight number keeps the simulation deterministic: the
// same flight always has the same capacity across restarts, so a demo repeats.
func (inv *Inventory) capacityFor(s pnr.Segment) int {
	if inv.Capacity > 0 {
		return inv.Capacity
	}
	h := fnv.New32a()
	h.Write([]byte(s.Carrier + s.FlightNum + s.Class))
	return 2 + int(h.Sum32()%7) // 2 to 8 seats per class
}

// SetOverride forces the outcome for a flight key, e.g. "BA/0175/15JUN/Y".
func (inv *Inventory) SetOverride(key, status string) {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	inv.overrides[key] = status
}

// Decide answers a request, updating the inventory to reflect what it granted.
//
// The decision and the seat commitment happen under one lock. Deciding first
// and committing later is how a simulator oversells, and it is how real systems
// do too.
func (inv *Inventory) Decide(ctx context.Context, p *pnr.PNR, peer *Peer) (map[string]string, error) {
	inv.mu.Lock()
	defer inv.mu.Unlock()

	out := map[string]string{}
	for _, s := range p.Segments {
		if inv.Carrier != "" && s.Carrier != inv.Carrier {
			continue
		}
		// Only segments awaiting a decision are answered. A segment already at
		// HK is one we confirmed earlier, and re-answering it would double-count
		// the seats.
		if !awaitingDecision(s.Status) {
			continue
		}
		key := flightKey(s)
		if forced, ok := inv.overrides[key]; ok {
			out[s.Key()] = forced
			inv.commit(key, forced, s.Seats)
			continue
		}
		if inv.ClosedClasses[s.Class] {
			out[s.Key()] = "UC"
			continue
		}
		cap := inv.capacityFor(s)
		switch {
		case inv.sold[key]+s.Seats <= cap:
			inv.sold[key] += s.Seats
			out[s.Key()] = "KK"
		case inv.waitlisted[key]+s.Seats <= inv.WaitlistCapacity:
			inv.waitlisted[key] += s.Seats
			// US: unable to confirm, but the passenger is now waitlisted.
			out[s.Key()] = "US"
		default:
			// UC: unable, and the waitlist is closed too.
			out[s.Key()] = "UC"
		}
	}
	return out, nil
}

func (inv *Inventory) commit(key, status string, seats int) {
	switch status {
	case "KK", "KL", "TK":
		inv.sold[key] += seats
	case "US", "UU", "TL":
		inv.waitlisted[key] += seats
	}
}

// Snapshot returns the current inventory state, for the console.
func (inv *Inventory) Snapshot() map[string]map[string]int {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	out := map[string]map[string]int{}
	for k, v := range inv.sold {
		out[k] = map[string]int{"sold": v, "waitlisted": inv.waitlisted[k]}
	}
	for k, v := range inv.waitlisted {
		if _, ok := out[k]; !ok {
			out[k] = map[string]int{"sold": 0, "waitlisted": v}
		}
	}
	return out
}

// awaitingDecision reports whether a segment status means somebody is waiting
// on us. HN is the canonical form, but a request code that reached the record
// unmapped counts too rather than being silently ignored.
func awaitingDecision(status string) bool {
	if status == "HN" || status == "PN" {
		return true
	}
	return rescode.ActionCode(status).NeedsReply()
}
