// Package fare prices an itinerary: which published fare each segment is
// sold under, what the rules allow, what the taxes add, and what the
// passenger pays.
//
// A carrier files fares per market -- origin, destination, carrier -- one
// per booking class, each with a fare basis code, an amount and the rules
// that decide whether it may be sold for this trip: how far in advance,
// how long the stay, whether it can be refunded or changed and for how
// much. Distribution prices a booking against the filing before it sells,
// and the fare basis and the amount ride the record and the ticket. The
// industry's filings come from ATPCO and are licensed; this package is the
// structure they fill and the arithmetic that uses them, with the data
// supplied by whoever deploys it. It carries no fare of its own.
//
// Taxes are the other half of the price and are the reason a $99 fare is
// $131 at the checkout: a percentage of the base, a fixed amount per
// segment, per ticket, or per enplanement at an airport, filed by
// authority code the way the ticket prints them. The package computes
// them from rules; it does not know any country's rates.
package fare

import (
	"errors"
	"fmt"
	"sort"
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
type Rule struct {
	// AdvancePurchaseDays is the least number of days before departure the
	// fare may be sold: the 7 in a 7-day advance purchase fare.
	AdvancePurchaseDays int `json:"advance_purchase_days,omitempty"`
	// MinStayDays and MaxStayDays bound a round trip; a one-way ignores them.
	MinStayDays int `json:"min_stay_days,omitempty"`
	MaxStayDays int `json:"max_stay_days,omitempty"`
	// Refundable says whether the fare comes back on cancellation; ChangeFee
	// is what a change costs, zero being free.
	Refundable bool  `json:"refundable"`
	ChangeFee  Money `json:"change_fee,omitempty"`
	// SeasonFrom and SeasonTo bound the travel dates the fare applies to.
	SeasonFrom time.Time `json:"season_from,omitempty"`
	SeasonTo   time.Time `json:"season_to,omitempty"`
}

// Fare is one published fare: a market, a class, a basis, an amount, rules.
type Fare struct {
	Carrier     string `json:"carrier"`
	Origin      string `json:"origin"`
	Destination string `json:"destination"`
	Class       string `json:"class"`
	// Basis is the fare basis code the ticket prints: YOW, MHE7NR, KLX21.
	Basis string `json:"basis"`
	// OneWay is the one-way amount. Round-trip pricing here is two one-ways;
	// round-trip-only fares are a rule, not a second amount.
	OneWay Money `json:"one_way"`
	Rule   Rule  `json:"rule"`
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

// Filing is a Tariff held in memory, indexed on first use.
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
}

// Fares implements Tariff.
func (f *Filing) Fares(carrier, origin, destination string) []Fare {
	f.index()
	return f.byMarket[marketKey(carrier, origin, destination)]
}

// TaxesFor implements Tariff: this simple filing is one set of taxes for
// every carrier.
func (f *Filing) TaxesFor(carrier string) []Tax { return f.Levies }

// Segment is one flight of the itinerary to price.
type Segment struct {
	Carrier     string
	Origin      string
	Destination string
	Class       string
	Depart      time.Time
}

// Request is an itinerary and who is travelling on it.
type Request struct {
	Segments   []Segment
	Passengers []PaxType
	// Purchased is when the fare is being sold; advance purchase rules
	// count from it. Zero means now.
	Purchased time.Time
}

// SegmentFare is what one passenger pays for one segment.
type SegmentFare struct {
	Basis string `json:"basis"`
	Class string `json:"class"`
	Base  Money  `json:"base"`
}

// TaxLine is one tax on one passenger's ticket.
type TaxLine struct {
	Code   string `json:"code"`
	Amount Money  `json:"amount"`
}

// PassengerQuote is one passenger's price.
type PassengerQuote struct {
	Type     PaxType       `json:"type"`
	Segments []SegmentFare `json:"segments"`
	Base     Money         `json:"base"`
	Taxes    []TaxLine     `json:"taxes"`
	Total    Money         `json:"total"`
}

// Quote is the priced itinerary.
type Quote struct {
	Currency   string           `json:"currency"`
	Passengers []PassengerQuote `json:"passengers"`
	Base       Money            `json:"base"`
	Taxes      Money            `json:"taxes"`
	Total      Money            `json:"total"`
}

// ErrNoFare says a segment has no fare that may be sold in its class for
// this trip: the class is not filed on the market, or every fare in it
// fails a rule. The reason names which.
type ErrNoFare struct {
	Segment Segment
	Reason  string
}

func (e *ErrNoFare) Error() string {
	return fmt.Sprintf("fare: no %s fare for %s %s-%s: %s", e.Segment.Class, e.Segment.Carrier, e.Segment.Origin, e.Segment.Destination, e.Reason)
}

// Price prices a request against a tariff: for each segment the lowest
// fare filed in the booked class whose rules the trip meets, then the
// passenger-type discount, then the taxes.
func Price(t Tariff, req Request) (*Quote, error) {
	if t == nil {
		return nil, errors.New("fare: no tariff")
	}
	if len(req.Segments) == 0 || len(req.Passengers) == 0 {
		return nil, errors.New("fare: nothing to price")
	}
	purchased := req.Purchased
	if purchased.IsZero() {
		purchased = time.Now().UTC()
	}
	// A round trip's stay is the days between the first departure and the
	// last, when the itinerary comes back to where it started.
	stay := 0
	if n := len(req.Segments); n > 1 && strings.EqualFold(req.Segments[n-1].Destination, req.Segments[0].Origin) {
		stay = int(req.Segments[n-1].Depart.Sub(req.Segments[0].Depart).Hours() / 24)
	}
	chosen := make([]Fare, 0, len(req.Segments))
	for _, s := range req.Segments {
		fares := t.Fares(s.Carrier, s.Origin, s.Destination)
		var inClass []Fare
		for _, f := range fares {
			if strings.EqualFold(f.Class, s.Class) {
				inClass = append(inClass, f)
			}
		}
		if len(inClass) == 0 {
			return nil, &ErrNoFare{Segment: s, Reason: "class not filed on the market"}
		}
		sort.Slice(inClass, func(i, j int) bool { return inClass[i].OneWay.Amount < inClass[j].OneWay.Amount })
		var pick *Fare
		reason := ""
		for i := range inClass {
			f := &inClass[i]
			if why := f.Rule.check(s.Depart, purchased, stay); why != "" {
				// The rule that stopped the least restrictive fare is the one
				// worth telling: the cheaper fares failed harder.
				reason = why
				continue
			}
			pick = f
			break
		}
		if pick == nil {
			return nil, &ErrNoFare{Segment: s, Reason: reason}
		}
		chosen = append(chosen, *pick)
	}
	q := &Quote{Currency: chosen[0].OneWay.Currency}
	taxes := t.TaxesFor(req.Segments[0].Carrier)
	for _, pt := range req.Passengers {
		share, ok := Discount[pt]
		if !ok {
			share = 1
		}
		pq := PassengerQuote{Type: pt}
		for _, f := range chosen {
			base := f.OneWay.Scale(share)
			pq.Segments = append(pq.Segments, SegmentFare{Basis: f.Basis, Class: f.Class, Base: base})
			pq.Base = pq.Base.Add(base)
		}
		for _, tx := range taxes {
			if pt == Infant && !tx.Infants {
				continue
			}
			line := TaxLine{Code: tx.Code}
			switch tx.Kind {
			case PercentOfBase:
				line.Amount = pq.Base.Scale(tx.Percent / 100)
			case PerSegment:
				line.Amount = Money{Amount: tx.Amount.Amount * int64(len(req.Segments)), Currency: tx.Amount.Currency}
			case PerTicket:
				line.Amount = tx.Amount
			case PerEnplanement:
				n := 0
				for _, s := range req.Segments {
					if len(tx.Airports) == 0 || contains(tx.Airports, s.Origin) {
						n++
					}
				}
				line.Amount = Money{Amount: tx.Amount.Amount * int64(n), Currency: tx.Amount.Currency}
			}
			if line.Amount.Amount == 0 {
				continue
			}
			if line.Amount.Currency == "" {
				line.Amount.Currency = q.Currency
			}
			pq.Taxes = append(pq.Taxes, line)
			pq.Total = pq.Total.Add(line.Amount)
			q.Taxes = q.Taxes.Add(line.Amount)
		}
		pq.Total = pq.Total.Add(pq.Base)
		q.Base = q.Base.Add(pq.Base)
		q.Total = q.Total.Add(pq.Total)
		q.Passengers = append(q.Passengers, pq)
	}
	return q, nil
}

func (r Rule) check(depart, purchased time.Time, stayDays int) string {
	if r.AdvancePurchaseDays > 0 && depart.Sub(purchased) < time.Duration(r.AdvancePurchaseDays)*24*time.Hour {
		return fmt.Sprintf("%d-day advance purchase", r.AdvancePurchaseDays)
	}
	if !r.SeasonFrom.IsZero() && depart.Before(r.SeasonFrom) {
		return "outside season"
	}
	if !r.SeasonTo.IsZero() && depart.After(r.SeasonTo) {
		return "outside season"
	}
	if stayDays > 0 {
		if r.MinStayDays > 0 && stayDays < r.MinStayDays {
			return fmt.Sprintf("%d-day minimum stay", r.MinStayDays)
		}
		if r.MaxStayDays > 0 && stayDays > r.MaxStayDays {
			return fmt.Sprintf("%d-day maximum stay", r.MaxStayDays)
		}
	}
	return ""
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if strings.EqualFold(v, x) {
			return true
		}
	}
	return false
}
