package fare

import (
	"errors"
	"testing"
	"time"
)

func filing() *Filing {
	usd := func(cents int64) Money { return Money{Amount: cents, Currency: "USD"} }
	return &Filing{
		Filed: []Fare{
			{Carrier: "WN", Origin: "BNA", Destination: "DCA", Class: "Y", Basis: "YOW", OneWay: usd(34900), Rule: Rule{Refundable: true}},
			{Carrier: "WN", Origin: "BNA", Destination: "DCA", Class: "M", Basis: "MHE7", OneWay: usd(18900), Rule: Rule{AdvancePurchaseDays: 7, ChangeFee: usd(7500)}},
			{Carrier: "WN", Origin: "BNA", Destination: "DCA", Class: "K", Basis: "KLX21", OneWay: usd(9900), Rule: Rule{AdvancePurchaseDays: 21, MinStayDays: 2}},
			{Carrier: "WN", Origin: "BNA", Destination: "DCA", Class: "K", Basis: "KLX14", OneWay: usd(12900), Rule: Rule{AdvancePurchaseDays: 14}},
			{Carrier: "WN", Origin: "DCA", Destination: "BNA", Class: "K", Basis: "KLX14", OneWay: usd(12900), Rule: Rule{AdvancePurchaseDays: 14}},
		},
		Levies: []Tax{
			{Code: "US", Kind: PercentOfBase, Percent: 7.5},
			{Code: "ZP", Kind: PerSegment, Amount: usd(450)},
			{Code: "AY", Kind: PerTicket, Amount: usd(560)},
			{Code: "XF", Kind: PerEnplanement, Amount: usd(450), Airports: []string{"BNA", "DCA"}},
		},
	}
}

// The lowest fare in the booked class whose rules the trip meets is the one
// sold; a child pays three quarters; taxes stack the way the ticket prints them.
func TestPriceChoosesTheCheapestRuleCompliantFare(t *testing.T) {
	dep := time.Date(2025, 11, 26, 8, 0, 0, 0, time.UTC)
	q, err := Price(filing(), Request{
		Segments:   []Segment{{Carrier: "WN", Origin: "BNA", Destination: "DCA", Class: "K", Depart: dep}},
		Passengers: []PaxType{Adult, Child},
		Purchased:  dep.AddDate(0, 0, -30),
	})
	if err != nil {
		t.Fatal(err)
	}
	// One way, so the 21-day fare's minimum stay does not apply and it wins.
	if q.Passengers[0].Segments[0].Basis != "KLX21" || q.Passengers[0].Base.Amount != 9900 {
		t.Errorf("adult fare: %+v", q.Passengers[0])
	}
	if q.Passengers[1].Base.Amount != 7425 {
		t.Errorf("child pays three quarters: %+v", q.Passengers[1].Base)
	}
	// Adult taxes: 7.5% of 99.00 = 7.43 (rounded), ZP 4.50, AY 5.60, XF 4.50.
	adult := q.Passengers[0]
	want := int64(743 + 450 + 560 + 450)
	if adult.Total.Amount != 9900+want || len(adult.Taxes) != 4 {
		t.Errorf("adult total %v taxes %+v; want base 99.00 + %d", adult.Total, adult.Taxes, want)
	}
	if q.Total.Amount != adult.Total.Amount+q.Passengers[1].Total.Amount || q.Currency != "USD" {
		t.Errorf("quote total: %+v", q)
	}
}

// Inside the advance-purchase window the cheap fares fall away one by one;
// when none in the class is left the sell is refused with the rule named.
func TestAdvancePurchaseClosesFares(t *testing.T) {
	dep := time.Date(2025, 11, 26, 8, 0, 0, 0, time.UTC)
	q, err := Price(filing(), Request{
		Segments:   []Segment{{Carrier: "WN", Origin: "BNA", Destination: "DCA", Class: "K", Depart: dep}},
		Passengers: []PaxType{Adult}, Purchased: dep.AddDate(0, 0, -15),
	})
	if err != nil || q.Passengers[0].Segments[0].Basis != "KLX14" {
		t.Fatalf("15 days out the 14-day fare should sell: %v %v", q, err)
	}
	_, err = Price(filing(), Request{
		Segments:   []Segment{{Carrier: "WN", Origin: "BNA", Destination: "DCA", Class: "K", Depart: dep}},
		Passengers: []PaxType{Adult}, Purchased: dep.AddDate(0, 0, -3),
	})
	var nf *ErrNoFare
	if !errors.As(err, &nf) || nf.Reason != "14-day advance purchase" {
		t.Fatalf("3 days out K should have no fare, with the rule named: %v", err)
	}
	if _, err := Price(filing(), Request{
		Segments:   []Segment{{Carrier: "WN", Origin: "BNA", Destination: "DCA", Class: "Y", Depart: dep}},
		Passengers: []PaxType{Adult}, Purchased: dep.Add(-2 * time.Hour),
	}); err != nil {
		t.Errorf("the full fare sells to the last minute: %v", err)
	}
	if _, err := Price(filing(), Request{
		Segments:   []Segment{{Carrier: "WN", Origin: "BNA", Destination: "DCA", Class: "Q", Depart: dep}},
		Passengers: []PaxType{Adult}}); !errors.As(err, &nf) || nf.Reason != "class not filed on the market" {
		t.Errorf("an unfiled class: %v", err)
	}
}

// A round trip's stay is checked against the fare's minimum, and an infant
// pays a tenth of the fare and only the taxes that apply to a lap child.
func TestRoundTripStayAndInfant(t *testing.T) {
	out := time.Date(2025, 11, 26, 8, 0, 0, 0, time.UTC)
	back := out.AddDate(0, 0, 1)
	segs := []Segment{
		{Carrier: "WN", Origin: "BNA", Destination: "DCA", Class: "K", Depart: out},
		{Carrier: "WN", Origin: "DCA", Destination: "BNA", Class: "K", Depart: back},
	}
	q, err := Price(filing(), Request{Segments: segs, Passengers: []PaxType{Adult, Infant}, Purchased: out.AddDate(0, 0, -40)})
	if err != nil {
		t.Fatal(err)
	}
	// One night's stay fails the 21-day fare's two-night minimum; the 14-day fare sells.
	if q.Passengers[0].Segments[0].Basis != "KLX14" {
		t.Errorf("a one-night stay should not get the two-night fare: %+v", q.Passengers[0].Segments)
	}
	inf := q.Passengers[1]
	if inf.Base.Amount != 2580 || len(inf.Taxes) != 0 {
		t.Errorf("infant: %+v", inf)
	}
	if q.Passengers[0].Taxes[1].Amount.Amount != 900 {
		t.Errorf("two segments, two ZP: %+v", q.Passengers[0].Taxes)
	}
}
