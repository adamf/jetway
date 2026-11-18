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

// The point of sale prices the booking. A fare filed for the carrier's
// own channels sells there and nowhere else, a contract fare sells to its
// account, a quote made while a fare could be sold is honoured when the
// booking arrives later, and the record keeps the fare calculation.
func TestBookingIsPricedForItsPointOfSale(t *testing.T) {
	ctx := context.Background()
	gds, _ := wire(t, "BA", store.FormatTypeB)
	usd := func(c int64) fare.Money { return fare.Money{Amount: c, Currency: "USD"} }
	gds.gw.Tariff = &fare.Filing{Filed: []fare.Fare{
		{Carrier: "BA", Origin: "LHR", Destination: "JFK", Class: "K", Basis: "KPUB", OneWay: usd(60000)},
		{Carrier: "BA", Origin: "LHR", Destination: "JFK", Class: "K", Basis: "KWEB", OneWay: usd(45000), Rule: fare.Rule{Sale: fare.SaleRule{Channels: []string{"direct"}}}},
		{Carrier: "BA", Origin: "LHR", Destination: "JFK", Class: "K", Basis: "KCORP", OneWay: usd(40000), Rule: fare.Rule{Sale: fare.SaleRule{Accounts: []string{"ACME"}}}},
		{Carrier: "BA", Origin: "LHR", Destination: "JFK", Class: "K", Basis: "KLX21", OneWay: usd(30000), Rule: fare.Rule{AdvancePurchaseDays: 21}},
	}}
	gds.gw.Miles = func(a, b string) int { return 3451 }
	book := func(surname string, days int, mut func(*BookingRequest)) (*pnr.PNR, error) {
		req := &BookingRequest{
			Passengers: []BookingPassenger{{Surname: surname, Given: "ANN", Title: "MS"}},
			Segments:   []BookingSegment{{Carrier: "BA", FlightNum: "0175", Class: "K", Date: pnr.FormatDate(dayAhead(days)), Board: "LHR", Off: "JFK", Seats: 1, DepartTime: "1100", ArriveTime: "1400"}},
			Agent:      "AGT1", Channel: "1A",
		}
		if mut != nil {
			mut(req)
		}
		res, err := gds.gw.Book(ctx, req)
		if err != nil {
			return nil, err
		}
		return res.PNR, nil
	}
	rec, err := book("AGENCY", 10, nil)
	if err != nil || rec.Segments[0].FareBasis != "KPUB" {
		t.Fatalf("through a distribution system the public fare: %v %v", rec, err)
	}
	if want := "LHR BA JFK 600.00KPUB USD600.00 END"; rec.Pricing.Calc != want || len(rec.Pricing.Components) != 1 || rec.Pricing.Components[0].Kind != "one way" {
		t.Errorf("the record keeps the fare calculation: %q %+v", rec.Pricing.Calc, rec.Pricing.Components)
	}
	rec, err = book("WEB", 10, func(r *BookingRequest) { r.Channel = "direct" })
	if err != nil || rec.Segments[0].FareBasis != "KWEB" {
		t.Errorf("on the carrier's own channel the web fare: %v %v", rec, err)
	}
	rec, err = book("CORP", 10, func(r *BookingRequest) { r.Account = "acme"; r.Country = "gb" })
	if err != nil || rec.Segments[0].FareBasis != "KCORP" || rec.Origin.Account != "ACME" || rec.Origin.Country != "GB" {
		t.Errorf("to the contract's account the contract fare: %v %v", rec, err)
	}
	// Ten days out the 21-day fare is closed. A quote given 15 days ago,
	// when the flight was 25 days off, still stands.
	quoted := time.Now().UTC().AddDate(0, 0, -15)
	rec, err = book("QUOTED", 10, func(r *BookingRequest) { r.PricedAt = quoted })
	if err != nil || rec.Segments[0].FareBasis != "KLX21" || !rec.Pricing.PricedAt.Equal(quoted) {
		t.Errorf("a quote from 25 days before departure: %v %v", rec, err)
	}
	// A node that honours a quote for a day prices an older one as of now.
	gds.gw.QuoteValidity = 24 * time.Hour
	rec, err = book("STALE", 10, func(r *BookingRequest) { r.PricedAt = quoted })
	if err != nil || rec.Segments[0].FareBasis != "KPUB" {
		t.Errorf("a stale quote is priced as of now: %v %v", rec, err)
	}
	if _, err := book("FUTURE", 10, func(r *BookingRequest) { r.PricedAt = time.Now().Add(48 * time.Hour) }); err == nil {
		t.Error("a quote dated in the future was accepted")
	}
}
