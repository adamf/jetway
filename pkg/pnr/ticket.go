package pnr

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// TicketNumber is an IATA document number: a three-digit airline code and a
// ten-digit serial whose last digit is a check digit.
type TicketNumber struct {
	// AirlineCode is the three-digit numeric code, e.g. 125 for British
	// Airways. It is not the two-letter designator.
	AirlineCode string `json:"airline_code"`
	// Serial is the ten-digit document number, check digit included.
	Serial string `json:"serial"`
}

var ticketRe = regexp.MustCompile(`^(\d{3})[- ]?(\d{10})$`)

// ParseTicketNumber reads a document number in either the hyphenated or the
// solid form.
//
// It does not reject a number whose check digit disagrees. See CheckDigitOK for
// why that is a question the caller answers rather than a gate here.
func ParseTicketNumber(s string) (TicketNumber, error) {
	m := ticketRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return TicketNumber{}, fmt.Errorf("pnr: %q is not a 3+10 digit ticket number", s)
	}
	return TicketNumber{AirlineCode: m[1], Serial: m[2]}, nil
}

// String renders the conventional hyphenated form, e.g. "125-1234567890".
func (t TicketNumber) String() string {
	if t.AirlineCode == "" && t.Serial == "" {
		return ""
	}
	return t.AirlineCode + "-" + t.Serial
}

// Compact renders the thirteen digits with no separator, which is the form
// that travels on the wire.
func (t TicketNumber) Compact() string { return t.AirlineCode + t.Serial }

// IsZero reports whether the number is unset.
func (t TicketNumber) IsZero() bool { return t.AirlineCode == "" && t.Serial == "" }

// CheckDigit returns the mod-7 check digit for the first nine digits of a
// serial.
//
// The rule implemented is the value of those nine digits modulo seven, which is
// the same family as the air waybill algorithm and is why the final digit of a
// document number is never above six.
//
// Public descriptions of the ticket rule disagree: some say the value modulo
// seven, others the digit sum modulo seven, and the two are different
// algorithms. That disagreement is why CheckDigitOK is advisory and nothing
// here refuses a ticket number on the strength of it. Rejecting a genuine
// ticket because of an uncertain rule is a worse failure than accepting a
// mistyped one.
func CheckDigit(serial string) (int, error) {
	if len(serial) < 9 {
		return 0, fmt.Errorf("pnr: need at least nine digits to compute a check digit, got %d", len(serial))
	}
	n, err := strconv.ParseUint(serial[:9], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("pnr: serial is not numeric: %w", err)
	}
	return int(n % 7), nil
}

// CheckDigitOK reports whether the serial's last digit matches the computed
// check digit. Advisory: see CheckDigit.
func (t TicketNumber) CheckDigitOK() bool {
	if len(t.Serial) != 10 {
		return false
	}
	want, err := CheckDigit(t.Serial)
	if err != nil {
		return false
	}
	got, err := strconv.Atoi(t.Serial[9:])
	return err == nil && got == want
}

// NewTicketNumber builds a document number from an airline code and a nine
// digit serial, appending the check digit.
func NewTicketNumber(airlineCode, serial9 string) (TicketNumber, error) {
	if len(airlineCode) != 3 {
		return TicketNumber{}, fmt.Errorf("pnr: airline code must be three digits, got %q", airlineCode)
	}
	if len(serial9) != 9 {
		return TicketNumber{}, fmt.Errorf("pnr: serial must be nine digits before the check digit, got %q", serial9)
	}
	cd, err := CheckDigit(serial9)
	if err != nil {
		return TicketNumber{}, err
	}
	return TicketNumber{AirlineCode: airlineCode, Serial: serial9 + strconv.Itoa(cd)}, nil
}

// CouponStatus is the status of one flight coupon.
//
// The vocabulary and its three-way split are taken from IATA's Airline Guide to
// EMD Implementation, which publishes the electronic ticket coupon status
// indicators in full and is free. An earlier version of this file guessed the
// list and got it wrong in three places: it invented "N", mislabelled "X", and
// omitted "Y" and "G".
type CouponStatus string

// CouponClass groups a status by what can still happen to the coupon.
type CouponClass int

const (
	// ClassOpen is the one status that means untouched and available.
	ClassOpen CouponClass = iota
	// ClassInterim means something is happening to the coupon but it has not
	// been finished with. An interim coupon can still end up flown or refunded.
	ClassInterim
	// ClassFinal means the coupon is done with and no follow-up is permitted.
	ClassFinal
	// ClassUnknown covers codes not in the published list.
	ClassUnknown
)

// Coupon status indicators.
const (
	CouponOpen CouponStatus = "O" // open for use

	// Interim.
	CouponAirport       CouponStatus = "A" // airport control
	CouponRefundTaxes   CouponStatus = "Y" // refund taxes, fees and charges only
	CouponSuspended     CouponStatus = "S"
	CouponUnavailable   CouponStatus = "U"
	CouponCheckedIn     CouponStatus = "C"
	CouponIrregular     CouponStatus = "I" // irregular operations
	CouponLifted        CouponStatus = "L" // boarded
	CouponPrinted       CouponStatus = "P"
	CouponPrintExchange CouponStatus = "X"

	// Final.
	CouponExchanged CouponStatus = "E" // exchanged or reissued
	CouponExchFIM   CouponStatus = "G" // exchanged against a flight interruption manifest
	CouponFlown     CouponStatus = "F" // used
	CouponRefunded  CouponStatus = "R"
	CouponVoid      CouponStatus = "V"
	CouponClosed    CouponStatus = "Z"
)

type couponInfo struct {
	Meaning string
	Class   CouponClass
}

var couponStatuses = map[CouponStatus]couponInfo{
	CouponOpen: {"open for use", ClassOpen},

	CouponAirport:       {"airport control", ClassInterim},
	CouponRefundTaxes:   {"refund taxes, fees and charges only", ClassInterim},
	CouponSuspended:     {"suspended", ClassInterim},
	CouponUnavailable:   {"unavailable", ClassInterim},
	CouponCheckedIn:     {"checked in", ClassInterim},
	CouponIrregular:     {"irregular operations", ClassInterim},
	CouponLifted:        {"lifted or boarded", ClassInterim},
	CouponPrinted:       {"printed", ClassInterim},
	CouponPrintExchange: {"print exchange", ClassInterim},

	CouponExchanged: {"exchanged or reissued", ClassFinal},
	CouponExchFIM:   {"exchanged against a flight interruption manifest", ClassFinal},
	CouponFlown:     {"used", ClassFinal},
	CouponRefunded:  {"refunded", ClassFinal},
	CouponVoid:      {"void", ClassFinal},
	CouponClosed:    {"closed", ClassFinal},
}

// Meaning returns the code's meaning, or "" when it is not in the published set.
func (c CouponStatus) Meaning() string { return couponStatuses[c].Meaning }

// Class returns what can still happen to a coupon in this status.
func (c CouponStatus) Class() CouponClass {
	info, ok := couponStatuses[c]
	if !ok {
		return ClassUnknown
	}
	return info.Class
}

// Final reports whether the coupon is finished with. No follow-up is permitted
// on a final coupon, which is what makes it the wrong thing to reissue against.
func (c CouponStatus) Final() bool { return c.Class() == ClassFinal }

// Usable reports whether the coupon still stands: open, or interim and
// therefore not yet finished with. It is what decides whether a segment is
// covered by a ticket.
//
// An unknown code is not usable. A coupon whose status this build cannot
// interpret is one it must not claim covers a passenger.
func (c CouponStatus) Usable() bool {
	switch c.Class() {
	case ClassOpen, ClassInterim:
		return true
	}
	return false
}

// Coupon is one coupon of a document: a flight coupon on a ticket, or a value
// coupon on an EMD.
type Coupon struct {
	// Number is the coupon number within the document, 1 to 4.
	Number int `json:"number"`
	// SegmentRef is the segment this coupon covers.
	SegmentRef int          `json:"segment_ref"`
	Status     CouponStatus `json:"status"`

	// RFISC is the Reason for Issuance Sub-Code on an EMD value coupon: what
	// specifically was bought. Coupons of one document may carry different
	// sub-codes so long as they belong to the document's own reason group.
	//
	// The sub-code list is maintained by ATPCO and is not reproduced here.
	RFISC string `json:"rfisc,omitempty"`

	// Association links an EMD-A value coupon to the flight coupon it is
	// lifted with.
	Association Association `json:"association,omitempty"`

	// Amount and Currency are what the coupon is worth, where a value was
	// stated. This node prices nothing; the figure is carried, not computed.
	Amount   string `json:"amount,omitempty"`
	Currency string `json:"currency,omitempty"`
}

// MaxCoupons is how many flight coupons one ticket carries. An itinerary with
// more needs conjunction tickets.
const MaxCoupons = 4

// MaxConjunction is how many documents may be issued in conjunction. Four
// documents of four coupons is sixteen, which is the ceiling IATA publishes for
// a single electronic document set.
const MaxConjunction = 4

// MaxItinerary is the longest itinerary one conjunction set can cover.
const MaxItinerary = MaxCoupons * MaxConjunction

// Ticket is an electronic document issued against this record for one
// passenger: a flight ticket, or an EMD.
//
// One type covers both because they are the same artefact in every respect
// that this node handles -- number format, coupon structure, status
// vocabulary, conjunction rules. What differs is what a coupon buys, and that
// is Type, RFIC and the sub-code on each coupon.
type Ticket struct {
	Number TicketNumber `json:"number"`
	// Type is the kind of document. Empty means a flight ticket, so records
	// written before EMD existed read correctly.
	Type DocumentType `json:"type,omitempty"`
	// RFIC is the reason for issuance, on an EMD only. One document carries
	// exactly one.
	RFIC RFIC `json:"rfic,omitempty"`
	// PaxRef is the passenger the ticket was issued to.
	PaxRef   int       `json:"pax_ref"`
	IssuedAt time.Time `json:"issued_at"`
	IssuedBy string    `json:"issued_by,omitempty"`
	Coupons  []Coupon  `json:"coupons,omitempty"`
	// Conjunction lists the further documents an itinerary of more than four
	// coupons spills onto, in order.
	Conjunction []TicketNumber `json:"conjunction,omitempty"`
	// RefundedAt is when the document's open coupons were refunded, nil
	// while it stands. Settlement dates the refund transaction by it.
	RefundedAt *time.Time `json:"refunded_at,omitempty"`
	// ExchangedFrom names the document this one was issued in exchange
	// for -- a reissue after a schedule change or a change of plans --
	// which settlement reports as the original issue behind the new one.
	ExchangedFrom *TicketNumber `json:"exchanged_from,omitempty"`
}

// Refunded reports whether the document has been refunded: at least one
// coupon refunded and none still open for use.
func (t Ticket) Refunded() bool {
	if t.RefundedAt == nil {
		return false
	}
	for _, c := range t.Coupons {
		if c.Status == CouponOpen {
			return false
		}
	}
	return true
}

// Covers reports whether the ticket has a usable coupon for a segment.
func (t Ticket) Covers(segmentRef int) bool {
	for _, c := range t.Coupons {
		if c.SegmentRef == segmentRef && c.Status.Usable() {
			return true
		}
	}
	return false
}

// Ticketed reports whether every air segment is covered by a usable coupon for
// every passenger.
//
// This is the question a ticketing time limit actually asks. A record with one
// passenger ticketed out of two is not ticketed, and letting it look ticketed
// would let the limit pass on the passenger nobody issued for.
func (p *PNR) Ticketed() bool {
	// An EMD is not a ticket. A record carrying only a baggage document is not
	// ticketed, and counting it would let the time limit pass on a booking
	// nobody has issued carriage for.
	flight := p.FlightTickets()
	if len(flight) == 0 || len(p.Passengers) == 0 {
		return false
	}
	for _, pax := range p.Passengers {
		for i := range p.Segments {
			s := &p.Segments[i]
			if s.Type != SegmentAir || s.Status == "XX" {
				continue
			}
			covered := false
			for _, t := range flight {
				if t.PaxRef == pax.Ref && t.Covers(s.Ref) {
					covered = true
					break
				}
			}
			if !covered {
				return false
			}
		}
	}
	return true
}

// TicketFor returns the ticket issued to a passenger, if there is one.
func (p *PNR) TicketFor(paxRef int) (Ticket, bool) {
	for _, t := range p.Tickets {
		if t.PaxRef == paxRef {
			return t, true
		}
	}
	return Ticket{}, false
}

// NextDeadline is the soonest ticketing time limit this record still owes, or
// nil if it owes none.
//
// A ticketed record owes nothing: the deadline was met. Persisting this lets a
// sweeper ask the store which records are due rather than reading records and
// deciding afterwards, which is the difference between an indexed query and a
// full pass.
func (p *PNR) NextDeadline() *time.Time {
	if p.Status == StatusCancelled || p.Ticketed() {
		return nil
	}
	var soonest *time.Time
	for _, t := range p.Ticketing {
		if t.Deadline == nil {
			continue
		}
		if soonest == nil || t.Deadline.Before(*soonest) {
			soonest = t.Deadline
		}
	}
	return soonest
}
