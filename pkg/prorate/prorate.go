// Package prorate divides a through fare between the coupons of an
// itinerary, which is what interline billing runs on: the carrier that
// flew a coupon on another carrier's ticket bills the ticket's carrier the
// coupon's share of the fare, less an interline service charge.
//
// The method here is straight rate proration by mileage: each coupon's
// share is its sector distance over the itinerary's total, the arithmetic
// the industry's prorate manuals begin from before their provisos (minima,
// prorate factors, special agreements). Method public knowledge; the
// Prorate Manual itself is sold and its provisos are not reproduced. The
// numbers -- distances, the service charge -- are the caller's.
package prorate

import "fmt"

// Coupon is one flight coupon to be given its share: the carrier that
// flew it and the sector's distance.
type Coupon struct {
	Number   int
	Carrier  string
	Distance int
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
