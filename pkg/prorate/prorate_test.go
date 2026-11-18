package prorate

import "testing"

// Straight rate by mileage, by hand: a 1000-unit fare over 600 and 400
// kilometre sectors splits 600 to 400; three unequal sectors leave the
// rounding on the last coupon so the shares still add to the fare.
func TestStraightSplitsByDistanceAndAddsUp(t *testing.T) {
	shares, err := Straight(100000, []Coupon{{Number: 1, Carrier: "BA", Distance: 600}, {Number: 2, Carrier: "AA", Distance: 400}})
	if err != nil {
		t.Fatal(err)
	}
	if shares[0].Amount != 60000 || shares[1].Amount != 40000 || shares[0].Carrier != "BA" {
		t.Errorf("60/40: %+v", shares)
	}
	shares, _ = Straight(100000, []Coupon{{Number: 1, Carrier: "BA", Distance: 1}, {Number: 2, Carrier: "AA", Distance: 1}, {Number: 3, Carrier: "DL", Distance: 1}})
	var sum int64
	for _, s := range shares {
		sum += s.Amount
	}
	if sum != 100000 || shares[0].Amount != 33333 || shares[2].Amount != 33334 {
		t.Errorf("thirds with the remainder last: %+v", shares)
	}
	shares, _ = Straight(50000, []Coupon{{Number: 1, Carrier: "BA", Distance: 500}, {Number: 2, Carrier: "BA", Distance: 0}})
	if shares[0].Amount != 50000 || shares[1].Amount != 0 {
		t.Errorf("a zero-distance coupon earns nothing: %+v", shares)
	}
	shares, _ = Straight(50000, []Coupon{{Number: 1, Carrier: "BA", Distance: 0}, {Number: 2, Carrier: "AA", Distance: 0}})
	if shares[0].Amount != 25000 || shares[1].Amount != 25000 {
		t.Errorf("no distances at all splits evenly: %+v", shares)
	}
	if _, err := Straight(1, nil); err == nil {
		t.Error("no coupons accepted")
	}
	if _, err := Straight(1, []Coupon{{Number: 1, Carrier: "BA", Distance: -5}}); err == nil {
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

// A through fare of 1,000 over a 900-mile feeder flown by A and a
// 3,500-mile trunk flown by B, ticketed by A. By straight rate the feeder
// is 900/4,400 of the fare, 204.54, and the trunk the rest, 795.46: the
// odd cent falls to the last coupon. A keeps a nine per cent service
// charge of 71.59, and B bills 723.87. A's own coupon is worth 204.54 to
// A and nobody bills it.
func TestDivideByStraightRateWithTheServiceCharge(t *testing.T) {
	d, err := Divide(100000, "A", []Coupon{{Number: 1, Carrier: "A", Distance: 900}, {Number: 2, Carrier: "B", Distance: 3500}}, nil, 900)
	if err != nil {
		t.Fatal(err)
	}
	a, b := d.Shares[0], d.Shares[1]
	if a.Value != 20454 || a.Charge != 0 || a.Net != 20454 {
		t.Errorf("the ticketing carrier's own coupon: %+v", a)
	}
	if b.Value != 79546 || b.Charge != 7159 || b.Net != 72387 || d.Residual != 0 {
		t.Errorf("the trunk: %+v residual %d", b, d.Residual)
	}
}

// Special prorate agreements. The feeder carrier has agreed a fixed 150
// for its sector whatever the fare: it takes that first and the trunk
// takes the rest. A codeshare partner takes 85 per cent of the fare and
// pays a five per cent commission; partners in a joint venture charge
// each other nothing; and when the agreed amounts exceed a low fare the
// ticketing carrier makes up the difference.
func TestDivideBySpecialAgreement(t *testing.T) {
	coupons := []Coupon{{Number: 1, Carrier: "F", Distance: 300, Local: 30000}, {Number: 2, Carrier: "T", Distance: 3500}}
	fixed := func(c string) *Terms {
		if c == "F" {
			return &Terms{Basis: Fixed, Amount: 15000, ServiceCharge: 500}
		}
		return nil
	}
	d, err := Divide(100000, "T", coupons, fixed, 900)
	if err != nil {
		t.Fatal(err)
	}
	if f, tr := d.Shares[0], d.Shares[1]; f.Value != 15000 || f.Charge != 750 || f.Net != 14250 || tr.Value != 85000 || tr.Charge != 0 {
		t.Errorf("a fixed amount for the feeder: %+v %+v", f, tr)
	}
	// The same feeder on a fare of 100: its 150 is more than the ticket
	// sold for, and the ticketing carrier is out 50 on top of flying the
	// trunk for nothing.
	d, _ = Divide(10000, "T", coupons, fixed, 900)
	if d.Shares[0].Value != 15000 || d.Shares[1].Value != 0 || d.Residual != -5000 {
		t.Errorf("a fixed amount against a low fare: %+v residual %d", d.Shares, d.Residual)
	}
	// A net local fare: 60 per cent of the feeder's own 300.
	local := func(c string) *Terms {
		if c == "F" {
			return &Terms{Basis: PercentOfLocal, Percent: 6000, ServiceCharge: -1}
		}
		return nil
	}
	d, _ = Divide(100000, "T", coupons, local, 900)
	if f := d.Shares[0]; f.Value != 18000 || f.Charge != 0 || f.Net != 18000 {
		t.Errorf("a share of the local fare, no service charge: %+v", f)
	}
	// A codeshare: the operating carrier flies the only coupon of a fare
	// the marketing carrier sold, takes the whole coupon value by straight
	// rate, and pays the marketing carrier a seven per cent commission.
	share := func(string) *Terms { return &Terms{ServiceCharge: 700} }
	d, _ = Divide(40000, "M", []Coupon{{Number: 1, Carrier: "O", Distance: 800}}, share, 900)
	if o := d.Shares[0]; o.Value != 40000 || o.Charge != 2800 || o.Net != 37200 {
		t.Errorf("a codeshare coupon: %+v", o)
	}
	// Straight rate with a floor: a 300-mile sector of a 3,800-mile fare
	// of 1,000 is worth 78.95, and the agreement says never less than 120.
	floor := func(c string) *Terms {
		if c == "F" {
			return &Terms{Floor: 12000}
		}
		return nil
	}
	d, _ = Divide(100000, "T", coupons, floor, 900)
	if f, tr := d.Shares[0], d.Shares[1]; f.Value != 12000 || tr.Value != 88000 || f.Value+tr.Value != 100000 {
		t.Errorf("straight rate with a minimum: %+v %+v", f, tr)
	}
}
