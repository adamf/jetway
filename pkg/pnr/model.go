// Package pnr defines the canonical Passenger Name Record that every wire
// format maps onto.
//
// The canonical model exists so that AIRIMP and EDIFACT traffic converge on one
// representation before anything acts on it. It is deliberately lossy-tolerant:
// content that a mapper could not place lands in Unparsed rather than being
// discarded, because the alternative is discovering months later that a carrier
// had been sending something you needed.
package pnr

import (
	"fmt"
	"strings"
	"time"
)

// Status is the lifecycle state of a booking.
type Status string

const (
	StatusOpen      Status = "open"      // active, held or requested segments
	StatusTicketed  Status = "ticketed"  // a ticket number is associated
	StatusCancelled Status = "cancelled" // all segments cancelled
	StatusSplit     Status = "split"     // divided into a child record
)

// SegmentType distinguishes flown segments from the placeholders that keep an
// itinerary continuous.
type SegmentType string

const (
	SegmentAir SegmentType = "air"
	// SegmentSurface is an ARNK: a gap the passenger covers by other means.
	// It carries no carrier and is never sold, but its presence is what makes
	// an itinerary's board and off points line up.
	SegmentSurface SegmentType = "surface"
	SegmentAux     SegmentType = "auxiliary"
)

// PassengerType is the PADIS traveller type code carried in TIF.
type PassengerType string

const (
	PaxAdult  PassengerType = "A"
	PaxChild  PassengerType = "C"
	PaxInfant PassengerType = "IN"
	PaxGroup  PassengerType = "G"
)

// Passenger is one traveller on the record.
type Passenger struct {
	Ref     int    `json:"ref"` // 1-based, stable for the life of the record
	Surname string `json:"surname"`
	Given   string `json:"given"`
	Title   string `json:"title,omitempty"`
	// Type is the traveller type code. Infant is derived from it rather than
	// being a separate assertion, so the two can never disagree.
	Type   PassengerType `json:"type,omitempty"`
	Infant bool          `json:"infant,omitempty"`
	// FrequentFlyer holds carrier-qualified loyalty numbers, e.g. "BA:12345678".
	FrequentFlyer []string `json:"frequent_flyer,omitempty"`
}

// titles are the honorifics that arrive suffixed to a given name on both wire
// formats. Longest first, so MSTR is not truncated to MS.
var titles = []string{"MSTR", "MISS", "MRS", "DR", "MR", "MS"}

// SplitTitle separates a trailing honorific from a concatenated given name,
// turning "JOHNMR" into "JOHN" and "MR".
//
// Both AIRIMP and PADIS carry the name this way, which is why this lives with
// the canonical model rather than in either codec: two copies of this list
// would drift, and a name that splits differently on the two paths produces a
// record that disagrees with itself.
func SplitTitle(given string) (name, title string) {
	g := strings.TrimSpace(given)
	for _, t := range titles {
		if len(g) > len(t) && strings.HasSuffix(g, t) {
			return g[:len(g)-len(t)], t
		}
	}
	return g, ""
}

// Display renders the passenger in the conventional SURNAME/GIVEN TITLE form.
func (p Passenger) Display() string {
	s := p.Surname + "/" + p.Given
	if p.Title != "" {
		s += " " + p.Title
	}
	return s
}

// Segment is one leg of the itinerary.
type Segment struct {
	Ref  int         `json:"ref"` // 1-based position in the itinerary
	Type SegmentType `json:"type"`

	// Carrier is the marketing carrier: whose code the passenger booked under.
	Carrier string `json:"carrier,omitempty"`
	// OperatingCarrier is who actually flies it, when it differs. Interline
	// messages address the operating carrier, so losing this means sending a
	// request to a carrier that does not hold the inventory.
	OperatingCarrier string `json:"operating_carrier,omitempty"`
	FlightNum        string `json:"flight_num,omitempty"`
	Class            string `json:"class,omitempty"`

	// Depart is the absolute departure date, resolved from the two-character
	// day and three-letter month that the wire carries. See ResolveDate for why
	// this is not simply parsed.
	Depart     time.Time `json:"depart"`
	DepartTime string    `json:"depart_time,omitempty"` // HHMM local to Board
	ArriveTime string    `json:"arrive_time,omitempty"` // HHMM local to Off

	Board string `json:"board,omitempty"`
	Off   string `json:"off,omitempty"`

	// Status is the current holding status: HK, HL, HN and so on. A segment we
	// have requested but not heard back on sits at HN.
	Status string `json:"status"`
	Seats  int    `json:"seats"`

	// CarrierLocator is the operating carrier's own record locator for this
	// segment, learned from a reply. Without it, follow-up messages to that
	// carrier cannot be matched to their record.
	CarrierLocator string `json:"carrier_locator,omitempty"`

	// WireDate is the DDMMM as received, kept so that outbound messages echo
	// exactly what the partner sent rather than a re-derived value.
	WireDate string `json:"wire_date,omitempty"`
}

// Key identifies the segment independently of status and seat count, which is
// how a reply is matched back to the request that caused it.
func (s Segment) Key() string {
	return strings.Join([]string{s.Carrier, s.FlightNum, s.Class, s.WireDate, s.Board, s.Off}, "|")
}

// Describe renders the segment for logs and the console.
func (s Segment) Describe() string {
	if s.Type == SegmentSurface {
		return "ARNK"
	}
	return fmt.Sprintf("%s%s %s %s %s-%s %s%d",
		s.Carrier, s.FlightNum, s.Class, s.WireDate, s.Board, s.Off, s.Status, s.Seats)
}

// SSR is a special service request held on the record.
type SSR struct {
	Code       string `json:"code"`
	Carrier    string `json:"carrier,omitempty"`
	Status     string `json:"status"`
	Count      int    `json:"count"`
	SegmentRef int    `json:"segment_ref,omitempty"`
	PaxRef     int    `json:"pax_ref,omitempty"`
	Text       string `json:"text,omitempty"`
	// Sensitive marks personal data: passport, address and visa details carried
	// in DOCS, DOCA, DOCO and FOID. The store encrypts these at rest and the
	// console redacts them.
	Sensitive bool `json:"sensitive,omitempty"`
}

// OSI is advisory information for a carrier, with no status and no reply.
type OSI struct {
	Carrier string `json:"carrier,omitempty"`
	Text    string `json:"text"`
}

// Contact is a phone or address element.
type Contact struct {
	Type string `json:"type,omitempty"` // e.g. "phone", "email"
	Text string `json:"text"`
}

// Ticketing is a ticketing arrangement or time limit.
type Ticketing struct {
	Text     string     `json:"text"`
	Deadline *time.Time `json:"deadline,omitempty"`
}

// Ticketed reports whether a ticketing arrangement carries a deadline that has
// passed at the given time.
func (t Ticketing) Expired(at time.Time) bool {
	return t.Deadline != nil && at.After(*t.Deadline)
}

// Remark is free text carried on the record.
type Remark struct {
	Text string `json:"text"`
}

// ExternalLocator is another system's record locator for this booking.
type ExternalLocator struct {
	// Owner is the carrier or system designator, e.g. "BA" or "1A".
	Owner string `json:"owner"`
	Value string `json:"value"`
}

// Fragment is content a mapper could not place. Keeping it attached to the
// record is what makes a dialect gap diagnosable instead of invisible.
type Fragment struct {
	Source string `json:"source"` // "airimp" or "edifact"
	Detail string `json:"detail"` // segment tag or element description
	Raw    string `json:"raw"`
}

// Origin records where the booking came from.
type Origin struct {
	// Party is the partner identifier: a Type B address or an EDIFACT sender.
	Party string `json:"party,omitempty"`
	// Agent is the booking agent or office, when the message carried one.
	Agent string `json:"agent,omitempty"`
	// Channel is the transport the first message arrived on.
	Channel string `json:"channel,omitempty"`
}

// PNR is the canonical record.
type PNR struct {
	ID            string `json:"id"`
	RecordLocator string `json:"record_locator"`
	// Version increments on every applied change and is the optimistic
	// concurrency token. A gateway and a carrier can both be modifying a record
	// at once; a blind write loses one of them.
	Version int64 `json:"version"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Status    Status    `json:"status"`

	Passengers []Passenger       `json:"passengers,omitempty"`
	Segments   []Segment         `json:"segments,omitempty"`
	SSRs       []SSR             `json:"ssrs,omitempty"`
	OSIs       []OSI             `json:"osis,omitempty"`
	Contacts   []Contact         `json:"contacts,omitempty"`
	Ticketing  []Ticketing       `json:"ticketing,omitempty"`
	Remarks    []Remark          `json:"remarks,omitempty"`
	Locators   []ExternalLocator `json:"locators,omitempty"`
	// Tickets are the documents issued against this record. A booking with no
	// ticket is a booking that will be cancelled when its time limit passes.
	Tickets []Ticket `json:"tickets,omitempty"`

	ReceivedFrom string     `json:"received_from,omitempty"`
	Origin       Origin     `json:"origin"`
	Unparsed     []Fragment `json:"unparsed,omitempty"`
}

// SegmentByKey returns a pointer to the segment matching key, or nil.
func (p *PNR) SegmentByKey(key string) *Segment {
	for i := range p.Segments {
		if p.Segments[i].Key() == key {
			return &p.Segments[i]
		}
	}
	return nil
}

// SegmentByRef returns a pointer to the segment with the given reference.
func (p *PNR) SegmentByRef(ref int) *Segment {
	for i := range p.Segments {
		if p.Segments[i].Ref == ref {
			return &p.Segments[i]
		}
	}
	return nil
}

// LocatorFor returns the external locator held for owner.
func (p *PNR) LocatorFor(owner string) (string, bool) {
	for _, l := range p.Locators {
		if l.Owner == owner {
			return l.Value, true
		}
	}
	return "", false
}

// SetLocator records or replaces an external locator.
func (p *PNR) SetLocator(owner, value string) {
	for i := range p.Locators {
		if p.Locators[i].Owner == owner {
			p.Locators[i].Value = value
			return
		}
	}
	p.Locators = append(p.Locators, ExternalLocator{Owner: owner, Value: value})
}

// Carriers returns the distinct marketing carriers on the itinerary, in order.
func (p *PNR) Carriers() []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range p.Segments {
		if s.Carrier == "" || seen[s.Carrier] {
			continue
		}
		seen[s.Carrier] = true
		out = append(out, s.Carrier)
	}
	return out
}

// AwaitingReply reports whether any segment is still in a requested state, and
// is what a timeout sweep keys off.
func (p *PNR) AwaitingReply() bool {
	for _, s := range p.Segments {
		if s.Status == "HN" || s.Status == "PN" {
			return true
		}
	}
	return false
}

// Recompute derives Status from the segments and renumbers references so they
// stay dense and ordered after edits.
func (p *PNR) Recompute() {
	for i := range p.Segments {
		p.Segments[i].Ref = i + 1
	}
	for i := range p.Passengers {
		p.Passengers[i].Ref = i + 1
	}
	if p.Status == StatusSplit {
		return
	}
	live := 0
	for _, s := range p.Segments {
		if s.Status != "XX" && s.Status != "HX" && s.Status != "UC" && s.Status != "UN" {
			live++
		}
	}
	switch {
	case len(p.Segments) == 0 || live == 0:
		p.Status = StatusCancelled
	case p.Status == StatusTicketed:
		// Ticketing is sticky: a ticketed record stays ticketed.
	default:
		p.Status = StatusOpen
	}
}

// Redacted returns a copy with personal data removed, for logging and for
// sharing a record with a party not entitled to see travel documents.
func (p *PNR) Redacted() *PNR {
	c := *p
	c.SSRs = make([]SSR, len(p.SSRs))
	copy(c.SSRs, p.SSRs)
	for i := range c.SSRs {
		if c.SSRs[i].Sensitive {
			c.SSRs[i].Text = "[redacted]"
		}
	}
	c.Contacts = make([]Contact, len(p.Contacts))
	for i := range p.Contacts {
		c.Contacts[i] = Contact{Type: p.Contacts[i].Type, Text: "[redacted]"}
	}
	return &c
}
