package airimp

import (
	"fmt"
	"strconv"
	"strings"
)

// Kind names an element type.
type Kind string

const (
	KindSegment      Kind = "segment"
	KindName         Kind = "name"
	KindSSR          Kind = "ssr"
	KindOSI          Kind = "osi"
	KindLocator      Kind = "locator"
	KindTicketing    Kind = "ticketing"
	KindContact      Kind = "contact"
	KindReceivedFrom Kind = "received_from"
	KindRemark       Kind = "remark"
	KindUnknown      Kind = "unknown"
)

// Element is one line of AIRIMP message text.
//
// Wire must reproduce the element in the form this package would send. For
// elements decoded from a peer it is not guaranteed to reproduce the received
// bytes; use Message.Text for that.
type Element interface {
	Kind() Kind
	Wire() string
}

// Segment is a flight segment element, the core of a sell or reply message:
//
//	BA0175Y15JUNLHRJFKNN1
//	│ │   │ │    │  │  │ └─ number of seats
//	│ │   │ │    │  │  └─── action or status code
//	│ │   │ │    │  └────── off point
//	│ │   │ │    └───────── board point
//	│ │   │ └────────────── departure date, DDMMM
//	│ │   └──────────────── booking class
//	│ └──────────────────── flight number, with optional operational suffix
//	└────────────────────── carrier designator
type Segment struct {
	Carrier   string
	FlightNum string
	Class     string
	Date      string // DDMMM as sent; no year is carried on the wire
	Board     string
	Off       string
	Action    ActionCode
	Seats     int
	// Trailer holds anything after the seat count, such as times or a
	// day-of-week indicator. Kept whole because its format varies by carrier.
	Trailer string
}

func (s *Segment) Kind() Kind { return KindSegment }

func (s *Segment) Wire() string {
	w := fmt.Sprintf("%s%s%s%s%s%s%s%d",
		s.Carrier, s.FlightNum, s.Class, s.Date, s.Board, s.Off, s.Action, s.Seats)
	if s.Trailer != "" {
		w += s.Trailer
	}
	return w
}

// Key identifies the segment independently of its action code and seat count,
// which is what matching a reply back to a request requires.
func (s *Segment) Key() string {
	return strings.Join([]string{s.Carrier, s.FlightNum, s.Class, s.Date, s.Board, s.Off}, "|")
}

// Name is a passenger name element: a seat count, a surname and one or more
// given-name-and-title groups sharing that surname.
type Name struct {
	Count   int
	Surname string
	// Givens holds each given name with its title appended, e.g. "JOHNMR".
	Givens []string
	// Infant marks an infant-in-arms association carried in the element.
	Infant bool
}

func (n *Name) Kind() Kind { return KindName }

func (n *Name) Wire() string {
	return fmt.Sprintf("%d%s/%s", n.Count, n.Surname, strings.Join(n.Givens, "/"))
}

// Full renders the i-th traveller as "SURNAME/GIVEN".
func (n *Name) Full(i int) string {
	if i < 0 || i >= len(n.Givens) {
		return n.Surname
	}
	return n.Surname + "/" + n.Givens[i]
}

// SSR is a Special Service Request element. SSRs expect an action from the
// receiving carrier and carry a status code, unlike OSI.
type SSR struct {
	Code    string // four-letter service code, e.g. VGML, WCHR, DOCS
	Carrier string
	Action  ActionCode
	Count   int
	// Itinerary is the optional flight reference the request applies to.
	Itinerary string
	// FreeText is the remainder, whose structure depends on Code. DOCS and
	// DOCA in particular carry passport and address data and must be treated
	// as personal data end to end.
	FreeText string
	// NameRef associates the request with one traveller, when present.
	NameRef string
}

func (s *SSR) Kind() Kind { return KindSSR }

func (s *SSR) Wire() string {
	var b strings.Builder
	fmt.Fprintf(&b, "SSR %s %s %s%d", s.Code, s.Carrier, s.Action, s.Count)
	if s.Itinerary != "" {
		b.WriteString(" " + s.Itinerary)
	}
	if s.FreeText != "" {
		b.WriteString(" " + s.FreeText)
	}
	if s.NameRef != "" {
		b.WriteString("/" + s.NameRef)
	}
	return b.String()
}

// Sensitive reports whether the SSR carries personal data that must be
// encrypted at rest and excluded from logs. DOCS, DOCA and DOCO hold passport,
// address and visa details.
func (s *SSR) Sensitive() bool {
	switch strings.ToUpper(s.Code) {
	case "DOCS", "DOCA", "DOCO", "FOID":
		return true
	}
	return false
}

// OSI is Other Service Information: advisory free text addressed to a carrier,
// with no status code and no reply expected.
type OSI struct {
	Carrier string
	Text    string
}

func (o *OSI) Kind() Kind   { return KindOSI }
func (o *OSI) Wire() string { return "OSI " + o.Carrier + " " + o.Text }

// Locator carries a carrier's own record locator for the booking.
type Locator struct {
	Carrier string
	Value   string
}

func (l *Locator) Kind() Kind   { return KindLocator }
func (l *Locator) Wire() string { return "RL " + l.Carrier + "/" + l.Value }

// Ticketing is the ticketing arrangement or time limit element.
type Ticketing struct{ Text string }

func (t *Ticketing) Kind() Kind   { return KindTicketing }
func (t *Ticketing) Wire() string { return "TK " + t.Text }

// Contact is a phone or address contact element.
type Contact struct{ Text string }

func (c *Contact) Kind() Kind   { return KindContact }
func (c *Contact) Wire() string { return "AP " + c.Text }

// ReceivedFrom records who authorised the change.
type ReceivedFrom struct{ Text string }

func (r *ReceivedFrom) Kind() Kind   { return KindReceivedFrom }
func (r *ReceivedFrom) Wire() string { return "RF " + r.Text }

// Remark is a free-text remark.
type Remark struct{ Text string }

func (r *Remark) Kind() Kind   { return KindRemark }
func (r *Remark) Wire() string { return "RM " + r.Text }

// Unknown is a line no recognizer claimed. It is kept verbatim so that an
// unfamiliar dialect degrades to pass-through rather than to data loss.
type Unknown struct {
	Line string
	// LineNo is the 1-based line within the message text.
	LineNo int
}

func (u *Unknown) Kind() Kind   { return KindUnknown }
func (u *Unknown) Wire() string { return u.Line }

func atoiOr(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}
