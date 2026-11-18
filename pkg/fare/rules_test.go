package fare

import (
	"errors"
	"math/rand"
	"testing"
	"time"
)

func one(t *testing.T, f Tariff, req Request) *Quote {
	t.Helper()
	if len(req.Passengers) == 0 {
		req.Passengers = []PaxType{Adult}
	}
	q, err := Price(f, req)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func refused(t *testing.T, f Tariff, req Request, reason string) {
	t.Helper()
	if len(req.Passengers) == 0 {
		req.Passengers = []PaxType{Adult}
	}
	var nf *ErrNoFare
	if _, err := Price(f, req); !errors.As(err, &nf) || nf.Reason != reason {
		t.Errorf("want a refusal for %q, got %v", reason, err)
	}
}

// A fare filed for the carrier's own channels is not sold through a
// distribution system; one filed for a contract is sold only to the
// account that holds it; a fare has first and last ticketing dates.
func TestSalesRestrictions(t *testing.T) {
	dep := day(2026, 3, 10, 9)
	f := &Filing{Filed: []Fare{
		{Carrier: "LH", Origin: "FRA", Destination: "LHR", Class: "K", Basis: "KPUB", OneWay: usd(12000)},
		{Carrier: "LH", Origin: "FRA", Destination: "LHR", Class: "K", Basis: "KWEB", OneWay: usd(9000), Rule: Rule{Sale: SaleRule{Channels: []string{"direct", "ndc"}}}},
		{Carrier: "LH", Origin: "FRA", Destination: "LHR", Class: "K", Basis: "KCORP", OneWay: usd(8000), Rule: Rule{Sale: SaleRule{Accounts: []string{"ACME"}}}},
		{Carrier: "LH", Origin: "FRA", Destination: "LHR", Class: "K", Basis: "KDE", OneWay: usd(7000), Rule: Rule{Sale: SaleRule{Countries: []string{"DE"}, From: day(2026, 1, 1, 0), To: day(2026, 1, 31, 0)}}},
		{Carrier: "LH", Origin: "FRA", Destination: "LHR", Class: "T", Basis: "TWEB", OneWay: usd(5000), Rule: Rule{Sale: SaleRule{Channels: []string{"direct"}}}},
	}}
	seg := []Segment{{Carrier: "LH", Origin: "FRA", Destination: "LHR", Class: "K", Depart: dep}}
	bought := day(2026, 2, 10, 9)
	if q := one(t, f, Request{Segments: seg, Purchased: bought, Sale: Sale{Channel: "1A", Country: "GB"}}); q.Components[0].Fare.Basis != "KPUB" {
		t.Errorf("through a distribution system: %s", q.Components[0].Fare.Basis)
	}
	if q := one(t, f, Request{Segments: seg, Purchased: bought, Sale: Sale{Channel: "direct"}}); q.Components[0].Fare.Basis != "KWEB" {
		t.Errorf("on the carrier's own site: %s", q.Components[0].Fare.Basis)
	}
	if q := one(t, f, Request{Segments: seg, Purchased: bought, Sale: Sale{Channel: "1A", Account: "acme"}}); q.Components[0].Fare.Basis != "KCORP" {
		t.Errorf("to the contract's account: %s", q.Components[0].Fare.Basis)
	}
	if q := one(t, f, Request{Segments: seg, Purchased: day(2026, 1, 15, 9), Sale: Sale{Channel: "1A", Country: "DE"}}); q.Components[0].Fare.Basis != "KDE" {
		t.Errorf("in Germany in January: %s", q.Components[0].Fare.Basis)
	}
	if q := one(t, f, Request{Segments: seg, Purchased: bought, Sale: Sale{Channel: "1A", Country: "DE"}}); q.Components[0].Fare.Basis != "KPUB" {
		t.Errorf("in Germany after the sale closed: %s", q.Components[0].Fare.Basis)
	}
	seg[0].Class = "T"
	refused(t, f, Request{Segments: seg, Purchased: bought, Sale: Sale{Channel: "1A"}}, "not sold through this channel")
}

// A through fare pays for a connection, not for a stay: a wait longer than
// the fare allows is a stopover, the through fare is not sold, and the
// journey is paid for with a fare for each flight when the classes have one.
func TestStopoverBreaksTheComponent(t *testing.T) {
	f := &Filing{Filed: []Fare{
		{Carrier: "AA", Origin: "BOS", Destination: "SFO", Class: "M", Basis: "MTHRU", OneWay: usd(30000), Rule: Rule{MaxStopHours: 4}, Routing: Routing{Via: []string{"DFW"}}},
		{Carrier: "AA", Origin: "BOS", Destination: "DFW", Class: "M", Basis: "MLOC1", OneWay: usd(22000)},
		{Carrier: "AA", Origin: "DFW", Destination: "SFO", Class: "M", Basis: "MLOC2", OneWay: usd(21000)},
	}}
	dep := day(2026, 3, 10, 9)
	segs := func(wait time.Duration) []Segment {
		return []Segment{
			{Carrier: "AA", Origin: "BOS", Destination: "DFW", Class: "M", Depart: dep, Arrive: dep.Add(4 * time.Hour), Miles: 1562},
			{Carrier: "AA", Origin: "DFW", Destination: "SFO", Class: "M", Depart: dep.Add(4*time.Hour + wait), Miles: 1464},
		}
	}
	q := one(t, f, Request{Segments: segs(90 * time.Minute), Purchased: dep.AddDate(0, 0, -5)})
	if len(q.Components) != 1 || q.Base.Amount != 30000 {
		t.Fatalf("a connection flies on the through fare: %+v", q.Components)
	}
	// The through fare is divided between the flights by their miles.
	p := q.Passengers[0].Segments
	if p[0].Base.Amount+p[1].Base.Amount != 30000 || p[0].Base.Amount != 30000*1562/3026+(30000-30000*1562/3026-30000*1464/3026) {
		t.Errorf("division by miles: %v and %v", p[0].Base, p[1].Base)
	}
	q = one(t, f, Request{Segments: segs(6 * time.Hour), Purchased: dep.AddDate(0, 0, -5)})
	if len(q.Components) != 2 || q.Base.Amount != 43000 {
		t.Errorf("a six-hour stop is a stopover; two local fares: %+v", q.Components)
	}
	only := &Filing{Filed: f.Filed[:1]}
	refused(t, only, Request{Segments: segs(6 * time.Hour), Purchased: dep.AddDate(0, 0, -5)}, "stopover at DFW not permitted")
}

// A partner's flight may ride a fare that names the partner, in the class
// the fare is booked in there; a fare that forbids it is not combined
// end on end with another unit; and an open jaw's gap may not be longer
// than the flights flown.
func TestPartnersCombinationsAndOpenJaws(t *testing.T) {
	dep := day(2026, 3, 10, 9)
	f := &Filing{Filed: []Fare{
		{Carrier: "UA", Origin: "DEN", Destination: "FRA", Class: "K", Basis: "KJOINT", OneWay: usd(60000), Directional: true,
			Rule: Rule{Carriers: []string{"LH"}, PartnerClass: map[string]string{"LH": "L"}}, Routing: Routing{Via: []string{"ORD"}}},
		{Carrier: "UA", Origin: "DEN", Destination: "ORD", Class: "K", Basis: "KUA", OneWay: usd(20000)},
		{Carrier: "LH", Origin: "ORD", Destination: "FRA", Class: "L", Basis: "LLH", OneWay: usd(70000)},
	}}
	via := []Segment{
		{Carrier: "UA", Origin: "DEN", Destination: "ORD", Class: "K", Depart: dep, Arrive: dep.Add(3 * time.Hour)},
		{Carrier: "LH", Origin: "ORD", Destination: "FRA", Class: "L", Depart: dep.Add(5 * time.Hour)},
	}
	// The joint fare is cheaper than the two carriers' own fares added up,
	// which is what an alliance is for.
	q := one(t, f, Request{Segments: via, Purchased: dep.AddDate(0, 0, -30)})
	if len(q.Components) != 1 || q.Components[0].Fare.Basis != "KJOINT" || q.Passengers[0].Segments[1].Class != "L" {
		t.Errorf("the joint fare covers the partner's flight in L: %+v", q.Components)
	}
	via[1].Class = "K"
	refused(t, f, Request{Segments: via, Purchased: dep.AddDate(0, 0, -30)}, "class not filed on the market")

	g := &Filing{Filed: []Fare{
		{Carrier: "BA", Origin: "LHR", Destination: "JFK", Class: "N", Basis: "NSALE", OneWay: usd(15000), Rule: Rule{NoEndOnEnd: true}},
		{Carrier: "BA", Origin: "LHR", Destination: "JFK", Class: "N", Basis: "NREG", OneWay: usd(25000)},
		{Carrier: "BA", Origin: "JFK", Destination: "BOS", Class: "N", Basis: "NADD", OneWay: usd(9000)},
		{Carrier: "BA", Origin: "LHR", Destination: "LAX", Class: "N", Basis: "NLAX", OneWay: usd(26000), Rule: Rule{}},
		{Carrier: "BA", Origin: "SFO", Destination: "LHR", Class: "N", Basis: "NSFO", OneWay: usd(26000), Rule: Rule{}},
		{Carrier: "BA", Origin: "MIA", Destination: "LHR", Class: "N", Basis: "NMIA", OneWay: usd(24000), Rule: Rule{}},
	}}
	// Alone the sale fare sells; with an onward fare on the ticket it
	// does not, and the regular fare does.
	lhrjfk := Segment{Carrier: "BA", Origin: "LHR", Destination: "JFK", Class: "N", Depart: dep}
	if q := one(t, g, Request{Segments: []Segment{lhrjfk}, Purchased: dep.AddDate(0, 0, -30)}); q.Base.Amount != 15000 {
		t.Errorf("the sale fare alone: %v", q.Base)
	}
	onward := Segment{Carrier: "BA", Origin: "JFK", Destination: "BOS", Class: "N", Depart: dep.AddDate(0, 0, 3)}
	if q := one(t, g, Request{Segments: []Segment{lhrjfk, onward}, Purchased: dep.AddDate(0, 0, -30)}); q.Base.Amount != 34000 {
		t.Errorf("end on end the regular fare applies: %v", q.Base)
	}
	// Out to Los Angeles, back from San Francisco: 337 miles apart, an
	// open jaw. Back from Miami, 2,340 miles from Los Angeles, with
	// 5,400-mile flights: still shorter than the flights. With a gap
	// longer than a flight it is two one ways.
	out := Segment{Carrier: "BA", Origin: "LHR", Destination: "LAX", Class: "N", Depart: dep, Miles: 5456}
	back := Segment{Carrier: "BA", Origin: "SFO", Destination: "LHR", Class: "N", Depart: dep.AddDate(0, 0, 9), Miles: 5367}
	dist := func(a, b string) int {
		if (a == "LAX" && b == "SFO") || (a == "SFO" && b == "LAX") {
			return 337
		}
		return 9000
	}
	q = one(t, g, Request{Segments: []Segment{out, back}, Purchased: dep.AddDate(0, 0, -30), Distance: dist})
	if len(q.Units) != 1 || q.Units[0].Kind != OpenJawUnit {
		t.Errorf("Los Angeles and San Francisco: %+v", q.Units)
	}
	if want := "LHR BA LAX 260.00NLAX /-SFO BA LHR 260.00NSFO USD520.00 END"; q.Calc != want {
		t.Errorf("fare calculation\n got %s\nwant %s", q.Calc, want)
	}
	back = Segment{Carrier: "BA", Origin: "MIA", Destination: "LHR", Class: "N", Depart: dep.AddDate(0, 0, 9), Miles: 4425}
	q = one(t, g, Request{Segments: []Segment{out, back}, Purchased: dep.AddDate(0, 0, -30), Distance: dist})
	if len(q.Units) != 2 || q.Units[0].Kind != OneWayUnit {
		t.Errorf("a gap longer than the flights is no open jaw: %+v", q.Units)
	}
}

type worldTaxes struct{ *Filing }

func (w worldTaxes) TaxesOn(segs []Segment) []Tax {
	if segs[0].Origin == "LHR" {
		return []Tax{{Code: "GB", Kind: PerTicket, Amount: usd(9400)}, {Code: "YQ", Kind: PerSegment, Amount: usd(15000), CarrierImposed: true, MinMiles: 3000}}
	}
	return w.Levies
}

// A carrier's own surcharge prints with the taxes and is counted apart;
// a tariff that knows its taxes by itinerary is asked for them; days,
// hours and blackout dates close a fare.
func TestCarrierFeesTaxesByItineraryAndDates(t *testing.T) {
	dep := day(2026, 7, 14, 21) // a Tuesday, 21:00
	f := worldTaxes{&Filing{Filed: []Fare{
		{Carrier: "BA", Origin: "LHR", Destination: "JFK", Class: "O", Basis: "ONIGHT", OneWay: usd(30000), Rule: Rule{DepartFrom: 20 * 60}},
		{Carrier: "BA", Origin: "LHR", Destination: "JFK", Class: "O", Basis: "ODAY", OneWay: usd(40000), Rule: Rule{Blackouts: []Period{{From: day(2026, 7, 20, 0), To: day(2026, 7, 31, 0)}}}},
		{Carrier: "BA", Origin: "LHR", Destination: "DUB", Class: "O", Basis: "ODUB", OneWay: usd(5000)},
	}, Levies: []Tax{{Code: "XX", Kind: PerTicket, Amount: usd(100)}}}}
	seg := Segment{Carrier: "BA", Origin: "LHR", Destination: "JFK", Class: "O", Depart: dep, Miles: 3451}
	q := one(t, f, Request{Segments: []Segment{seg}, Passengers: []PaxType{Adult, Infant}, Purchased: dep.AddDate(0, 0, -30)})
	if q.Components[0].Fare.Basis != "ONIGHT" || q.CarrierFees.Amount != 15000 || q.Taxes.Amount != 9400+15000 || q.Passengers[0].CarrierFees.Amount != 15000 {
		t.Errorf("night fare, departure tax and carrier fee: %+v", q)
	}
	seg.Depart = day(2026, 7, 14, 11)
	if q := one(t, f, Request{Segments: []Segment{seg}, Purchased: dep.AddDate(0, 0, -30)}); q.Components[0].Fare.Basis != "ODAY" {
		t.Errorf("at eleven in the morning the night fare is closed: %s", q.Components[0].Fare.Basis)
	}
	seg.Depart = day(2026, 7, 22, 11)
	refused(t, f, Request{Segments: []Segment{seg}, Purchased: dep.AddDate(0, 0, -30)}, "blackout date")
	// A short flight pays no long-haul fee.
	short := Segment{Carrier: "BA", Origin: "LHR", Destination: "DUB", Class: "O", Depart: dep, Miles: 279}
	if q := one(t, f, Request{Segments: []Segment{short}, Purchased: dep.AddDate(0, 0, -30)}); q.CarrierFees.Amount != 0 || q.Taxes.Amount != 9400 {
		t.Errorf("a short flight: %+v", q.Taxes)
	}
}

// Whatever the fares and the journey, a price that is quoted adds up:
// every segment is covered by exactly one component, the segment shares
// add to the components, and the components to the base.
func TestQuotesAddUp(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	cities := []string{"AAA", "BBB", "CCC", "DDD", "EEE"}
	classes := []string{"Y", "M", "K"}
	priced := 0
	for trial := 0; trial < 400; trial++ {
		f := &Filing{}
		for i := 0; i < 30; i++ {
			o, d := cities[rng.Intn(len(cities))], cities[rng.Intn(len(cities))]
			if o == d {
				continue
			}
			fr := Fare{Carrier: "XX", Origin: o, Destination: d, Class: classes[rng.Intn(3)], Basis: "B" + string(rune('A'+i)), OneWay: usd(int64(5000 + rng.Intn(40000))),
				RoundTrip: rng.Intn(4) == 0, Routing: Routing{Any: rng.Intn(2) == 0}}
			if rng.Intn(3) == 0 {
				fr.Rule.AdvancePurchaseDays = 7 * rng.Intn(4)
			}
			if rng.Intn(4) == 0 {
				fr.Rule.SaturdayNight = true
			}
			f.Filed = append(f.Filed, fr)
		}
		n := 1 + rng.Intn(6)
		at := cities[rng.Intn(len(cities))]
		dep := day(2026, 5, 4, 8)
		var segs []Segment
		for k := 0; k < n; k++ {
			to := cities[rng.Intn(len(cities))]
			for to == at {
				to = cities[rng.Intn(len(cities))]
			}
			segs = append(segs, Segment{Carrier: "XX", Origin: at, Destination: to, Depart: dep, Arrive: dep.Add(2 * time.Hour), Miles: 100 + rng.Intn(3000)})
			at = to
			dep = dep.Add(time.Duration(3+rng.Intn(100)) * time.Hour)
		}
		q, err := Price(f, Request{Segments: segs, Passengers: []PaxType{Adult}, Purchased: day(2026, 4, 20, 8), Open: func(int, string) bool { return true }})
		if err != nil {
			var nf *ErrNoFare
			if !errors.As(err, &nf) {
				t.Fatalf("trial %d: %v", trial, err)
			}
			continue
		}
		priced++
		covered := make([]int, n)
		var sum int64
		for _, c := range q.Components {
			var part int64
			for k := c.From; k <= c.To; k++ {
				covered[k]++
				part += q.Passengers[0].Segments[k].Base.Amount
			}
			if part != c.Amount.Amount {
				t.Fatalf("trial %d: component %+v, its segments add to %d", trial, c, part)
			}
			sum += c.Amount.Amount
		}
		for k, c := range covered {
			if c != 1 {
				t.Fatalf("trial %d: segment %d covered %d times", trial, k, c)
			}
		}
		if sum != q.Base.Amount {
			t.Fatalf("trial %d: components %d, base %v", trial, sum, q.Base)
		}
		inUnit := 0
		for _, u := range q.Units {
			inUnit += len(u.Components)
		}
		if inUnit != len(q.Components) {
			t.Fatalf("trial %d: %d components, %d in units", trial, len(q.Components), inUnit)
		}
	}
	if priced < 40 {
		t.Errorf("only %d of 400 random journeys priced; the test checks little", priced)
	}
}
