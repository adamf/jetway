// Package prorate divides a through fare between the coupons of an
// itinerary, which is what interline billing runs on: the carrier that
// flew a coupon on another carrier's ticket bills the ticket's carrier the
// coupon's share of the fare, less an interline service charge.
//
// The default method is straight rate proration by mileage: each coupon's
// share is its sector distance over the fare component's total, the
// arithmetic the industry's prorate manuals begin from before their
// provisos (minima, prorate factors). Two carriers may agree something
// else for each other's coupons -- a fixed amount, a share of the fare, a
// share of the local fare, a floor or a ceiling -- in a special prorate
// agreement or a codeshare agreement, and Divide applies those terms
// before it divides the rest. Method public knowledge; the Prorate Manual
// itself is sold and its factors and filed provisos are not reproduced.
// The numbers -- distances, the service charge, every agreed term -- are
// the caller's.
package prorate

import "fmt"

// Coupon is one flight coupon to be given its share: the carrier that
// flew it and the sector's distance.
type Coupon struct {
	Number   int
	Carrier  string
	Distance int
	// Local is the flying carrier's own fare for the sector, for an
	// agreement that values the coupon as a share of it.
	Local int64
}

// Share is a coupon's share of the fare, in the fare's minor units.
type Share struct {
	Coupon  int
	Carrier string
	Amount  int64
}

// Straight divides fare across the coupons by distance. Rounding falls to
// the last coupon so the shares add up to the fare exactly; a coupon of
// zero distance (a surface sector, a missing figure) gets nothing, and an
// itinerary of zero total distance gives every coupon an equal share.
func Straight(fare int64, coupons []Coupon) ([]Share, error) {
	if len(coupons) == 0 {
		return nil, fmt.Errorf("prorate: no coupons")
	}
	total := 0
	for _, c := range coupons {
		if c.Distance < 0 {
			return nil, fmt.Errorf("prorate: coupon %d has a negative distance", c.Number)
		}
		total += c.Distance
	}
	out := make([]Share, len(coupons))
	var given int64
	for i, c := range coupons {
		var amt int64
		if total == 0 {
			amt = fare / int64(len(coupons))
		} else {
			amt = fare * int64(c.Distance) / int64(total)
		}
		out[i] = Share{Coupon: c.Number, Carrier: c.Carrier, Amount: amt}
		given += amt
	}
	out[len(out)-1].Amount += fare - given
	return out, nil
}

// ServiceCharge is the interline service charge the billing carrier keeps
// back for the ticketing carrier's costs of sale, in hundredths of a
// percent of the prorated amount (900 is 9%), rounded to the nearest minor
// unit.
func ServiceCharge(amount int64, rateHundredths int) int64 {
	if rateHundredths <= 0 || amount == 0 {
		return 0
	}
	n := amount * int64(rateHundredths)
	if n >= 0 {
		return (n + 5000) / 10000
	}
	return -((-n + 5000) / 10000)
}

// Basis is how an agreement values a coupon.
type Basis string

const (
	// StraightRate is the multilateral default: the coupon's share of the
	// fare component by distance.
	StraightRate Basis = ""
	// Fixed is an agreed amount per coupon, the commonest special prorate
	// for a short feeder sector.
	Fixed Basis = "fixed"
	// PercentOfFare is an agreed share of the fare component.
	PercentOfFare Basis = "percent of fare"
	// PercentOfLocal is an agreed share of the flying carrier's own fare
	// for the sector, a net local fare.
	PercentOfLocal Basis = "percent of local"
)

// Terms is what two carriers have agreed for the coupons one flies on the
// other's tickets: a special prorate agreement, a codeshare agreement, or
// nothing, in which case the multilateral default applies -- straight
// rate and the default service charge. The forms are the ones such
// agreements commonly take; every number is the two carriers' own.
type Terms struct {
	Basis Basis
	// Amount is the Fixed amount per coupon, in the fare's minor units.
	Amount int64
	// Percent is the PercentOfFare or PercentOfLocal share, in hundredths
	// of a percent.
	Percent int
	// Floor and Ceiling bound what the coupon is worth under any basis; a
	// straight rate share with a floor is a prorate with a minimum. Zero
	// is no bound.
	Floor, Ceiling int64
	// ServiceCharge is what the ticketing carrier keeps of the coupon's
	// value for its cost of sale, in hundredths of a percent: the interline
	// service charge, or under a codeshare agreement the marketing
	// carrier's commission. Negative means none is charged, as between
	// the partners of a joint venture; zero takes the default given to
	// Divide.
	ServiceCharge int
}

// Division is a fare component divided between its coupons.
type Division struct {
	Shares []Settlement
	// Residual is what the coupons' values leave of the fare when every
	// coupon is valued by agreement and none by straight rate: it stays
	// with the ticketing carrier. It is negative when the agreed amounts
	// exceed the fare, which the ticketing carrier makes up -- the
	// dilution a carrier accepts when it sells a low fare over a partner
	// it owes a fixed amount.
	Residual int64
}

// Settlement is one coupon's value and what is charged against it.
type Settlement struct {
	Coupon  int
	Carrier string
	// Value is the coupon's prorate value. Charge is the service charge or
	// commission the ticketing carrier keeps. Net is what the flying
	// carrier bills: Value less Charge.
	Value, Charge, Net int64
}

// Divide values each coupon of one fare component. Coupons whose carrier
// has agreed a basis other than straight rate take their agreed value
// first; what is left of the fare is divided between the other coupons by
// distance. Each coupon then carries its service charge. A coupon flown
// by the ticketing carrier itself is valued and charged nothing: there
// is nobody to bill.
//
// terms answers for the carrier that flew a coupon, nil for no agreement;
// defaultCharge is the service charge without one, in hundredths of a
// percent. A fare is divided component by component, never across a whole
// ticket: a coupon belongs to the fare that paid for it.
func Divide(fare int64, ticketing string, coupons []Coupon, terms func(carrier string) *Terms, defaultCharge int) (*Division, error) {
	if len(coupons) == 0 {
		return nil, fmt.Errorf("prorate: no coupons")
	}
	values := make([]int64, len(coupons))
	special := make([]bool, len(coupons))
	agreed := make([]*Terms, len(coupons))
	left := fare
	for i, c := range coupons {
		if c.Distance < 0 {
			return nil, fmt.Errorf("prorate: coupon %d has a negative distance", c.Number)
		}
		if terms != nil && c.Carrier != ticketing {
			agreed[i] = terms(c.Carrier)
		}
		t := agreed[i]
		if t == nil || t.Basis == StraightRate {
			continue
		}
		special[i] = true
		switch t.Basis {
		case Fixed:
			values[i] = t.Amount
		case PercentOfFare:
			values[i] = scale(fare, t.Percent)
		case PercentOfLocal:
			values[i] = scale(c.Local, t.Percent)
		default:
			return nil, fmt.Errorf("prorate: coupon %d: basis %q", c.Number, t.Basis)
		}
		values[i] = bound(values[i], t)
		left -= values[i]
	}
	var rest []Coupon
	var at []int
	for i, c := range coupons {
		if !special[i] {
			rest = append(rest, c)
			at = append(at, i)
		}
	}
	d := &Division{}
	if len(rest) > 0 {
		pool := left
		if pool < 0 {
			pool = 0
		}
		shares, err := Straight(pool, rest)
		if err != nil {
			return nil, err
		}
		for k, s := range shares {
			values[at[k]] = s.Amount
		}
		// A floor or a ceiling on a straight rate share moves money to or
		// from the other straight rate coupons; with one carrier's bound
		// and one free coupon that is the free coupon's.
		for k := range rest {
			i := at[k]
			if t := agreed[i]; t != nil {
				if b := bound(values[i], t); b != values[i] {
					diff := b - values[i]
					values[i] = b
					for _, j := range at {
						if j != i && (agreed[j] == nil || (agreed[j].Floor == 0 && agreed[j].Ceiling == 0)) {
							values[j] -= diff
							diff = 0
							break
						}
					}
					d.Residual -= diff
				}
			}
		}
		d.Residual += left - pool
	} else {
		d.Residual = left
	}
	for i, c := range coupons {
		s := Settlement{Coupon: c.Number, Carrier: c.Carrier, Value: values[i]}
		if c.Carrier != ticketing {
			rate := defaultCharge
			if t := agreed[i]; t != nil && t.ServiceCharge != 0 {
				rate = t.ServiceCharge
			}
			if rate > 0 {
				s.Charge = ServiceCharge(values[i], rate)
			}
		}
		s.Net = s.Value - s.Charge
		d.Shares = append(d.Shares, s)
	}
	return d, nil
}

func scale(amount int64, hundredths int) int64 {
	n := amount * int64(hundredths)
	if n >= 0 {
		return (n + 5000) / 10000
	}
	return -((-n + 5000) / 10000)
}

func bound(v int64, t *Terms) int64 {
	if t.Floor > 0 && v < t.Floor {
		return t.Floor
	}
	if t.Ceiling > 0 && v > t.Ceiling {
		return t.Ceiling
	}
	return v
}
