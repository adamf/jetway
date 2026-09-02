package gateway

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/adamf/jetway/pkg/fare"
	"github.com/adamf/jetway/pkg/pnr"
	"github.com/adamf/jetway/pkg/store"
)

func tariff() *fare.Filing {
	usd := func(c int64) fare.Money { return fare.Money{Amount: c, Currency: "USD"} }
	return &fare.Filing{
		Filed: []fare.Fare{
			{Carrier: "BA", Origin: "LHR", Destination: "JFK", Class: "Y", Basis: "YOW", OneWay: usd(120000)},
			{Carrier: "BA", Origin: "LHR", Destination: "JFK", Class: "K", Basis: "KLX21", OneWay: usd(45000), Rule: fare.Rule{AdvancePurchaseDays: 21}},
		},
		Levies: []fare.Tax{{Code: "GB", Kind: fare.PerTicket, Amount: usd(9400)}, {Code: "YQ", Kind: fare.PerSegment, Amount: usd(25000)}},
	}
}

// A node with a tariff prices what it books: the fare basis on the segment,
// the money on the record, and, when tickets are issued, the value on each
// coupon. A class with no sellable fare refuses the booking by rule.
func TestBookingIsPricedAgainstTheTariff(t *testing.T) {
	ctx := context.Background()
	gds, _ := wire(t, "BA", store.FormatTypeB)
	gds.gw.Tariff = tariff()
	res, err := gds.gw.Book(ctx, &BookingRequest{
		Passengers: []BookingPassenger{{Surname: "PRICED", Given: "ANN", Title: "MS"}, {Surname: "PRICED", Given: "TOM", Title: "MSTR"}},
		Segments:   []BookingSegment{{Carrier: "BA", FlightNum: "0175", Class: "Y", Date: pnr.FormatDate(dayAhead(40)), Board: "LHR", Off: "JFK", Seats: 2}},
		Agent:      "test", Channel: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := res.PNR
	if rec.Pricing == nil || rec.Pricing.Currency != "USD" || rec.Segments[0].FareBasis != "YOW" {
		t.Fatalf("record not priced: %+v", rec.Pricing)
	}
	// Two adults (titles do not make a child): 2 x (1200.00 + 94.00 + 250.00).
	if rec.Pricing.Total != 2*(120000+9400+25000) || len(rec.Pricing.Passengers) != 2 {
		t.Errorf("pricing: %+v", rec.Pricing)
	}
	// Inside 21 days the K fare cannot be sold; the booking is refused and says why.
	_, err = gds.gw.Book(ctx, &BookingRequest{
		Passengers: []BookingPassenger{{Surname: "LATE", Given: "BOB", Title: "MR"}},
		Segments:   []BookingSegment{{Carrier: "BA", FlightNum: "0175", Class: "K", Date: pnr.FormatDate(dayAhead(5)), Board: "LHR", Off: "JFK", Seats: 1}},
		Agent:      "test", Channel: "test",
	})
	var nf *fare.ErrNoFare
	if !errors.As(err, &nf) || nf.Reason != "21-day advance purchase" {
		t.Fatalf("a K sell five days out should fail the 21-day rule: %v", err)
	}
	// Tickets carry the value: the segment's base plus the passenger's taxes.
	ticketed, err := gds.gw.IssueTickets(ctx, rec.RecordLocator, IssueOptions{AirlineCode: "125", IssuedBy: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ticketed.Tickets) != 2 || ticketed.Tickets[0].Coupons[0].Amount != "1544.00" || ticketed.Tickets[0].Coupons[0].Currency != "USD" {
		t.Errorf("coupon value: %+v", ticketed.Tickets)
	}
}

func dayAhead(days int) time.Time { return time.Now().UTC().AddDate(0, 0, days) }
