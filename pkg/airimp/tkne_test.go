package airimp

import (
	"strings"
	"testing"
	"time"

	"github.com/adamf/jetway/pkg/pnr"
)

// A ticket goes to a teletype carrier as SSR TKNE per coupon, and the
// carrier's record, applying the message, holds the ticket with its
// coupons on the right segments and the right traveller.
func TestTicketAdviceRoundTripsIntoTheCarriersRecord(t *testing.T) {
	dep := time.Date(2026, 12, 16, 0, 0, 0, 0, time.UTC)
	rec := &pnr.PNR{RecordLocator: "ABC123", Origin: pnr.Origin{Party: "1G"},
		Passengers: []pnr.Passenger{{Ref: 1, Surname: "SMITH", Given: "JOHN", Title: "MR"}, {Ref: 2, Surname: "SMITH", Given: "ANN", Title: "MRS"}},
		Segments: []pnr.Segment{
			{Ref: 1, Type: pnr.SegmentAir, Carrier: "BA", FlightNum: "0117", Class: "Y", Depart: dep, WireDate: "16DEC", Board: "LHR", Off: "JFK", Status: "HK", Seats: 2},
			{Ref: 2, Type: pnr.SegmentAir, Carrier: "BA", FlightNum: "0112", Class: "Y", Depart: dep.AddDate(0, 0, 7), WireDate: "23DEC", Board: "JFK", Off: "LHR", Status: "HK", Seats: 2},
		},
		Locators: []pnr.ExternalLocator{{Owner: "BA", Value: "XYZ789"}},
	}
	tk := pnr.Ticket{Number: pnr.TicketNumber{AirlineCode: "125", Serial: "2400123456"}, PaxRef: 2, IssuedBy: "1G",
		Coupons: []pnr.Coupon{{Number: 1, SegmentRef: 1, Status: pnr.CouponOpen}, {Number: 2, SegmentRef: 2, Status: pnr.CouponOpen}}}
	text := BuildTicketAdvice(rec, "BA", tk)
	for _, want := range []string{"1SMITH/ANNMRS", "SSR TKNE BA HK1 LHRJFK0117Y16DEC /1252400123456C1", "SSR TKNE BA HK1 JFKLHR0112Y23DEC /1252400123456C2", "RL 1G/ABC123", "RL BA/XYZ789"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in\n%s", want, text)
		}
	}
	for _, l := range strings.Split(text, "\n") {
		if len(l) > 63 {
			t.Errorf("a Type B line may carry 63 characters; %d: %q", len(l), l)
		}
	}
	// The carrier's copy of the record: the same people and flights, no
	// ticket yet.
	theirs := &pnr.PNR{RecordLocator: "XYZ789", Passengers: rec.Passengers, Segments: rec.Segments}
	msg := Parse(text)
	changes := Apply(theirs, msg, ApplyOptions{Party: "1G", ReceivedAt: dep.AddDate(0, 0, -30)})
	if len(theirs.Tickets) != 1 {
		t.Fatalf("tickets after TKNE: %+v (changes %v)", theirs.Tickets, changes)
	}
	got := theirs.Tickets[0]
	if got.Number.AirlineCode != "125" || got.Number.Serial != "2400123456" || got.PaxRef != 2 || got.IssuedBy != "BA" || got.Type != pnr.DocTicket {
		t.Errorf("ticket: %+v", got)
	}
	if len(got.Coupons) != 2 || got.Coupons[0].SegmentRef != 1 || got.Coupons[1].SegmentRef != 2 || got.Coupons[1].Number != 2 {
		t.Errorf("coupons: %+v", got.Coupons)
	}
	// Told twice, the record holds it once.
	Apply(theirs, msg, ApplyOptions{Party: "1G", ReceivedAt: dep.AddDate(0, 0, -30)})
	if len(theirs.Tickets) != 1 || len(theirs.Tickets[0].Coupons) != 2 {
		t.Errorf("a repeated advice duplicated the ticket: %+v", theirs.Tickets)
	}
	// A ticket covering another carrier's segments says nothing to this one.
	if BuildTicketAdvice(rec, "AA", tk) != "" {
		t.Error("advice for a carrier with no coupon")
	}
}
