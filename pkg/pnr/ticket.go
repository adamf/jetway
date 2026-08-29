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
type CouponStatus string

// Coupon status codes from the IATA electronic ticket vocabulary.
const (
	CouponOpen          CouponStatus = "O" // open for use
	CouponAirport       CouponStatus = "A" // airport control
	CouponCheckedIn     CouponStatus = "C"
	CouponLifted        CouponStatus = "L" // boarded
	CouponFlown         CouponStatus = "F" // used
	CouponExchanged     CouponStatus = "E" // reissued
	CouponRefunded      CouponStatus = "R"
	CouponVoid          CouponStatus = "V"
	CouponPrinted       CouponStatus = "P"
	CouponIrregular     CouponStatus = "I" // irregular operations
	CouponSuspended     CouponStatus = "S"
	CouponClosed        CouponStatus = "Z"
	CouponUnavailable   CouponStatus = "U"
	CouponNotValid      CouponStatus = "N"
	CouponPrintExchange CouponStatus = "D" // deleted
)

// CouponStatusMeaning explains a coupon status code.
var CouponStatusMeaning = map[CouponStatus]string{
	CouponOpen: "open for use", CouponAirport: "airport control",
	CouponCheckedIn: "checked in", CouponLifted: "boarded",
	CouponFlown: "flown", CouponExchanged: "exchanged or reissued",
	CouponRefunded: "refunded", CouponVoid: "void", CouponPrinted: "printed",
	CouponIrregular: "irregular operations", CouponSuspended: "suspended",
	CouponClosed: "closed", CouponUnavailable: "unavailable",
	CouponNotValid: "not valid", CouponPrintExchange: "deleted",
}

// Meaning returns the code's meaning, or "" when it is not in the known set.
func (c CouponStatus) Meaning() string { return CouponStatusMeaning[c] }

// Usable reports whether the coupon can still be flown. It is what decides
// whether a segment is covered by a ticket.
func (c CouponStatus) Usable() bool {
	switch c {
	case CouponOpen, CouponAirport, CouponCheckedIn, CouponPrinted, CouponIrregular:
		return true
	}
	return false
}

// Coupon is one flight coupon of a ticket.
type Coupon struct {
	// Number is the coupon number within the ticket, 1 to 4.
	Number int `json:"number"`
	// SegmentRef is the segment this coupon covers.
	SegmentRef int          `json:"segment_ref"`
	Status     CouponStatus `json:"status"`
}

// MaxCoupons is how many flight coupons one ticket carries. An itinerary with
// more needs conjunction tickets.
const MaxCoupons = 4

// Ticket is a document issued against this record for one passenger.
type Ticket struct {
	Number TicketNumber `json:"number"`
	// PaxRef is the passenger the ticket was issued to.
	PaxRef   int       `json:"pax_ref"`
	IssuedAt time.Time `json:"issued_at"`
	IssuedBy string    `json:"issued_by,omitempty"`
	Coupons  []Coupon  `json:"coupons,omitempty"`
	// Conjunction lists the further documents an itinerary of more than four
	// coupons spills onto, in order.
	Conjunction []TicketNumber `json:"conjunction,omitempty"`
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
	if len(p.Tickets) == 0 || len(p.Passengers) == 0 {
		return false
	}
	for _, pax := range p.Passengers {
		for i := range p.Segments {
			s := &p.Segments[i]
			if s.Type != SegmentAir || s.Status == "XX" {
				continue
			}
			covered := false
			for _, t := range p.Tickets {
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
