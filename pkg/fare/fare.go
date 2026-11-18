// Package fare prices an itinerary: which published fares pay for its
// flights, what their rules allow, what the taxes add, and what the
// passenger pays.
//
// A carrier files fares per market -- origin, destination, carrier -- each
// with a booking class, a fare basis code, an amount and the rules that
// decide whether it may be sold for this trip. A fare pays for one or more
// consecutive flights: the fare and the flights it covers are a fare
// component, and every flight of a journey is covered by exactly one. Fare
// components are grouped into priceable units -- a one way, a round trip,
// an open jaw, a circle trip -- and the priceable unit is where the rules
// that fence a discount from the traveller who would have paid more are
// measured: how far ahead the trip was bought, how long the stay is,
// whether a Saturday night falls in it, which other fares it may be
// combined with. The same flights can be divided into components and
// units in many ways and the division changes what the rules say, so
// pricing is a search: the cheapest division whose every fare passes its
// rules. This is the model Carl de Marcken's "Computational Complexity of
// Air Travel Planning" (ITA Software, 2003) sets out, and the package's
// tests price that paper's worked examples.
//
// The rules are the ones the industry files by category with ATPCO:
// day and time, season, flight application, advance purchase, minimum and
// maximum stay, stopovers, combinations, blackouts, surcharges, sales
// restrictions and penalties. The category structure is public knowledge;
// the record layouts are ATPCO's and licensed, and this package does not
// reproduce them. It is the structure a filing fills and the search that
// uses it, with the data supplied by whoever deploys it. It carries no
// fare of its own. Not implemented, and said so where it matters: the
// mileage system and the IATA construction checks for international
// multi-stop journeys, stopovers for a charge, accompanied travel, and
// the back-to-back restriction.
//
// Taxes are the other half of the price and are the reason a $99 fare is
// $131 at the checkout: a percentage of the base, a fixed amount per
// segment, per ticket, or per enplanement at an airport, filed by
// authority code the way the ticket prints them. A carrier-imposed fee
// (YQ, YR) is computed the same way and kept apart, because it is the
// carrier's money and no government's. The package computes them from
// rules; it does not know any country's rates.
package fare

import (
	"fmt"
	"strings"
	"time"
)

// Money is an amount in minor units of a currency: 12345 USD is $123.45.
type Money struct {
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

func (m Money) String() string {
	return fmt.Sprintf("%s %d.%02d", m.Currency, m.Amount/100, m.Amount%100)
}

// Add returns the sum; currencies must agree.
func (m Money) Add(o Money) Money {
	if m.Currency == "" {
		m.Currency = o.Currency
	}
	return Money{Amount: m.Amount + o.Amount, Currency: m.Currency}
}

// Scale multiplies by a share, rounding to the minor unit.
func (m Money) Scale(share float64) Money {
	return Money{Amount: int64(float64(m.Amount)*share + 0.5), Currency: m.Currency}
}

// Rule is what a fare's conditions allow. Zero values are no restriction.
// The fields follow the categories fares are filed under; the comment on
// each names the category.
type Rule struct {
	// AdvancePurchaseDays is the least number of days before the first
	// flight of the priceable unit the fare may be sold: the 7 in a 7-day
	// advance purchase fare (category 5). A fare that pays for the return
	// of a trip is measured to the outbound, not to its own flight.
	AdvancePurchaseDays int `json:"advance_purchase_days,omitempty"`
	// MinStayDays and MaxStayDays bound the days between the first flight
	// of the unit's first component and the first flight of its last
	// (categories 6 and 7). SaturdayNight wants a Saturday night in that
	// time. A fare used one way has no stay of its own; it is then held
	// against the journey's return when the journey has one, so a round
	// trip cannot escape a minimum stay by being priced as two one ways.
	MinStayDays   int  `json:"min_stay_days,omitempty"`
	MaxStayDays   int  `json:"max_stay_days,omitempty"`
	SaturdayNight bool `json:"saturday_night,omitempty"`
	// Refundable says whether the fare comes back on cancellation;
	// ChangeFee is what a change costs, zero being free; NoChanges forbids
	// one (category 16).
	Refundable bool  `json:"refundable"`
	ChangeFee  Money `json:"change_fee,omitempty"`
	NoChanges  bool  `json:"no_changes,omitempty"`
	// SeasonFrom and SeasonTo bound the travel dates the fare applies to
	// (categories 3 and 14); Blackouts are dates inside the season it does
	// not (category 11). All are held against the component's first flight.
	SeasonFrom time.Time `json:"season_from,omitempty"`
	SeasonTo   time.Time `json:"season_to,omitempty"`
	Blackouts  []Period  `json:"blackouts,omitempty"`
	// Days are the days of the week the component's first flight may leave
	// on, and DepartFrom and DepartTo the minutes of the day, on the
	// departure's own clock (category 2). DepartTo zero is the day's end.
	Days       []time.Weekday `json:"days,omitempty"`
	DepartFrom int            `json:"depart_from,omitempty"`
	DepartTo   int            `json:"depart_to,omitempty"`
	// NonstopOnly holds the component to one flight; NoNonstop wants a
	// connection, the way the cheapest fares on business routes do.
	// Carriers are the other airlines whose flights the component may use
	// besides the fare's own, and PartnerClass the booking class the fare
	// is booked in on each, when it differs (category 4 and the booking
	// code exceptions).
	NonstopOnly  bool              `json:"nonstop_only,omitempty"`
	NoNonstop    bool              `json:"no_nonstop,omitempty"`
	Carriers     []string          `json:"carriers,omitempty"`
	PartnerClass map[string]string `json:"partner_class,omitempty"`
	// MaxStopHours is the longest a traveller may wait between two flights
	// of one component: longer is a stopover, and stopovers are not sold
	// (categories 8 and 9). Zero is 24 hours, the international
	// definition; fares within one country usually say 4.
	MaxStopHours int `json:"max_stop_hours,omitempty"`
	// Surcharges add to the fare when the component's first flight meets
	// their condition (category 12).
	Surcharges []Surcharge `json:"surcharges,omitempty"`
	// Sale restricts who may sell the fare, where, and when (categories 15
	// and 35).
	Sale SaleRule `json:"sale,omitempty"`
	// Combinations (category 10). UnitCarriers, when set, are the only
	// carriers besides the fare's own whose fares may share its priceable
	// unit; UnitSameBasis wants every fare of the unit to carry the same
	// basis code; NoEndOnEnd keeps the fare off a ticket that has another
	// priceable unit; NoOpenJaw keeps it out of an open jaw.
	UnitCarriers  []string `json:"unit_carriers,omitempty"`
	UnitSameBasis bool     `json:"unit_same_basis,omitempty"`
	NoEndOnEnd    bool     `json:"no_end_on_end,omitempty"`
	NoOpenJaw     bool     `json:"no_open_jaw,omitempty"`
}

// Period is a span of dates, both ends included.
type Period struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// Surcharge is an amount added to a fare component whose first flight
// leaves on one of the days named, or on any day when none is, from the
// origin named, or from either end when none is.
type Surcharge struct {
	Amount Money          `json:"amount"`
	Days   []time.Weekday `json:"days,omitempty"`
	From   string         `json:"from,omitempty"`
}

// SaleRule is who may sell a fare. Every list set must contain the sale's
// value; an empty list does not restrict. Accounts are the corporate or
// agency contracts a private fare is filed for.
type SaleRule struct {
	// From and To bound the dates the fare may be ticketed on.
	From      time.Time `json:"from,omitempty"`
	To        time.Time `json:"to,omitempty"`
	Countries []string  `json:"countries,omitempty"`
	Channels  []string  `json:"channels,omitempty"`
	Agencies  []string  `json:"agencies,omitempty"`
	Accounts  []string  `json:"accounts,omitempty"`
}

// Sale is the point of sale a request is priced for: the country, the
// channel (a distribution system's designator, "direct", "ndc"), the
// agency and the account code the seller holds.
type Sale struct {
	Country string `json:"country,omitempty"`
	Channel string `json:"channel,omitempty"`
	Agency  string `json:"agency,omitempty"`
	Account string `json:"account,omitempty"`
}

// Routing is the way a fare may be flown between its end points. Via are
// the points a connection may be made at; MaxVia is how many of them one
// component may use, one when zero. A fare with no routing may be flown
// nonstop only, unless Any says the route is free.
type Routing struct {
	Via    []string `json:"via,omitempty"`
	MaxVia int      `json:"max_via,omitempty"`
	Any    bool     `json:"any,omitempty"`
	// Excludes are pairs of points that may not both be used: Chicago or
	// Dallas, not both.
	Excludes [][2]string `json:"excludes,omitempty"`
	// MaxMiles, when set, bounds the miles flown in the component, the way
	// a maximum permitted mileage does. Segments that carry no mileage are
	// not counted.
	MaxMiles int `json:"max_miles,omitempty"`
}

// Fare is one published fare: a market, a class, a basis, an amount, rules.
type Fare struct {
	Carrier     string `json:"carrier"`
	Origin      string `json:"origin"`
	Destination string `json:"destination"`
	Class       string `json:"class"`
	// Basis is the fare basis code the ticket prints: YOW, MHE7NR, KLX21.
	Basis string `json:"basis"`
	// OneWay is what one fare component costs. A fare tagged RoundTrip is
	// half a round trip: it may not be sold in a one-way priceable unit,
	// so it takes a second fare and a return to use it.
	OneWay    Money `json:"one_way"`
	RoundTrip bool  `json:"round_trip,omitempty"`
	// Directional holds the fare to travel from Origin to Destination. A
	// fare is otherwise good in either direction, which is how most are
	// filed.
	Directional bool `json:"directional,omitempty"`
	Rule        Rule `json:"rule"`
	// Routing is how the market may be flown; the zero routing is nonstop.
	Routing Routing `json:"routing,omitempty"`
}

// TaxKind is how a tax is computed.
type TaxKind string

const (
	// PercentOfBase is a share of the base fare, like a domestic excise.
	PercentOfBase TaxKind = "percent"
	// PerSegment is a fixed amount for each flight segment.
	PerSegment TaxKind = "segment"
	// PerTicket is a fixed amount once per passenger.
	PerTicket TaxKind = "ticket"
	// PerEnplanement is a fixed amount per departure from an airport that
	// levies it, a passenger facility charge.
	PerEnplanement TaxKind = "enplanement"
)

// Tax is one authority's charge as the ticket prints it.
type Tax struct {
	Code string  `json:"code"` // the two-letter code on the ticket: US, ZP, AY, XF, YQ
	Kind TaxKind `json:"kind"`
	// Percent applies to PercentOfBase; Amount to the others.
	Percent float64 `json:"percent,omitempty"`
	Amount  Money   `json:"amount,omitempty"`
	// Airports restricts a PerEnplanement tax to departures from these.
	Airports []string `json:"airports,omitempty"`
	// Infants says whether a passenger without a seat pays it.
	Infants bool `json:"infants,omitempty"`
	// CarrierImposed marks a fee that is the carrier's own -- a fuel or
	// insurance surcharge, YQ or YR -- and not an authority's. It prints
	// with the taxes and is totalled apart, because it is revenue.
	CarrierImposed bool `json:"carrier_imposed,omitempty"`
	// MinMiles and MaxMiles hold a PerSegment charge to segments of a
	// length: a long-haul surcharge. Zero is no bound; a segment with no
	// mileage is charged.
	MinMiles int `json:"min_miles,omitempty"`
	MaxMiles int `json:"max_miles,omitempty"`
}

// Taxer is a Tariff that knows which taxes an itinerary pays from where
// it goes, not only from who sells it: a departure tax by country, a
// charge by airport. Price asks it instead of TaxesFor when the tariff
// implements it.
type Taxer interface {
	TaxesOn(segments []Segment) []Tax
}

// PaxType is the passenger type the fare is discounted for.
type PaxType string

const (
	Adult  PaxType = "ADT"
	Child  PaxType = "CHD"
	Infant PaxType = "INF" // no seat
)

// Discount is the share of the adult fare a type pays.
var Discount = map[PaxType]float64{Adult: 1, Child: 0.75, Infant: 0.10}

// Tariff answers the filing: the fares on a market and the taxes that
// apply to a carrier's ticket. Implementations index whatever they load.
type Tariff interface {
	Fares(carrier, origin, destination string) []Fare
	TaxesFor(carrier string) []Tax
}

// Filing is a Tariff held in memory, indexed on first use. Each fare
// sells in both directions of its market unless it is Directional.
type Filing struct {
	Filed  []Fare `json:"fares"`
	Levies []Tax  `json:"taxes"`

	byMarket map[string][]Fare
}

func marketKey(carrier, origin, destination string) string {
	return strings.ToUpper(carrier + "/" + origin + "/" + destination)
}

func (f *Filing) index() {
	if f.byMarket != nil {
		return
	}
	f.byMarket = map[string][]Fare{}
	for _, x := range f.Filed {
		k := marketKey(x.Carrier, x.Origin, x.Destination)
		f.byMarket[k] = append(f.byMarket[k], x)
	}
	// A fare not marked directional also sells the other way, unless the
	// filing already carries the same basis in that direction.
	for _, x := range f.Filed {
		if x.Directional || strings.EqualFold(x.Origin, x.Destination) {
			continue
		}
		k := marketKey(x.Carrier, x.Destination, x.Origin)
		dup := false
		for _, y := range f.byMarket[k] {
			if strings.EqualFold(y.Basis, x.Basis) && strings.EqualFold(y.Class, x.Class) {
				dup = true
				break
			}
		}
		if !dup {
			x.Origin, x.Destination = x.Destination, x.Origin
			f.byMarket[k] = append(f.byMarket[k], x)
		}
	}
}

// Fares implements Tariff.
func (f *Filing) Fares(carrier, origin, destination string) []Fare {
	f.index()
	return f.byMarket[marketKey(carrier, origin, destination)]
}

// TaxesFor implements Tariff: this simple filing is one set of taxes for
// every carrier.
func (f *Filing) TaxesFor(carrier string) []Tax { return f.Levies }
