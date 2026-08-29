package pnr

import (
	"fmt"
	"strings"
)

// Electronic miscellaneous documents.
//
// An EMD is a document like a ticket -- same number format, same coupon status
// vocabulary -- but its coupons buy something other than carriage: excess
// baggage, a meal, a residual balance, an airport service. It replaced the
// paper MCO.
//
// Everything asserted here comes from IATA's Airline Guide to EMD
// Implementation, which is free, or from Resolution 725f which that guide
// summarises. Where the guide points at a paid document -- the full list of
// sub-codes lives with ATPCO -- this package carries the structure and not the
// contents.

// DocumentType distinguishes the kinds of electronic document a record holds.
type DocumentType string

const (
	// DocTicket is an electronic flight ticket: coupons that buy carriage.
	DocTicket DocumentType = "T"
	// DocEMDA is an associated EMD. Its value coupons are linked to flight
	// coupons and are lifted at the same time, which is the electronic
	// equivalent of stapling an MCO to a flight coupon.
	DocEMDA DocumentType = "A"
	// DocEMDS is a standalone EMD, used independently of any ticket.
	DocEMDS DocumentType = "S"
)

// IsEMD reports whether the type is a miscellaneous document.
func (d DocumentType) IsEMD() bool { return d == DocEMDA || d == DocEMDS }

// String renders the type for display.
func (d DocumentType) String() string {
	switch d {
	case DocTicket:
		return "ticket"
	case DocEMDA:
		return "EMD-A"
	case DocEMDS:
		return "EMD-S"
	}
	return string(d)
}

// RFIC is the Reason for Issuance Code: what an EMD is broadly for.
//
// One document carries exactly one. A passenger buying two services under
// different codes needs two documents.
type RFIC string

// Reason for Issuance Codes, from Resolution 722f Attachment A as summarised in
// the EMD implementation guide.
const (
	RFICAir         RFIC = "A" // air transportation
	RFICSurface     RFIC = "B" // surface transportation and non-air services
	RFICBaggage     RFIC = "C"
	RFICFinancial   RFIC = "D" // financial impact
	RFICAirport     RFIC = "E" // airport services
	RFICMerchandise RFIC = "F"
	RFICInflight    RFIC = "G" // in-flight services
)

var rficMeaning = map[RFIC]string{
	RFICAir:         "air transportation",
	RFICSurface:     "surface transportation and non-air services",
	RFICBaggage:     "baggage",
	RFICFinancial:   "financial impact",
	RFICAirport:     "airport services",
	RFICMerchandise: "merchandise",
	RFICInflight:    "in-flight services",
}

// Meaning returns the code's meaning, or "" when it is not one of the seven.
func (r RFIC) Meaning() string { return rficMeaning[r] }

// Known reports whether the code is one of the seven published groups.
func (r RFIC) Known() bool { _, ok := rficMeaning[r]; return ok }

// Association links an EMD-A value coupon to the flight coupon it is lifted
// with. Association and disassociation are per coupon, not per document.
type Association struct {
	// Document is the ticket carrying the flight coupon.
	Document TicketNumber `json:"document"`
	// Coupon is the flight coupon number within it.
	Coupon int `json:"coupon"`
	// SegmentRef is the segment that coupon covers, where it is known here.
	SegmentRef int `json:"segment_ref,omitempty"`
}

// IsZero reports whether no association is recorded.
func (a Association) IsZero() bool { return a.Document.IsZero() && a.Coupon == 0 }

func (a Association) String() string {
	return fmt.Sprintf("%s coupon %d", a.Document, a.Coupon)
}

// emdForbiddenStatuses are the two coupon status indicators an EMD does not
// support. Both are print concepts and an EMD is never printed.
var emdForbiddenStatuses = map[CouponStatus]bool{
	CouponPrinted:       true,
	CouponPrintExchange: true,
}

// SupportsStatus reports whether a document of this type may carry a coupon
// status.
func (d DocumentType) SupportsStatus(s CouponStatus) bool {
	if s.Class() == ClassUnknown {
		return false
	}
	if d.IsEMD() && emdForbiddenStatuses[s] {
		return false
	}
	return true
}

// Validate checks a document against the rules the EMD standard states.
//
// These are the rules that make a document mean something. A document with no
// reason for issuance says a fee was charged without saying what for; an EMD-A
// with nothing associated is stapled to nothing; a standalone EMD that claims
// an association is two contradictory things at once.
func (t Ticket) Validate() error {
	typ := t.Type
	if typ == "" {
		typ = DocTicket
	}
	if len(t.Coupons) == 0 {
		return fmt.Errorf("pnr: %s %s has no coupons", typ, t.Number)
	}
	if len(t.Coupons) > MaxCoupons {
		return fmt.Errorf("pnr: %s %s has %d coupons, the limit is %d",
			typ, t.Number, len(t.Coupons), MaxCoupons)
	}
	for _, c := range t.Coupons {
		if !typ.SupportsStatus(c.Status) {
			return fmt.Errorf("pnr: %s does not support coupon status %q", typ, c.Status)
		}
	}
	if !typ.IsEMD() {
		if t.RFIC != "" {
			return fmt.Errorf("pnr: a flight ticket carries no reason for issuance")
		}
		return nil
	}

	// Both the code and the sub-code are mandatory on an EMD.
	if !t.RFIC.Known() {
		return fmt.Errorf("pnr: %s needs a reason for issuance code, got %q", typ, t.RFIC)
	}
	for _, c := range t.Coupons {
		if strings.TrimSpace(c.RFISC) == "" {
			return fmt.Errorf("pnr: %s coupon %d needs a reason for issuance sub-code",
				typ, c.Number)
		}
		switch {
		case typ == DocEMDA && c.Association.IsZero():
			return fmt.Errorf("pnr: EMD-A coupon %d is associated to nothing", c.Number)
		case typ == DocEMDS && !c.Association.IsZero():
			return fmt.Errorf("pnr: EMD-S coupon %d claims an association; a standalone document has none",
				c.Number)
		}
	}
	return nil
}

// EMDs returns the miscellaneous documents on a record.
func (p *PNR) EMDs() []Ticket {
	var out []Ticket
	for _, t := range p.Tickets {
		if t.Type.IsEMD() {
			out = append(out, t)
		}
	}
	return out
}

// FlightTickets returns the flight tickets on a record, which is what a
// ticketing time limit is asking about.
func (p *PNR) FlightTickets() []Ticket {
	var out []Ticket
	for _, t := range p.Tickets {
		if !t.Type.IsEMD() {
			out = append(out, t)
		}
	}
	return out
}

// Associated returns the EMD-A coupons linked to a flight coupon, which is what
// must be lifted alongside it.
func (p *PNR) Associated(document TicketNumber, coupon int) []Coupon {
	var out []Coupon
	for _, t := range p.Tickets {
		if t.Type != DocEMDA {
			continue
		}
		for _, c := range t.Coupons {
			if c.Association.Coupon == coupon &&
				c.Association.Document.Compact() == document.Compact() {
				out = append(out, c)
			}
		}
	}
	return out
}
