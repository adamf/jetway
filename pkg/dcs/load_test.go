package dcs

import (
	"strings"
	"testing"
	"time"
)

// boardedFlight builds a closed-shape flight of n boarded economy
// passengers with one bag each, on the given type.
func boardedFlight(t *testing.T, typ string, n int, bagKg int) (*AircraftType, *Flight) {
	t.Helper()
	at, ok := DefaultFleet().Type(typ)
	if !ok {
		t.Fatalf("no type %s", typ)
	}
	f := &Flight{Key: testKey, Carrier: "BA", Dest: "JFK", Registration: "GBZHA", Version: at.Version(), Crew: "2/6"}
	f.Cabin = at.Cabin.instance()
	tags := 0
	for i := 0; i < n; i++ {
		seats, err := f.Cabin.Assign("Y", 1)
		if err != nil {
			t.Fatalf("seat %d: %v", i, err)
		}
		p := &Passenger{ID: i + 1, Surname: "PAX", Given: "AMR", Compartment: "Y", Dest: "JFK", Type: PaxAdult, Status: StatusBoarded, Seat: seats[0]}
		_ = f.Cabin.Take(seats[0], p.ID)
		if bagKg > 0 {
			tags++
			p.Bags = []Bag{{Tag: "0125" + strings.Repeat("0", 5) + string(rune('0'+tags%10)), Weight: bagKg}}
		}
		f.Passengers = append(f.Passengers, p)
	}
	return at, f
}

func TestPlanBalancesANarrowBody(t *testing.T) {
	at, f := boardedFlight(t, "320", 150, 15)
	l := at.Plan(f, StandardWeights{}, FuelPlan{TakeOff: 9000, Trip: 6500}, 500, 100)
	if l.Adults != 150 || l.Bags != 150 || l.BagKilos != 2250 {
		t.Errorf("counts %+v", l)
	}
	if l.PaxWeight != 150*84 {
		t.Errorf("pax weight %d", l.PaxWeight)
	}
	if l.ZFW != 42600+150*84+2250+600 {
		t.Errorf("ZFW %d", l.ZFW)
	}
	if l.TOW != l.ZFW+9000 || l.LAW != l.TOW-6500 {
		t.Errorf("TOW %d LAW %d", l.TOW, l.LAW)
	}
	holds := 0
	for _, kg := range l.Holds {
		holds += kg
	}
	if holds != 2250+600 {
		t.Errorf("holds carry %d, load is %d", holds, 2250+600)
	}
	for _, c := range at.Compartments {
		if l.Holds[c.Name] > c.Max {
			t.Errorf("compartment %s over its limit: %d > %d", c.Name, l.Holds[c.Name], c.Max)
		}
	}
	if l.MACTOW < at.FwdMAC || l.MACTOW > at.AftMAC {
		t.Errorf("take-off CG %.1f%% MAC outside %v-%v", l.MACTOW, at.FwdMAC, at.AftMAC)
	}
	if len(l.Violations) != 0 {
		t.Errorf("violations: %v", l.Violations)
	}
	if l.Underload <= 0 {
		t.Errorf("underload %d for a light load", l.Underload)
	}
	if len(l.ULDs) != 0 {
		t.Error("an A320 has no containers")
	}
}

func TestPlanBuildsContainersOnAWideBody(t *testing.T) {
	at, f := boardedFlight(t, "77W", 300, 20)
	l := at.Plan(f, StandardWeights{}, FuelPlan{TakeOff: 90000, Trip: 75000}, 8000, 0)
	if len(l.ULDs) == 0 {
		t.Fatal("no ULDs built")
	}
	uldKg, uldBags := 0, 0
	for _, u := range l.ULDs {
		if u.Weight > at.Compartments[0].ULDMax {
			t.Errorf("ULD %s at %d kg over %d", u.ID, u.Weight, at.Compartments[0].ULDMax)
		}
		if !strings.HasSuffix(u.ID, "BA") || !strings.HasPrefix(u.ID, "AKE") {
			t.Errorf("ULD id %q", u.ID)
		}
		uldKg += u.Weight
		uldBags += u.Bags
	}
	if uldKg != 6000 {
		t.Errorf("containers hold %d kg of bags, want 6000", uldKg)
	}
	if uldBags != 300 {
		t.Errorf("containers account for %d bags, want 300", uldBags)
	}
	if len(l.Violations) != 0 {
		t.Errorf("violations: %v", l.Violations)
	}
	cpm := BuildCPM(f, l)
	if !strings.HasPrefix(cpm, "CPM\nBA0117/16.GBZHA.C48Y312\n-") {
		t.Errorf("CPM:\n%s", cpm)
	}
	m, err := ParseCPM(cpm)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Positions) < len(l.ULDs) || m.Version != "C48Y312" {
		t.Errorf("CPM parse: %d positions, version %q", len(m.Positions), m.Version)
	}
}

func TestPlanReportsAnOverload(t *testing.T) {
	at, f := boardedFlight(t, "AT7", 70, 30)
	// Absurd cargo for a turboprop.
	l := at.Plan(f, StandardWeights{}, FuelPlan{TakeOff: 2000, Trip: 1200}, 6000, 0)
	if len(l.Violations) == 0 {
		t.Fatal("6 tonnes of cargo on an ATR is legal?")
	}
	if l.Underload >= 0 {
		t.Errorf("underload %d on an overload", l.Underload)
	}
	ls := at.Loadsheet(f, l, time.Date(2026, 12, 16, 9, 30, 0, 0, time.UTC))
	if !strings.Contains(ls, "LOAD NOT LEGAL") {
		t.Errorf("loadsheet does not flag it:\n%s", ls)
	}
}

func TestLoadsheetLayout(t *testing.T) {
	at, f := boardedFlight(t, "320", 100, 12)
	l := at.Plan(f, StandardWeights{}, FuelPlan{TakeOff: 8000, Trip: 5000}, 0, 0)
	ls := at.Loadsheet(f, l, time.Date(2026, 12, 16, 9, 30, 0, 0, time.UTC))
	for _, want := range []string{
		"LOADSHEET", "LHR JFK  BA0117   GBZHA    Y180", "PASSENGER/CABIN BAG    100/0/0  TTL 100",
		"ZERO FUEL WEIGHT ACTUAL", "TAKE OFF WEIGHT ACTUAL", "LANDING WEIGHT ACTUAL",
		"DOI", "MACTOW", "UNDERLOAD BEFORE LMC", "SI NIL",
	} {
		if !strings.Contains(ls, want) {
			t.Errorf("loadsheet lacks %q:\n%s", want, ls)
		}
	}
}

func TestLDMBuildsAndParsesPublishedForms(t *testing.T) {
	at, f := boardedFlight(t, "320", 150, 15)
	l := at.Plan(f, StandardWeights{}, FuelPlan{TakeOff: 9000, Trip: 6500}, 500, 100)
	text := BuildLDM(f, l)
	if !strings.HasPrefix(text, "LDM\nBA0117/16.GBZHA.Y180.2/6\n-JFK.150/0/0.T2850.") {
		t.Errorf("LDM:\n%s", text)
	}
	m, err := ParseLDM(text)
	if err != nil {
		t.Fatal(err)
	}
	if m.Flight != "BA0117" || m.Registration != "GBZHA" || m.Version != "Y180" || m.Crew != "2/6" {
		t.Errorf("%+v", m)
	}
	d := m.Destinations[0]
	if d.Dest != "JFK" || d.Adults != 150 || d.Total != 2850 || len(d.Pax) != 1 || d.Pax[0] != 150 {
		t.Errorf("%+v", d)
	}
	sum := 0
	for _, kg := range d.Holds {
		sum += kg
	}
	if sum != 2850 {
		t.Errorf("holds %v sum %d", d.Holds, sum)
	}

	// The Avinor-published shape, with the house fields kept.
	m, err = ParseLDM("LDM\nVY5172/04.ECHQI.A320P.2/05\n-AMS.153/1/2.T1794.3/624.4/1170.PAX/154.PRF/0.DHC/0.B138/1794\nSI NIL")
	if err != nil {
		t.Fatal(err)
	}
	d = m.Destinations[0]
	if d.Adults != 153 || d.Children != 1 || d.Infants != 2 || d.Total != 1794 || d.Holds["3"] != 624 || d.Holds["4"] != 1170 {
		t.Errorf("%+v", d)
	}
	if len(d.Extra) != 3 || d.Extra[2] != "B138/1794" {
		t.Errorf("house fields not kept: %v", d.Extra)
	}
	// The older male/female/child/infant form.
	m, err = ParseLDM("LDM\nRAT0123/09.ECENZ.Y323.3/8\n-DUS.161/119/43/19.T9335.2/2105.4/5330.5/1900.PAX/323.PAD/0\nSI B/8775 C/1450")
	if err != nil {
		t.Fatal(err)
	}
	d = m.Destinations[0]
	if d.Adults != 280 || d.Children != 43 || d.Infants != 19 || d.Holds["5"] != 1900 || d.PAD[0] != 0 {
		t.Errorf("%+v", d)
	}
}

func TestParseCPMPublishedExample(t *testing.T) {
	m, err := ParseCPM(strings.Join([]string{
		"CPM",
		"RAT0123/02.ECENZ.31904H01",
		"-11L/PKC/IST/630/C",
		"-12L/AKH/IST/600/C",
		"-41L/AKH/IST/620/C",
		"-42L/AKH/IST/583/BC/BY0",
		"-43L/DZH/IST/96/E/BY",
		"-13L/N",
		"-5/IST/50/BY",
		"SI - TWO BABY-STROLLERS IN CPT 5",
	}, "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Flight != "RAT0123" || m.Day != "02" || m.Registration != "ECENZ" || m.Version != "31904H01" {
		t.Errorf("%+v", m)
	}
	if len(m.Positions) != 7 {
		t.Fatalf("%d positions", len(m.Positions))
	}
	if p := m.Positions[0]; p.Position != "11L" || p.ID != "PKC" || p.Dest != "IST" || p.Weight != 630 || p.Contents != "C" {
		t.Errorf("%+v", p)
	}
	if p := m.Positions[3]; p.Contents != "BC/BY0" {
		t.Errorf("extra contents lost: %+v", p)
	}
	if p := m.Positions[5]; p.Position != "13L" || p.Contents != "N" {
		t.Errorf("empty position: %+v", p)
	}
	if p := m.Positions[6]; p.Position != "5" || p.ID != "" || p.Weight != 50 || p.Contents != "BY" {
		t.Errorf("bulk: %+v", p)
	}
	if len(m.SI) != 1 || !strings.Contains(m.SI[0], "STROLLERS") {
		t.Errorf("SI %v", m.SI)
	}
}

func TestCabinLayoutsMatchTheirVersions(t *testing.T) {
	for code, want := range map[string]struct {
		seats   int
		version string
	}{
		"AT7": {70, "Y70"}, "320": {180, "Y180"}, "321": {220, "Y220"},
		"789": {290, "C32Y258"}, "77W": {360, "C48Y312"},
	} {
		at := DefaultFleet().Types[code]
		if at.Cabin.Seats() != want.seats || at.Version() != want.version {
			t.Errorf("%s: %d seats %s, want %d %s", code, at.Cabin.Seats(), at.Version(), want.seats, want.version)
		}
	}
}
