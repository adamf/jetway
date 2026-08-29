package pnr

import (
	"testing"
	"time"
)

func TestTicketNumberParseAndRender(t *testing.T) {
	for _, in := range []string{"125-1234567890", "1251234567890", " 125 1234567890 "} {
		n, err := ParseTicketNumber(in)
		if err != nil {
			t.Fatalf("ParseTicketNumber(%q): %v", in, err)
		}
		if n.AirlineCode != "125" || n.Serial != "1234567890" {
			t.Errorf("%q parsed as %+v", in, n)
		}
		if n.String() != "125-1234567890" || n.Compact() != "1251234567890" {
			t.Errorf("render of %q = %q / %q", in, n.String(), n.Compact())
		}
	}
	for _, bad := range []string{"12-1234567890", "125-123456789", "abc-1234567890", ""} {
		if _, err := ParseTicketNumber(bad); err == nil {
			t.Errorf("ParseTicketNumber(%q) should have failed", bad)
		}
	}
}

func TestCheckDigit(t *testing.T) {
	// 123456789 mod 7 = 1
	if d, err := CheckDigit("123456789"); err != nil || d != 1 {
		t.Errorf("CheckDigit = %d, %v; want 1", d, err)
	}
	n, err := NewTicketNumber("125", "123456789")
	if err != nil {
		t.Fatal(err)
	}
	if n.Serial != "1234567891" {
		t.Errorf("Serial = %q, want the check digit appended", n.Serial)
	}
	if !n.CheckDigitOK() {
		t.Error("a number we just built must satisfy its own check")
	}
	// A check digit can never exceed six, which is the point of mod seven.
	for _, s := range []string{"000000000", "999999999", "123456789"} {
		d, err := CheckDigit(s)
		if err != nil || d < 0 || d > 6 {
			t.Errorf("CheckDigit(%q) = %d, %v", s, d, err)
		}
	}
	if _, err := CheckDigit("123"); err == nil {
		t.Error("a short serial has no check digit")
	}

	// Parsing must not reject a number whose check digit disagrees: the public
	// descriptions of the rule conflict, so this is advisory.
	bad, err := ParseTicketNumber("125-1234567899")
	if err != nil {
		t.Fatalf("parsing must not enforce the check digit: %v", err)
	}
	if bad.CheckDigitOK() {
		t.Error("this fixture is meant to fail the check")
	}
}

// The vocabulary and the three-way split come from IATA's Airline Guide to EMD
// Implementation, which publishes the electronic ticket coupon status
// indicators in full. An earlier version of this file guessed and got three of
// them wrong, so these assertions name the source.
func TestCouponStatusMatchesThePublishedList(t *testing.T) {
	open := []CouponStatus{CouponOpen}
	interim := []CouponStatus{
		CouponAirport, CouponRefundTaxes, CouponSuspended, CouponUnavailable,
		CouponCheckedIn, CouponIrregular, CouponLifted, CouponPrinted, CouponPrintExchange,
	}
	final := []CouponStatus{
		CouponExchanged, CouponExchFIM, CouponFlown, CouponRefunded, CouponVoid, CouponClosed,
	}

	for _, c := range open {
		if c.Class() != ClassOpen || !c.Usable() || c.Final() {
			t.Errorf("%s should be open and usable", c)
		}
	}
	for _, c := range interim {
		if c.Class() != ClassInterim {
			t.Errorf("%s (%s) should be interim", c, c.Meaning())
		}
		if !c.Usable() || c.Final() {
			t.Errorf("%s is interim, so it still stands", c)
		}
	}
	for _, c := range final {
		if c.Class() != ClassFinal {
			t.Errorf("%s (%s) should be final", c, c.Meaning())
		}
		if c.Usable() || !c.Final() {
			t.Errorf("%s is final; no follow-up is permitted", c)
		}
	}
	if len(open)+len(interim)+len(final) != 16 {
		t.Errorf("the published list has sixteen indicators, this has %d",
			len(open)+len(interim)+len(final))
	}

	// Codes the guide does not list.
	for _, c := range []CouponStatus{"N", "D", "Q", ""} {
		if c.Meaning() != "" {
			t.Errorf("%s is not in the published list and must not claim a meaning", c)
		}
		if c.Class() != ClassUnknown {
			t.Errorf("%s should be unknown", c)
		}
		// A coupon this build cannot interpret must not be claimed to cover
		// anybody.
		if c.Usable() {
			t.Errorf("an unknown status must not count as usable: %q", c)
		}
	}
}

func ticketedRecord() *PNR {
	return &PNR{
		RecordLocator: "TKT001",
		Passengers:    []Passenger{{Ref: 1, Surname: "A"}, {Ref: 2, Surname: "B"}},
		Segments: []Segment{
			{Ref: 1, Type: SegmentAir, Status: "HK"},
			{Ref: 2, Type: SegmentAir, Status: "HK"},
		},
	}
}

func TestTicketedNeedsEveryPassengerAndSegment(t *testing.T) {
	p := ticketedRecord()
	if p.Ticketed() {
		t.Error("a record with no tickets is not ticketed")
	}

	full := func(paxRef int) Ticket {
		return Ticket{
			Number: TicketNumber{AirlineCode: "125", Serial: "1234567891"}, PaxRef: paxRef,
			Coupons: []Coupon{
				{Number: 1, SegmentRef: 1, Status: CouponOpen},
				{Number: 2, SegmentRef: 2, Status: CouponOpen},
			},
		}
	}

	p.Tickets = []Ticket{full(1)}
	// One of two passengers ticketed is not ticketed: letting this look done
	// would let the limit pass on the passenger nobody issued for.
	if p.Ticketed() {
		t.Error("one passenger of two must not count as ticketed")
	}

	p.Tickets = append(p.Tickets, full(2))
	if !p.Ticketed() {
		t.Error("both passengers on both segments is ticketed")
	}

	// A refunded coupon stops covering its segment.
	p.Tickets[1].Coupons[0].Status = CouponRefunded
	if p.Ticketed() {
		t.Error("a refunded coupon must not cover a segment")
	}

	// A cancelled segment needs no coupon.
	p.Segments[0].Status = "XX"
	if !p.Ticketed() {
		t.Error("a cancelled segment must not hold the record un-ticketed")
	}
}

func TestTicketingExpiry(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	if !(Ticketing{Deadline: &past}).Expired(now) {
		t.Error("a deadline in the past has expired")
	}
	if (Ticketing{Deadline: &future}).Expired(now) {
		t.Error("a deadline in the future has not expired")
	}
	if (Ticketing{}).Expired(now) {
		t.Error("no deadline cannot expire")
	}
}
