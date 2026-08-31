// Package avail holds what a distribution system believes is sellable.
//
// Availability is not a message format, and this package deliberately knows
// about none. AVS pushes status over teletype, NDC returns it inside a priced
// offer, direct access answers a question per shop -- all three land here, and
// the thing that consumes availability should not care which one supplied it.
//
// Two properties are treated as first-class because leaving them implicit is
// how systems oversell:
//
// Age. Availability decays. An entry is a claim about a moment, and code that
// cannot tell a fresh claim from a day-old one will happily sell a seat that
// went hours ago. Every lookup returns how old its answer is.
//
// Provenance. A status pushed by the carrier, a status inferred from a booking
// reply, and a status somebody typed in are not equally trustworthy, and the
// difference matters when they disagree.
package avail

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Status is what a carrier says about a booking class on a flight.
//
// These are the distribution-level states, deliberately fewer than the status
// codes any one wire format defines: a carrier's private code maps onto one of
// these, and everything downstream reasons about these.
type Status string

const (
	// Open means the class may be sold without asking. This is free sale, and
	// it is the entire point of AVS: the carrier grants permission in advance
	// so a booking does not need a round trip.
	Open Status = "open"
	// Closed means do not sell. Negative information is still information, and
	// acting on it saves a doomed request.
	Closed Status = "closed"
	// Waitlist means the class is full but a waitlist is open.
	Waitlist Status = "waitlist"
	// Request means the carrier will not grant free sale; ask and wait.
	Request Status = "request"
	// Unknown means we hold no belief. Distinct from Closed: one is the
	// carrier's answer, the other is our ignorance, and conflating them either
	// blocks sellable inventory or sells inventory nobody offered.
	Unknown Status = "unknown"
)

// Sellable reports whether the status permits selling without asking first.
func (s Status) Sellable() bool { return s == Open }

// Source records where a belief came from, in descending order of authority.
type Source string

const (
	// SourceAVS is a status message pushed by the carrier.
	SourceAVS Source = "avs"
	// SourceNDC is availability carried in an offer.
	SourceNDC Source = "ndc"
	// SourceDirect is a direct availability request answered by the carrier.
	SourceDirect Source = "direct"
	// SourceReply is inferred from how a carrier answered a booking: a refusal
	// tells us the class was closed at that moment.
	SourceReply Source = "reply"
	// SourceManual is an operator override.
	SourceManual Source = "manual"
)

// authority ranks sources so a weaker belief cannot overwrite a stronger, more
// recent one. Direct access and NDC answer a specific question and outrank a
// broadcast; an inference from a booking reply outranks nothing.
var authority = map[Source]int{
	SourceManual: 5, SourceDirect: 4, SourceNDC: 3, SourceAVS: 2, SourceReply: 1,
}

// Key identifies a sellable unit: one booking class on one segment of one
// flight on one day.
//
// Board and off points are part of the key because availability is per
// segment, not per flight. A carrier can hold LHR-JFK closed while LHR-JFK-BOS
// stays open, and a key without the city pair cannot express that.
type Key struct {
	Carrier   string
	FlightNum string
	// Date is the departure date as YYYY-MM-DD. A string rather than a
	// time.Time so the key compares by value without a monotonic clock reading
	// or a location making two identical dates unequal.
	Date  string
	Board string
	Off   string
	Class string
}

// NewKey builds a key, normalising case and resolving the date.
func NewKey(carrier, flightNum string, depart time.Time, board, off, class string) Key {
	return Key{
		Carrier:   strings.ToUpper(carrier),
		FlightNum: strings.ToUpper(flightNum),
		Date:      depart.UTC().Format("2006-01-02"),
		Board:     strings.ToUpper(board),
		Off:       strings.ToUpper(off),
		Class:     strings.ToUpper(class),
	}
}

// Flight is the key without the booking class, identifying a segment.
func (k Key) Flight() Key { k.Class = ""; return k }

func (k Key) String() string {
	return fmt.Sprintf("%s%s/%s %s-%s/%s", k.Carrier, k.FlightNum, k.Date, k.Board, k.Off, k.Class)
}

// Entry is one belief about one key.
type Entry struct {
	Key    Key
	Status Status
	// Seats is the number of seats the source reported. Numeric AVS and direct
	// access give counts; plain AVS gives only a status, and SeatsKnown says
	// which happened. A zero count with SeatsKnown set means none left, which
	// is not the same as no count being supplied.
	Seats      int
	SeatsKnown bool
	Source     Source
	// AsOf is when the source asserted this, not when we stored it.
	AsOf time.Time
}

// Age reports how old the belief is relative to now.
func (e Entry) Age(now time.Time) time.Duration { return now.Sub(e.AsOf) }

func (e Entry) String() string {
	s := fmt.Sprintf("%s %s", e.Key, e.Status)
	if e.SeatsKnown {
		s += fmt.Sprintf(" (%d seats)", e.Seats)
	}
	return s + " via " + string(e.Source)
}

// Cache holds current beliefs about availability.
//
// Safe for concurrent use: a gateway updates it from every carrier link while
// booking reads from it.
type Cache struct {
	// StaleAfter is how long a belief is trusted. AVS is a push protocol, so
	// silence normally means no change -- but a link outage looks exactly like
	// silence, which is why a maximum trust window exists at all.
	StaleAfter time.Duration
	// Now is overridable for tests.
	Now func() time.Time

	mu      sync.RWMutex
	entries map[Key]Entry
}

// DefaultStaleAfter is a conservative trust window. Beyond it, a belief is
// reported as stale and free sale is withheld: asking the carrier costs a round
// trip, while selling a seat that went is a denied boarding.
const DefaultStaleAfter = 6 * time.Hour

// NewCache returns an empty cache.
func NewCache() *Cache {
	return &Cache{StaleAfter: DefaultStaleAfter, Now: time.Now, entries: map[Key]Entry{}}
}

func (c *Cache) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func (c *Cache) staleAfter() time.Duration {
	if c.StaleAfter > 0 {
		return c.StaleAfter
	}
	return DefaultStaleAfter
}

// Put records a belief.
//
// A weaker source cannot overwrite a stronger one that is still fresh, so a
// broadcast status does not erase the answer a carrier gave to a direct
// question a moment earlier. Between equals, newer wins; a strictly older
// assertion never wins, which keeps out-of-order delivery from moving state
// backwards.
func (c *Cache) Put(e Entry) bool {
	if e.AsOf.IsZero() {
		e.AsOf = c.now()
	}
	e.AsOf = e.AsOf.UTC()

	c.mu.Lock()
	defer c.mu.Unlock()
	if prev, ok := c.entries[e.Key]; ok {
		if e.AsOf.Before(prev.AsOf) {
			return false
		}
		if authority[e.Source] < authority[prev.Source] &&
			c.now().Sub(prev.AsOf) < c.staleAfter() {
			return false
		}
	}
	c.entries[e.Key] = e
	return true
}

// Lookup returns the belief held for a key, whether one exists, and whether it
// is still within the trust window.
func (c *Cache) Lookup(k Key) (Entry, bool, bool) {
	c.mu.RLock()
	e, ok := c.entries[k]
	c.mu.RUnlock()
	if !ok {
		return Entry{}, false, false
	}
	return e, true, e.Age(c.now()) < c.staleAfter()
}

// Status returns the effective status for a key: Unknown when nothing is held
// or the belief has gone stale, because a stale belief is not evidence.
func (c *Cache) Status(k Key) Status {
	e, ok, fresh := c.Lookup(k)
	if !ok || !fresh {
		return Unknown
	}
	return e.Status
}

// Decision is what the booking path should do about a segment.
type Decision string

const (
	// FreeSale means sell it and tell the carrier afterwards.
	FreeSale Decision = "free_sale"
	// Ask means send a request and wait for the answer.
	Ask Decision = "ask"
	// AskWaitlist means the class is full but a waitlist is open.
	AskWaitlist Decision = "ask_waitlist"
	// Refuse means the carrier has said this is closed; do not send anything.
	Refuse Decision = "refuse"
)

// Decide says how to handle a request for seats on a key, and explains why.
//
// The default when nothing is known is to ask, never to refuse and never to
// free-sell. Ignorance is not a closed flight, and it is not permission.
func (c *Cache) Decide(k Key, seats int) (Decision, string) {
	e, ok, fresh := c.Lookup(k)
	if !ok {
		return Ask, "no availability held for " + k.String()
	}
	age := e.Age(c.now()).Round(time.Second)
	if !fresh {
		return Ask, fmt.Sprintf("availability is %s old, beyond the %s trust window",
			age, c.staleAfter())
	}
	switch e.Status {
	case Closed:
		// Closed means no free sale, not a barred door: real distribution
		// still places the request, and the carrier answers -- often with
		// the waitlist a refusing shortcut would have denied.
		return Ask, fmt.Sprintf("carrier reported closed %s ago via %s; requesting", age, e.Source)
	case Waitlist:
		return AskWaitlist, fmt.Sprintf("carrier reported waitlist %s ago via %s", age, e.Source)
	case Open:
		// A count, where one was given, is the binding constraint: free sale is
		// permission to sell what was offered, not more.
		if e.SeatsKnown && seats > e.Seats {
			return Ask, fmt.Sprintf("%d seats wanted but only %d offered", seats, e.Seats)
		}
		return FreeSale, fmt.Sprintf("open %s ago via %s", age, e.Source)
	case Request:
		return Ask, fmt.Sprintf("carrier requires a request, reported %s ago", age)
	}
	return Ask, "status is unknown"
}

// Sold decrements a known seat count after a free sale, so two bookings in
// quick succession cannot both sell the last seat on the strength of one
// broadcast.
//
// The count is bookkeeping, not a carrier assertion: the status stays what
// the carrier said. At zero the decision logic stops free-selling and asks
// the carrier -- it once flipped the entry to Closed instead, and the next
// booking was refused with "carrier reported closed", words the carrier
// never sent.
func (c *Cache) Sold(k Key, seats int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[k]
	if !ok || !e.SeatsKnown {
		return
	}
	e.Seats -= seats
	if e.Seats < 0 {
		e.Seats = 0
	}
	c.entries[k] = e
}

// Forget removes a belief.
func (c *Cache) Forget(k Key) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, k)
}

// Purge drops beliefs older than the trust window, and any whose departure date
// has passed. Returns how many went.
func (c *Cache) Purge() int {
	now := c.now()
	today := now.Format("2006-01-02")
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for k, e := range c.entries {
		if e.Age(now) >= c.staleAfter() || k.Date < today {
			delete(c.entries, k)
			n++
		}
	}
	return n
}

// Len reports how many beliefs are held.
func (c *Cache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}

// Snapshot returns every belief, newest first, for the console and the API.
func (c *Cache) Snapshot() []Entry {
	c.mu.RLock()
	out := make([]Entry, 0, len(c.entries))
	for _, e := range c.entries {
		out = append(out, e)
	}
	c.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if !out[i].AsOf.Equal(out[j].AsOf) {
			return out[i].AsOf.After(out[j].AsOf)
		}
		return out[i].Key.String() < out[j].Key.String()
	})
	return out
}

// Classes returns the beliefs held for one segment, by booking class.
func (c *Cache) Classes(flight Key) map[string]Entry {
	flight = flight.Flight()
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := map[string]Entry{}
	for k, e := range c.entries {
		if k.Flight() == flight {
			out[k.Class] = e
		}
	}
	return out
}
