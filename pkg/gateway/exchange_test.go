package gateway

import (
	"context"
	"errors"
	"testing"

	"github.com/adamf/jetway/pkg/pnr"
)

// After a schedule change reprotects a passenger, the ticket is reissued
// over the live itinerary: the old document's open coupons are exchanged,
// the new document names the old one, a lifted coupon stays where it was,
// and the record reads ticketed again.
func TestExchangeReissuesOverTheLiveItinerary(t *testing.T) {
	gw, _ := cancelNode(t)
	ctx := context.Background()
	interlineRecord(t, gw, "EXC001")
	if _, err := gw.Exchange(ctx, "EXC001", ExchangeOptions{By: "adam"}); !errors.Is(err, ErrNothingToExchange) {
		t.Fatalf("exchange before ticketing: %v", err)
	}
	if _, err := gw.IssueTickets(ctx, "EXC001", IssueOptions{AirlineCode: "125", IssuedBy: "adam"}); err != nil {
		t.Fatal(err)
	}
	rec, _ := gw.Store.GetPNR(ctx, "EXC001")
	old := rec.Tickets[0].Number
	// The second segment is dropped by a schedule change and a new one sold.
	rec.Segments[1].Status = "XX"
	rec.Segments = append(rec.Segments, pnr.Segment{Ref: 3, Type: pnr.SegmentAir, Carrier: rec.Segments[1].Carrier, FlightNum: "0999", Class: "Y",
		Depart: rec.Segments[1].Depart, WireDate: rec.Segments[1].WireDate, Board: rec.Segments[1].Board, Off: rec.Segments[1].Off, Status: "HK", Seats: 1})
	if err := gw.Store.UpdatePNR(ctx, rec, rec.Version, nil); err != nil {
		t.Fatal(err)
	}
	if rec.Ticketed() {
		t.Fatal("a record whose new segment has no coupon is not ticketed")
	}
	after, err := gw.Exchange(ctx, "EXC001", ExchangeOptions{By: "irops", Reason: "schedule change"})
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Tickets) != 2 {
		t.Fatalf("tickets after exchange: %+v", after.Tickets)
	}
	oldT, newT := after.Tickets[0], after.Tickets[1]
	for _, c := range oldT.Coupons {
		if c.Status != pnr.CouponExchanged {
			t.Errorf("old coupon %d is %s, want exchanged", c.Number, c.Status)
		}
	}
	if newT.ExchangedFrom == nil || *newT.ExchangedFrom != old || newT.Number == old {
		t.Errorf("new document names the old: %+v", newT)
	}
	covers := map[int]bool{}
	for _, c := range newT.Coupons {
		covers[c.SegmentRef] = c.Status == pnr.CouponOpen
	}
	if !covers[1] || !covers[3] || covers[2] {
		t.Errorf("new coupons cover the live itinerary: %+v", newT.Coupons)
	}
	if !after.Ticketed() {
		t.Error("the record reads ticketed again")
	}
	// Exchanging again with nothing new does the same to the new document.
	if _, err := gw.Exchange(ctx, "EXC001", ExchangeOptions{By: "adam", Reason: "again"}); err != nil {
		t.Errorf("a second exchange: %v", err)
	}
	final, _ := gw.Store.GetPNR(ctx, "EXC001")
	if len(final.Tickets) != 3 || final.Tickets[2].ExchangedFrom == nil || *final.Tickets[2].ExchangedFrom != newT.Number {
		t.Errorf("chain of exchanges: %d tickets", len(final.Tickets))
	}
}
