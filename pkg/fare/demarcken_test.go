package fare

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// These tests price the worked examples of Carl de Marcken's
// "Computational Complexity of Air Travel Planning" (ITA Software, public
// notes, 2003): the fares, the flights and the answers are the paper's,
// written out here by hand from its slides, not built with this package.

func usd(cents int64) Money { return Money{Amount: cents, Currency: "USD"} }

func day(y int, m time.Month, d, h int) time.Time { return time.Date(y, m, d, h, 0, 0, 0, time.UTC) }

// Slides 10 and 11. American publishes three fares; the journey is San
// Francisco to Boston nonstop and back through Dallas.
//
//	AA BOS SFO Y     $1000
//	AA BOS DFW QE14  $500
//	AA DFW SFO K21AP $300
//
// "One way to pay for this journey is by using the Y to pay for the single
// outbound flight, and the Y again to pay for both return flights, for a
// total price of $2000." And "there is another valid way to pay for the
// same flights, using a BOS-DFW fare to pay for the first flight of the
// return and a DFW-SFO fare to pay for the second flight. In this case the
// total is only $1800."
func papersThreeFares() *Filing {
	return &Filing{Filed: []Fare{
		{Carrier: "AA", Origin: "BOS", Destination: "SFO", Class: "Y", Basis: "Y", OneWay: usd(100000), Rule: Rule{Refundable: true}, Routing: Routing{Via: []string{"DFW"}}},
		{Carrier: "AA", Origin: "BOS", Destination: "DFW", Class: "Q", Basis: "QE14", OneWay: usd(50000), Rule: Rule{AdvancePurchaseDays: 14}},
		{Carrier: "AA", Origin: "DFW", Destination: "SFO", Class: "K", Basis: "K21AP", OneWay: usd(30000), Rule: Rule{AdvancePurchaseDays: 21}},
	}}
}

func papersJourney(classes ...string) []Segment {
	return []Segment{
		{Carrier: "AA", Origin: "SFO", Destination: "BOS", Class: classes[0], Depart: day(2003, 10, 7, 8), Arrive: day(2003, 10, 7, 14)},
		{Carrier: "AA", Origin: "BOS", Destination: "DFW", Class: classes[1], Depart: day(2003, 10, 14, 8), Arrive: day(2003, 10, 14, 12)},
		{Carrier: "AA", Origin: "DFW", Destination: "SFO", Class: classes[2], Depart: day(2003, 10, 14, 13), Arrive: day(2003, 10, 14, 17)},
	}
}

func TestThePapersTwoWaysToPayForTheSameFlights(t *testing.T) {
	bought := day(2003, 9, 1, 12)
	// Booked in Y throughout: the Y fare pays for the outbound, and once
	// more for both flights of the return.
	q, err := Price(papersThreeFares(), Request{Segments: papersJourney("Y", "Y", "Y"), Passengers: []PaxType{Adult}, Purchased: bought})
	if err != nil {
		t.Fatal(err)
	}
	if q.Total.Amount != 200000 || len(q.Components) != 2 || q.Components[1].From != 1 || q.Components[1].To != 2 || q.Components[1].Fare.Basis != "Y" {
		t.Fatalf("two Y fares, the second covering both return flights, $2000: total %v components %+v", q.Total, q.Components)
	}
	if want := "SFO AA BOS 1000.00Y AA X/DFW AA SFO 1000.00Y USD2000.00 END"; q.Calc != want {
		t.Errorf("fare calculation\n got %s\nwant %s", q.Calc, want)
	}
	// Booked Y, Q and K: a fare per flight on the return.
	q, err = Price(papersThreeFares(), Request{Segments: papersJourney("Y", "Q", "K"), Passengers: []PaxType{Adult}, Purchased: bought})
	if err != nil {
		t.Fatal(err)
	}
	if q.Total.Amount != 180000 || len(q.Components) != 3 || q.Components[1].Fare.Basis != "QE14" || q.Components[2].Fare.Basis != "K21AP" {
		t.Fatalf("Y + QE14 + K21AP, $1800: total %v components %+v", q.Total, q.Components)
	}
	// Asked for the lowest fare with every class open, the search finds
	// the $1800 and says which classes to book.
	open := func(int, string) bool { return true }
	q, err = Price(papersThreeFares(), Request{Segments: papersJourney("", "", ""), Passengers: []PaxType{Adult}, Purchased: bought, Open: open})
	if err != nil {
		t.Fatal(err)
	}
	got := []string{q.Passengers[0].Segments[0].Class, q.Passengers[0].Segments[1].Class, q.Passengers[0].Segments[2].Class}
	if q.Total.Amount != 180000 || strings.Join(got, "") != "YQK" {
		t.Errorf("the lowest fare is $1800 in Y, Q, K: %v in %v", q.Total, got)
	}
	// With Q sold out on the Boston to Dallas flight only the $2000 is left.
	noQ := func(seg int, class string) bool { return !(seg == 1 && class == "Q") }
	q, err = Price(papersThreeFares(), Request{Segments: papersJourney("", "", ""), Passengers: []PaxType{Adult}, Purchased: bought, Open: noQ})
	if err != nil || q.Total.Amount != 200000 {
		t.Errorf("with Q closed the price is $2000: %v %v", q, err)
	}
	// Inside 21 days the K21AP fare is gone, and with it the $1800.
	q, err = Price(papersThreeFares(), Request{Segments: papersJourney("", "", ""), Passengers: []PaxType{Adult}, Purchased: day(2003, 9, 28, 12), Open: open})
	if err != nil || q.Total.Amount != 200000 {
		t.Errorf("15 days out the K21AP fare cannot be sold: %v %v", q, err)
	}
}

// Slide 19 onwards: "AA BOS-SFO H14ESNR $436.28". "The fare restricts the
// first flight in its fare component to be on Tuesday or Wednesday. It
// has a 14 day advance purchase restriction defined on the entire
// priceable unit... The fare, a round trip fare, prohibits use in one way
// PUs." Its routing "permits non-stop travel between BOS and SFO, but
// also permits stops in NYC, CHI, DFW and LAX (but not in both DFW and
// CHI). Thus, the price is the same, $436.28, whether one takes one
// flight or four". Elsewhere the paper gives its surcharges ($22.50 from
// Boston, $20 from San Francisco) and says every fare in its priceable
// unit must be on AA or TW. The S in the code is the Saturday night.
func h14esnr() Fare {
	return Fare{Carrier: "AA", Origin: "BOS", Destination: "SFO", Class: "H", Basis: "H14ESNR", OneWay: usd(43628), RoundTrip: true,
		Rule: Rule{AdvancePurchaseDays: 14, SaturdayNight: true, Days: []time.Weekday{time.Tuesday, time.Wednesday},
			Surcharges:   []Surcharge{{Amount: usd(2250), From: "BOS"}, {Amount: usd(2000), From: "SFO"}},
			UnitCarriers: []string{"TW"}},
		Routing: Routing{Via: []string{"NYC", "CHI", "DFW", "LAX"}, MaxVia: 3, Excludes: [][2]string{{"DFW", "CHI"}}}}
}

func TestH14ESNR(t *testing.T) {
	f := &Filing{Filed: []Fare{
		h14esnr(),
		{Carrier: "TW", Origin: "SFO", Destination: "BOS", Class: "H", Basis: "HTW", OneWay: usd(40000)},
		{Carrier: "UA", Origin: "SFO", Destination: "BOS", Class: "H", Basis: "HUA", OneWay: usd(40000)},
	}}
	tue, wed := day(2003, 10, 7, 9), day(2003, 10, 15, 9) // a Tuesday; the Wednesday of the week after
	bought := day(2003, 9, 20, 12)
	rt := func(out, back []Segment) Request {
		return Request{Segments: append(append([]Segment{}, out...), back...), Passengers: []PaxType{Adult}, Purchased: bought}
	}
	nonstopOut := []Segment{{Carrier: "AA", Origin: "BOS", Destination: "SFO", Class: "H", Depart: tue}}
	nonstopBack := []Segment{{Carrier: "AA", Origin: "SFO", Destination: "BOS", Class: "H", Depart: wed}}

	// The round trip: the fare each way and the surcharge of each direction.
	q, err := Price(f, rt(nonstopOut, nonstopBack))
	if err != nil {
		t.Fatal(err)
	}
	if q.Base.Amount != 43628+2250+43628+2000 || len(q.Units) != 1 || q.Units[0].Kind != RoundTripUnit {
		t.Fatalf("round trip on H14ESNR: base %v units %+v", q.Base, q.Units)
	}
	if want := "BOS AA SFO 436.28H14ESNR Q22.50 AA BOS 436.28H14ESNR Q20.00 USD915.06 END"; q.Calc != want {
		t.Errorf("fare calculation\n got %s\nwant %s", q.Calc, want)
	}

	// The same price by way of Chicago, or New York and Los Angeles.
	viaCHI := []Segment{
		{Carrier: "AA", Origin: "BOS", Destination: "CHI", Class: "H", Depart: tue, Arrive: tue.Add(3 * time.Hour)},
		{Carrier: "AA", Origin: "CHI", Destination: "SFO", Class: "H", Depart: tue.Add(4 * time.Hour)},
	}
	if q, err := Price(f, rt(viaCHI, nonstopBack)); err != nil || q.Base.Amount != 91506 || len(q.Components) != 2 {
		t.Errorf("by way of Chicago the price is the same: %v %v", q, err)
	}
	viaTwo := []Segment{
		{Carrier: "AA", Origin: "BOS", Destination: "NYC", Class: "H", Depart: tue, Arrive: tue.Add(1 * time.Hour)},
		{Carrier: "AA", Origin: "NYC", Destination: "LAX", Class: "H", Depart: tue.Add(2 * time.Hour), Arrive: tue.Add(8 * time.Hour)},
		{Carrier: "AA", Origin: "LAX", Destination: "SFO", Class: "H", Depart: tue.Add(9 * time.Hour)},
	}
	if q, err := Price(f, rt(viaTwo, nonstopBack)); err != nil || q.Base.Amount != 91506 {
		t.Errorf("three flights, same price: %v %v", q, err)
	}
	// But not Dallas and Chicago both.
	both := []Segment{
		{Carrier: "AA", Origin: "BOS", Destination: "CHI", Class: "H", Depart: tue, Arrive: tue.Add(3 * time.Hour)},
		{Carrier: "AA", Origin: "CHI", Destination: "DFW", Class: "H", Depart: tue.Add(4 * time.Hour), Arrive: tue.Add(6 * time.Hour)},
		{Carrier: "AA", Origin: "DFW", Destination: "SFO", Class: "H", Depart: tue.Add(7 * time.Hour)},
	}
	var nf *ErrNoFare
	if _, err := Price(f, rt(both, nonstopBack)); !errors.As(err, &nf) || !strings.Contains(nf.Reason, "not both") {
		t.Errorf("Dallas and Chicago together: %v", err)
	}
	// A city the routing does not name.
	viaDEN := []Segment{
		{Carrier: "AA", Origin: "BOS", Destination: "DEN", Class: "H", Depart: tue, Arrive: tue.Add(4 * time.Hour)},
		{Carrier: "AA", Origin: "DEN", Destination: "SFO", Class: "H", Depart: tue.Add(5 * time.Hour)},
	}
	if _, err := Price(f, rt(viaDEN, nonstopBack)); !errors.As(err, &nf) {
		t.Errorf("by way of Denver: %v", err)
	}

	// The first flight of the component on a Thursday.
	thu := []Segment{{Carrier: "AA", Origin: "BOS", Destination: "SFO", Class: "H", Depart: day(2003, 10, 9, 9)}}
	if _, err := Price(f, rt(thu, nonstopBack)); !errors.As(err, &nf) || nf.Reason != "day of week" {
		t.Errorf("a Thursday departure: %v", err)
	}
	// One way: a round trip fare prohibits use in a one-way unit.
	if _, err := Price(f, Request{Segments: nonstopOut, Passengers: []PaxType{Adult}, Purchased: bought}); !errors.As(err, &nf) || nf.Reason != "round trip required" {
		t.Errorf("one way on a round trip fare: %v", err)
	}
	// Back on the Wednesday of the same week: no Saturday night.
	sameWeek := []Segment{{Carrier: "AA", Origin: "SFO", Destination: "BOS", Class: "H", Depart: day(2003, 10, 8, 9)}}
	if _, err := Price(f, rt(nonstopOut, sameWeek)); !errors.As(err, &nf) || nf.Reason != "Saturday night stay required" {
		t.Errorf("no Saturday night: %v", err)
	}
	// Every fare in the unit on AA or TW: TWA's fare may bring the
	// traveller home, United's may not.
	tw := []Segment{{Carrier: "TW", Origin: "SFO", Destination: "BOS", Class: "H", Depart: wed}}
	if q, err := Price(f, rt(nonstopOut, tw)); err != nil || q.Base.Amount != 43628+2250+40000 {
		t.Errorf("home on TWA: %v %v", q, err)
	}
	ua := []Segment{{Carrier: "UA", Origin: "SFO", Destination: "BOS", Class: "H", Depart: wed}}
	if _, err := Price(f, rt(nonstopOut, ua)); !errors.As(err, &nf) || nf.Reason != "may not be combined with a UA fare" {
		t.Errorf("home on United: %v", err)
	}
}

// "If the fare is used to pay for the return portion of a trip, then even
// if the flights the fare is paying for leave 21 days after reservations
// the fare may not be usable, if the outbound portion of the trip took
// place only 7 days after reservation."
func TestAdvancePurchaseIsMeasuredToTheUnitsFirstFlight(t *testing.T) {
	f := &Filing{Filed: []Fare{h14esnr()}}
	bought := day(2003, 9, 30, 9)
	out := Segment{Carrier: "AA", Origin: "BOS", Destination: "SFO", Class: "H", Depart: day(2003, 10, 7, 9)}   // Tuesday, 7 days after
	back := Segment{Carrier: "AA", Origin: "SFO", Destination: "BOS", Class: "H", Depart: day(2003, 10, 21, 9)} // Tuesday, 21 days after
	var nf *ErrNoFare
	if _, err := Price(f, Request{Segments: []Segment{out, back}, Passengers: []PaxType{Adult}, Purchased: bought}); !errors.As(err, &nf) || nf.Reason != "14-day advance purchase" {
		t.Fatalf("a return 21 days out on a trip that starts 7 days out: %v", err)
	}
	// The same return with the outbound a week later is 14 days out at its
	// first flight, and sells.
	out.Depart = day(2003, 10, 14, 9)
	if _, err := Price(f, Request{Segments: []Segment{out, back}, Passengers: []PaxType{Adult}, Purchased: bought}); err != nil {
		t.Errorf("14 days to the unit's first flight: %v", err)
	}
}

// Slides 13 to 15. Four flights, San Francisco, Denver, Boston, Dallas and
// home, one fare for each; "the third fare, a BOS-DFW QE14 fare, has a
// 14-day advance purchase restriction and a Saturday night stay
// restriction". As one circle trip the advance purchase runs to the
// departure from San Francisco and the Saturday night from there to the
// departure from Dallas. As two open jaws -- San Francisco to Denver with
// Dallas to San Francisco, and Denver to Boston with Boston to Dallas --
// the advance purchase runs to the later departure from Denver and the
// Saturday night from Denver to the departure from Boston. "It is
// important how fare components are grouped into priceable units."
func fourFlights(sfo, den, bos, dfw time.Time) []Segment {
	return []Segment{
		{Carrier: "AA", Origin: "SFO", Destination: "DEN", Class: "M", Depart: sfo, Miles: 967},
		{Carrier: "AA", Origin: "DEN", Destination: "BOS", Class: "M", Depart: den, Miles: 1754},
		{Carrier: "AA", Origin: "BOS", Destination: "DFW", Class: "Q", Depart: bos, Miles: 1562},
		{Carrier: "AA", Origin: "DFW", Destination: "SFO", Class: "M", Depart: dfw, Miles: 1464},
	}
}

// gaps is the great-circle miles across each open jaw the four flights
// could make. Denver to Dallas is shorter than any of the flights, so the
// paper's two open jaws are open jaws; San Francisco to Boston is longer
// than the flight to Denver, so that pair is not.
func gaps(a, b string) int {
	switch a + b {
	case "DENDFW", "DFWDEN":
		return 641
	case "SFOBOS", "BOSSFO":
		return 2704
	}
	return 99999
}

func fourFares() *Filing {
	rt := func(o, d, class, basis string, r Rule) Fare {
		return Fare{Carrier: "AA", Origin: o, Destination: d, Class: class, Basis: basis, OneWay: usd(20000), RoundTrip: true, Directional: true, Rule: r}
	}
	return &Filing{Filed: []Fare{
		rt("SFO", "DEN", "M", "M1", Rule{}),
		rt("DEN", "BOS", "M", "M2", Rule{}),
		rt("BOS", "DFW", "Q", "QE14", Rule{AdvancePurchaseDays: 14, SaturdayNight: true}),
		rt("DFW", "SFO", "M", "M4", Rule{}),
	}}
}

// unitOf is the indices of the segments priced together with segment seg.
func unitOf(q *Quote, seg int) []int {
	for _, u := range q.Units {
		var segs []int
		has := false
		for _, ci := range u.Components {
			c := q.Components[ci]
			segs = append(segs, c.From)
			if c.From <= seg && seg <= c.To {
				has = true
			}
		}
		if has {
			return segs
		}
	}
	return nil
}

func TestTheDivisionIntoUnitsChangesWhatTheRulesSay(t *testing.T) {
	// Bought 13 days before the departure from San Francisco (a Monday)
	// and 15 before the departure from Denver (the Wednesday). The
	// traveller leaves Boston the next Monday and Dallas the same day.
	// As a circle trip the advance purchase fails by a day. As the paper's
	// two open jaws it is met from Denver, and the Saturday night falls
	// between Denver and Boston.
	sfo, den, bos, dfw := day(2003, 10, 6, 8), day(2003, 10, 8, 8), day(2003, 10, 13, 8), day(2003, 10, 13, 14)
	q, err := Price(fourFares(), Request{Segments: fourFlights(sfo, den, bos, dfw), Passengers: []PaxType{Adult}, Purchased: day(2003, 9, 23, 8), Distance: gaps})
	if err != nil {
		t.Fatalf("the two open jaws sell where the circle trip does not: %v", err)
	}
	if len(q.Units) != 2 || q.Units[0].Kind != OpenJawUnit || q.Units[1].Kind != OpenJawUnit {
		t.Fatalf("two open jaws: %+v", q.Units)
	}
	if with := unitOf(q, 2); len(with) != 2 || with[0] != 1 || with[1] != 2 {
		t.Errorf("QE14 is priced with the Denver to Boston fare: segments %v", with)
	}
	if q.Base.Amount != 80000 {
		t.Errorf("four fares of $200: %v", q.Base)
	}

	// Now bought long before, with the traveller leaving Boston on the
	// Friday of the first week and Dallas the Monday after. Denver to
	// Boston holds no Saturday night, so the two open jaws fail the stay.
	// As a circle trip it is measured from San Francisco to the departure
	// from Dallas, and met: "the circle-trip choice is advantageous".
	bos, dfw = day(2003, 10, 10, 8), day(2003, 10, 13, 8)
	q, err = Price(fourFares(), Request{Segments: fourFlights(sfo, den, bos, dfw), Passengers: []PaxType{Adult}, Purchased: day(2003, 8, 1, 8), Distance: gaps})
	if err != nil {
		t.Fatalf("the Saturday night is met to the Dallas departure: %v", err)
	}
	if len(q.Units) != 1 || q.Units[0].Kind != CircleUnit || len(q.Units[0].Components) != 4 {
		t.Errorf("one circle trip of four fares: %+v", q.Units)
	}
	// And with no Saturday night anywhere in the trip nothing sells.
	bos, dfw = day(2003, 10, 9, 8), day(2003, 10, 10, 8)
	var nf *ErrNoFare
	if _, err := Price(fourFares(), Request{Segments: fourFlights(sfo, den, bos, dfw), Passengers: []PaxType{Adult}, Purchased: day(2003, 8, 1, 8), Distance: gaps}); !errors.As(err, &nf) || nf.Reason != "Saturday night stay required" {
		t.Errorf("home before the weekend: %v", err)
	}
}
