package prorate

import "testing"

// Straight rate by mileage, by hand: a 1000-unit fare over 600 and 400
// kilometre sectors splits 600 to 400; three unequal sectors leave the
// rounding on the last coupon so the shares still add to the fare.
func TestStraightSplitsByDistanceAndAddsUp(t *testing.T) {
	shares, err := Straight(100000, []Coupon{{1, "BA", 600}, {2, "AA", 400}})
	if err != nil {
		t.Fatal(err)
	}
	if shares[0].Amount != 60000 || shares[1].Amount != 40000 || shares[0].Carrier != "BA" {
		t.Errorf("60/40: %+v", shares)
	}
	shares, _ = Straight(100000, []Coupon{{1, "BA", 1}, {2, "AA", 1}, {3, "DL", 1}})
	var sum int64
	for _, s := range shares {
		sum += s.Amount
	}
	if sum != 100000 || shares[0].Amount != 33333 || shares[2].Amount != 33334 {
		t.Errorf("thirds with the remainder last: %+v", shares)
	}
	shares, _ = Straight(50000, []Coupon{{1, "BA", 500}, {2, "BA", 0}})
	if shares[0].Amount != 50000 || shares[1].Amount != 0 {
		t.Errorf("a zero-distance coupon earns nothing: %+v", shares)
	}
	shares, _ = Straight(50000, []Coupon{{1, "BA", 0}, {2, "AA", 0}})
	if shares[0].Amount != 25000 || shares[1].Amount != 25000 {
		t.Errorf("no distances at all splits evenly: %+v", shares)
	}
	if _, err := Straight(1, nil); err == nil {
		t.Error("no coupons accepted")
	}
	if _, err := Straight(1, []Coupon{{1, "BA", -5}}); err == nil {
		t.Error("a negative distance accepted")
	}
}

func TestServiceChargeRounds(t *testing.T) {
	if got := ServiceCharge(60000, 900); got != 5400 {
		t.Errorf("9%% of 600.00 = %d", got)
	}
	if got := ServiceCharge(33333, 900); got != 3000 {
		t.Errorf("9%% of 333.33 rounds to %d, want 3000", got)
	}
	if ServiceCharge(60000, 0) != 0 || ServiceCharge(0, 900) != 0 {
		t.Error("no charge on nothing")
	}
	if got := ServiceCharge(-60000, 900); got != -5400 {
		t.Errorf("a refund's charge reverses: %d", got)
	}
}
