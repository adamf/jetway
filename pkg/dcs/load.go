package dcs

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// StandardWeights are the passenger weights load control uses when nobody
// weighs the passengers, which is always. Zero fields take the defaults.
//
// The defaults are the EASA all-adult standard (84 kg including hand
// baggage), 35 kg for a child and 10 kg for an infant. Operators with
// approved survey weights set their own.
type StandardWeights struct {
	Adult  int `json:"adult"`
	Child  int `json:"child"`
	Infant int `json:"infant"`
}

func (w StandardWeights) filled() StandardWeights {
	if w.Adult == 0 {
		w.Adult = 84
	}
	if w.Child == 0 {
		w.Child = 35
	}
	if w.Infant == 0 {
		w.Infant = 10
	}
	return w
}

// Compartment is one hold or hold section.
type Compartment struct {
	// Name is the AHM compartment number as the LDM carries it: 1, 3, 4, 5.
	Name string `json:"name"`
	// Max is the structural limit in kilos.
	Max int `json:"max"`
	// Arm is the compartment's balance arm in metres from the datum.
	Arm float64 `json:"arm"`
	// ULD says the compartment takes containers at Positions; otherwise it
	// is bulk-loaded.
	ULD       bool     `json:"uld,omitempty"`
	Positions []string `json:"positions,omitempty"`
	// ULDMax is the gross weight limit of one container in this compartment.
	ULDMax int `json:"uld_max,omitempty"`
}

// Zone is a cabin section for balance: rows sharing one arm.
type Zone struct {
	Name    string  `json:"name"`
	FromRow int     `json:"from_row"`
	ToRow   int     `json:"to_row"`
	Arm     float64 `json:"arm"`
}

// AircraftType is the weight and balance data for one type, in the shape
// an AHM 560 exchange carries it: weights, arms, limits, index constants.
type AircraftType struct {
	Code  string      `json:"code"`
	Name  string      `json:"name"`
	Cabin CabinLayout `json:"cabin"`

	// DOW is the dry operating weight; DOWArm its balance arm.
	DOW    int     `json:"dow"`
	DOWArm float64 `json:"dow_arm"`
	MZFW   int     `json:"mzfw"`
	MTOW   int     `json:"mtow"`
	MLW    int     `json:"mlw"`

	// Index constants: I = W*(arm-RefArm)/C + K. LEMAC and MAC convert an
	// arm to %MAC.
	RefArm float64 `json:"ref_arm"`
	C      float64 `json:"c"`
	K      float64 `json:"k"`
	LEMAC  float64 `json:"lemac"`
	MAC    float64 `json:"mac"`
	// FwdMAC and AftMAC are the take-off envelope, %MAC. A single envelope
	// stands in for the weight-dependent one a real AHM 560 carries.
	FwdMAC float64 `json:"fwd_mac"`
	AftMAC float64 `json:"aft_mac"`
	// FuelArm is where fuel sits, for the take-off index.
	FuelArm float64 `json:"fuel_arm"`

	Compartments []Compartment `json:"compartments"`
	Zones        []Zone        `json:"zones"`
}

// Version renders the cabin configuration.
func (t *AircraftType) Version() string { return t.Cabin.Version() }

func (t *AircraftType) hasULDs() bool {
	for _, c := range t.Compartments {
		if c.ULD {
			return true
		}
	}
	return false
}

// FleetData is the aircraft types a station knows.
type FleetData struct {
	Types       map[string]*AircraftType `json:"types"`
	DefaultType string                   `json:"default_type"`
}

// Type looks up an aircraft type by code.
func (f *FleetData) Type(code string) (*AircraftType, bool) {
	t, ok := f.Types[code]
	return t, ok
}

// Default is the type used when a flight's equipment is unknown.
func (f *FleetData) Default() *AircraftType {
	if t, ok := f.Types[f.DefaultType]; ok {
		return t
	}
	for _, t := range f.Types {
		return t
	}
	return &AircraftType{Code: "UNK", Cabin: CabinLayout{Sections: []Section{{Compartment: "Y", FromRow: 1, ToRow: 30, Letters: "ABC DEF"}}}}
}

// DefaultFleet returns representative data for five type classes.
//
// Every figure here is representative of the type, rounded from public
// type-certificate-level values: it is not any operator's AHM 560 and no
// aircraft should be loaded from it. It exists so the package computes a
// coherent loadsheet out of the box; an operator replaces it with their own.
func DefaultFleet() *FleetData {
	types := []*AircraftType{
		{
			Code: "AT7", Name: "ATR 72",
			Cabin: CabinLayout{Sections: []Section{
				{Compartment: "Y", FromRow: 1, ToRow: 17, Letters: "AC DF"},
				{Compartment: "Y", FromRow: 18, ToRow: 18, Letters: "AC"},
			}},
			DOW: 13500, DOWArm: 12.10, MZFW: 20800, MTOW: 23000, MLW: 22350,
			RefArm: 12.05, C: 500, K: 50, LEMAC: 11.47, MAC: 2.30, FwdMAC: 18, AftMAC: 37, FuelArm: 12.3,
			Compartments: []Compartment{
				{Name: "1", Max: 930, Arm: 6.9},
				{Name: "2", Max: 400, Arm: 21.5},
			},
			Zones: []Zone{{Name: "OA", FromRow: 1, ToRow: 6, Arm: 9.5}, {Name: "OB", FromRow: 7, ToRow: 12, Arm: 13.2}, {Name: "OC", FromRow: 13, ToRow: 18, Arm: 16.9}},
		},
		{
			Code: "320", Name: "Airbus A320",
			Cabin: CabinLayout{Sections: []Section{
				{Compartment: "Y", FromRow: 1, ToRow: 30, Letters: "ABC DEF"},
			}},
			DOW: 42600, DOWArm: 19.20, MZFW: 62500, MTOW: 78000, MLW: 66000,
			RefArm: 18.85, C: 1000, K: 50, LEMAC: 17.80, MAC: 4.19, FwdMAC: 17, AftMAC: 37, FuelArm: 18.9,
			Compartments: []Compartment{
				{Name: "1", Max: 3402, Arm: 12.4},
				{Name: "3", Max: 2426, Arm: 26.0},
				{Name: "4", Max: 2110, Arm: 29.3},
				{Name: "5", Max: 1497, Arm: 31.2},
			},
			Zones: []Zone{{Name: "OA", FromRow: 1, ToRow: 10, Arm: 13.5}, {Name: "OB", FromRow: 11, ToRow: 20, Arm: 19.0}, {Name: "OC", FromRow: 21, ToRow: 30, Arm: 24.8}},
		},
		{
			Code: "321", Name: "Airbus A321",
			Cabin: CabinLayout{Sections: []Section{
				{Compartment: "Y", FromRow: 1, ToRow: 36, Letters: "ABC DEF"},
				{Compartment: "Y", FromRow: 37, ToRow: 37, Letters: "AC DF"},
			}},
			DOW: 48500, DOWArm: 21.60, MZFW: 73800, MTOW: 93500, MLW: 77800,
			RefArm: 21.20, C: 1000, K: 50, LEMAC: 20.15, MAC: 4.19, FwdMAC: 17, AftMAC: 37, FuelArm: 21.2,
			Compartments: []Compartment{
				{Name: "1", Max: 4536, Arm: 13.6},
				{Name: "3", Max: 3000, Arm: 29.0},
				{Name: "4", Max: 2500, Arm: 32.4},
				{Name: "5", Max: 1497, Arm: 34.6},
			},
			Zones: []Zone{{Name: "OA", FromRow: 1, ToRow: 12, Arm: 14.4}, {Name: "OB", FromRow: 13, ToRow: 24, Arm: 21.2}, {Name: "OC", FromRow: 25, ToRow: 37, Arm: 28.5}},
		},
		{
			Code: "789", Name: "Boeing 787-9",
			Cabin: CabinLayout{Sections: []Section{
				{Compartment: "C", FromRow: 1, ToRow: 8, Letters: "A DG K"},
				{Compartment: "Y", FromRow: 10, ToRow: 37, Letters: "ABC DEF HJK"},
				{Compartment: "Y", FromRow: 38, ToRow: 38, Letters: "ABC HJK"},
			}},
			DOW: 129000, DOWArm: 30.40, MZFW: 192777, MTOW: 254011, MLW: 192777,
			RefArm: 30.10, C: 5000, K: 100, LEMAC: 28.35, MAC: 7.00, FwdMAC: 15, AftMAC: 40, FuelArm: 30.0,
			Compartments: []Compartment{
				{Name: "1", Max: 18000, Arm: 20.5, ULD: true, ULDMax: 1588,
					Positions: []string{"11L", "11R", "12L", "12R", "13L", "13R", "14L", "14R", "21L", "21R", "22L", "22R", "23L", "23R", "24L", "24R"}},
				{Name: "3", Max: 20000, Arm: 40.6, ULD: true, ULDMax: 1588,
					Positions: []string{"31L", "31R", "32L", "32R", "33L", "33R", "34L", "34R", "35L", "35R", "41L", "41R", "42L", "42R", "43L", "43R", "44L", "44R", "45L", "45R"}},
				{Name: "5", Max: 2500, Arm: 46.0},
			},
			Zones: []Zone{{Name: "OA", FromRow: 1, ToRow: 8, Arm: 17.0}, {Name: "OB", FromRow: 10, ToRow: 23, Arm: 27.0}, {Name: "OC", FromRow: 24, ToRow: 38, Arm: 38.5}},
		},
		{
			Code: "77W", Name: "Boeing 777-300ER",
			Cabin: CabinLayout{Sections: []Section{
				{Compartment: "C", FromRow: 1, ToRow: 12, Letters: "A DG K"},
				{Compartment: "Y", FromRow: 20, ToRow: 50, Letters: "ABC DEFG HJK"},
				{Compartment: "Y", FromRow: 51, ToRow: 51, Letters: "DG"},
			}},
			DOW: 168000, DOWArm: 35.60, MZFW: 237682, MTOW: 351534, MLW: 251290,
			RefArm: 35.20, C: 5000, K: 100, LEMAC: 33.40, MAC: 7.07, FwdMAC: 14, AftMAC: 44, FuelArm: 35.0,
			Compartments: []Compartment{
				{Name: "1", Max: 30000, Arm: 23.0, ULD: true, ULDMax: 1588,
					Positions: []string{"11L", "11R", "12L", "12R", "13L", "13R", "14L", "14R", "21L", "21R", "22L", "22R", "23L", "23R", "24L", "24R", "25L", "25R", "26L", "26R"}},
				{Name: "3", Max: 32000, Arm: 47.5, ULD: true, ULDMax: 1588,
					Positions: []string{"31L", "31R", "32L", "32R", "33L", "33R", "34L", "34R", "35L", "35R", "36L", "36R", "41L", "41R", "42L", "42R", "43L", "43R", "44L", "44R", "45L", "45R", "46L", "46R"}},
				{Name: "5", Max: 4000, Arm: 54.0},
			},
			Zones: []Zone{{Name: "OA", FromRow: 1, ToRow: 12, Arm: 19.5}, {Name: "OB", FromRow: 20, ToRow: 35, Arm: 32.0}, {Name: "OC", FromRow: 36, ToRow: 51, Arm: 45.5}},
		},
	}
	f := &FleetData{Types: map[string]*AircraftType{}, DefaultType: "320"}
	for _, t := range types {
		f.Types[t.Code] = t
	}
	return f
}

// FuelPlan is the flight plan's fuel, in kilos.
type FuelPlan struct {
	TakeOff int `json:"takeoff"`
	Trip    int `json:"trip"`
}

// ULD is one container as loaded.
type ULD struct {
	Position string `json:"position"`
	ID       string `json:"id"` // e.g. AKE12345BA; empty for an empty position
	Dest     string `json:"dest,omitempty"`
	Weight   int    `json:"weight"`
	// Contents is the AHM code: B baggage, C cargo, M mail, E equipment,
	// with a compartment letter on baggage (BY, BC) when it is sorted.
	Contents string `json:"contents,omitempty"`
	Bags     int    `json:"bags,omitempty"`
}

// Load is the computed load and balance of a closed flight.
type Load struct {
	Adults   int `json:"adults"`
	Children int `json:"children"`
	Infants  int `json:"infants"`
	// ByCompartment is passengers per cabin, in F, C, Y order as listed in
	// Compartments order on the messages.
	ByCompartment map[string]int `json:"by_compartment"`
	Bags          int            `json:"bags"`
	BagKilos      int            `json:"bag_kilos"`
	Cargo         int            `json:"cargo"`
	Mail          int            `json:"mail"`
	// Holds is kilos per compartment name.
	Holds map[string]int `json:"holds"`
	ULDs  []ULD          `json:"ulds,omitempty"`

	PaxWeight   int `json:"pax_weight"`
	TrafficLoad int `json:"traffic_load"`
	DOW         int `json:"dow"`
	ZFW         int `json:"zfw"`
	TakeOffFuel int `json:"takeoff_fuel"`
	TOW         int `json:"tow"`
	TripFuel    int `json:"trip_fuel"`
	LAW         int `json:"law"`
	MZFW        int `json:"mzfw"`
	MTOW        int `json:"mtow"`
	MLW         int `json:"mlw"`
	// Underload is how much more could have been carried against the most
	// limiting weight. Negative is an overload.
	Underload int `json:"underload"`

	DOI    float64 `json:"doi"`
	LIZFW  float64 `json:"lizfw"`
	LITOW  float64 `json:"litow"`
	MACZFW float64 `json:"mac_zfw"`
	MACTOW float64 `json:"mac_tow"`

	// Violations lists every limit the load breaks. Empty is a legal load.
	Violations []string `json:"violations,omitempty"`
}

// Plan computes the load for a flight's boarded passengers and their bags.
//
// Passengers weigh by zone from their seats. Bags are distributed across the
// holds to bring the take-off centre of gravity towards the middle of the
// envelope, within each compartment's limit; on a containerised aircraft
// they are built into ULDs and given positions. Cargo and mail go aft first,
// which is where a trim-conscious load controller puts dead load on a
// passenger aircraft, then wherever there is room.
func (t *AircraftType) Plan(f *Flight, w StandardWeights, fuel FuelPlan, cargo, mail int) *Load {
	w = w.filled()
	l := &Load{ByCompartment: map[string]int{}, Holds: map[string]int{}, Cargo: cargo, Mail: mail,
		DOW: t.DOW, MZFW: t.MZFW, MTOW: t.MTOW, MLW: t.MLW, TakeOffFuel: fuel.TakeOff, TripFuel: fuel.Trip}
	for _, c := range t.Compartments {
		l.Holds[c.Name] = 0
	}

	// Passengers and their moment.
	moment := float64(t.DOW) * (t.DOWArm - t.RefArm)
	type bagRef struct {
		w    int
		dest string
		comp string
	}
	var bags []bagRef
	for _, p := range f.Passengers {
		if p.Status != StatusBoarded {
			continue
		}
		var pw int
		switch p.Type {
		case PaxChild:
			l.Children++
			pw = w.Child
		case PaxInfant:
			l.Infants++
			pw = w.Infant
		default:
			l.Adults++
			pw = w.Adult
		}
		l.ByCompartment[p.Compartment]++
		l.PaxWeight += pw
		moment += float64(pw) * (t.zoneArm(rowOf(p.Seat)) - t.RefArm)
		for _, b := range p.Bags {
			if b.Offloaded {
				continue
			}
			bags = append(bags, bagRef{w: b.Weight, dest: p.Dest, comp: p.Compartment})
			l.Bags++
			l.BagKilos += b.Weight
		}
	}
	l.DOI = t.K + float64(t.DOW)*(t.DOWArm-t.RefArm)/t.C

	// Dead load: aft compartments first.
	dead := cargo + mail
	aft := t.aftFirst()
	for _, c := range aft {
		if dead == 0 {
			break
		}
		room := c.Max - l.Holds[c.Name]
		take := min(room, dead)
		if take > 0 {
			l.Holds[c.Name] += take
			dead -= take
			moment += float64(take) * (c.Arm - t.RefArm)
		}
	}
	if dead > 0 {
		l.Violations = append(l.Violations, fmt.Sprintf("%d kg of cargo and mail does not fit the holds", dead))
	}

	// Bags: search the forward share that lands the take-off CG nearest the
	// envelope's middle without breaking a compartment limit.
	fwd, aftC := t.splitCompartments()
	target := (t.FwdMAC + t.AftMAC) / 2
	best, bestScore := 0.5, math.Inf(1)
	bagTotal := l.BagKilos
	for share := 0.0; share <= 1.0001; share += 0.05 {
		fwdKg := int(math.Round(float64(bagTotal) * share))
		aftKg := bagTotal - fwdKg
		if fwdKg > capacityOf(fwd, l.Holds) || aftKg > capacityOf(aftC, l.Holds) {
			continue
		}
		m := moment + float64(fwdKg)*(avgArm(fwd)-t.RefArm) + float64(aftKg)*(avgArm(aftC)-t.RefArm)
		tow := t.DOW + l.PaxWeight + bagTotal + cargo + mail + fuel.TakeOff
		mTO := m + float64(fuel.TakeOff)*(t.FuelArm-t.RefArm)
		mac := t.macOf(mTO, tow)
		score := math.Abs(mac - target)
		if score < bestScore {
			best, bestScore = share, score
		}
	}
	fwdKg := int(math.Round(float64(bagTotal) * best))
	// Distribute into actual compartments and, where they take containers,
	// into ULDs at positions.
	fill := func(comps []Compartment, kg int, contents string) {
		for _, c := range comps {
			if kg <= 0 {
				return
			}
			room := c.Max - l.Holds[c.Name]
			take := min(room, kg)
			if take <= 0 {
				continue
			}
			l.Holds[c.Name] += take
			moment += float64(take) * (c.Arm - t.RefArm)
			kg -= take
			if c.ULD {
				l.buildULDs(c, take, contents, f)
			}
		}
		if kg > 0 {
			l.Violations = append(l.Violations, fmt.Sprintf("%d kg of baggage does not fit the holds", kg))
		}
	}
	fill(fwd, fwdKg, "B")
	fill(aftC, bagTotal-fwdKg, "B")
	// Bag counts per ULD are apportioned by weight.
	if l.Bags > 0 && len(l.ULDs) > 0 {
		remaining := l.Bags
		last := -1
		for i := range l.ULDs {
			if l.ULDs[i].Contents == "" || l.ULDs[i].Contents[0] != 'B' {
				continue
			}
			n := int(math.Round(float64(l.Bags) * float64(l.ULDs[i].Weight) / float64(max(1, bagTotal))))
			n = min(n, remaining)
			l.ULDs[i].Bags = n
			remaining -= n
			last = i
		}
		if last >= 0 {
			// Rounding leaves a bag or two unaccounted; they are real and
			// they are in the last container.
			l.ULDs[last].Bags += remaining
		}
	}

	l.TrafficLoad = l.PaxWeight + l.BagKilos + cargo + mail
	l.ZFW = t.DOW + l.TrafficLoad
	l.TOW = l.ZFW + fuel.TakeOff
	l.LAW = l.TOW - fuel.Trip
	l.LIZFW = t.K + moment/t.C
	momentTO := moment + float64(fuel.TakeOff)*(t.FuelArm-t.RefArm)
	l.LITOW = t.K + momentTO/t.C
	l.MACZFW = round1(t.macOf(moment, l.ZFW))
	l.MACTOW = round1(t.macOf(momentTO, l.TOW))
	l.LIZFW, l.LITOW = round1(l.LIZFW), round1(l.LITOW)
	l.DOI = round1(l.DOI)

	l.Underload = min(t.MZFW-l.ZFW, t.MTOW-l.TOW, t.MLW-l.LAW)
	if l.ZFW > t.MZFW {
		l.Violations = append(l.Violations, fmt.Sprintf("ZFW %d exceeds MZFW %d", l.ZFW, t.MZFW))
	}
	if l.TOW > t.MTOW {
		l.Violations = append(l.Violations, fmt.Sprintf("TOW %d exceeds MTOW %d", l.TOW, t.MTOW))
	}
	if l.LAW > t.MLW {
		l.Violations = append(l.Violations, fmt.Sprintf("LAW %d exceeds MLW %d", l.LAW, t.MLW))
	}
	if l.MACTOW < t.FwdMAC || l.MACTOW > t.AftMAC {
		l.Violations = append(l.Violations, fmt.Sprintf("take-off CG %.1f%% MAC outside %.0f-%.0f", l.MACTOW, t.FwdMAC, t.AftMAC))
	}
	for _, c := range t.Compartments {
		if l.Holds[c.Name] > c.Max {
			l.Violations = append(l.Violations, fmt.Sprintf("compartment %s %d kg exceeds %d", c.Name, l.Holds[c.Name], c.Max))
		}
	}
	return l
}

func round1(x float64) float64 { return math.Round(x*10) / 10 }

func (t *AircraftType) macOf(moment float64, weight int) float64 {
	if weight <= 0 || t.MAC == 0 {
		return 0
	}
	arm := t.RefArm + moment/float64(weight)
	return (arm - t.LEMAC) / t.MAC * 100
}

func (t *AircraftType) zoneArm(row int) float64 {
	for _, z := range t.Zones {
		if row >= z.FromRow && row <= z.ToRow {
			return z.Arm
		}
	}
	if len(t.Zones) > 0 {
		// Off the zone table: nearest end.
		if row < t.Zones[0].FromRow {
			return t.Zones[0].Arm
		}
		return t.Zones[len(t.Zones)-1].Arm
	}
	return t.RefArm
}

// splitCompartments divides the holds into forward and aft of the datum.
func (t *AircraftType) splitCompartments() (fwd, aft []Compartment) {
	for _, c := range t.Compartments {
		if c.Arm < t.RefArm {
			fwd = append(fwd, c)
		} else {
			aft = append(aft, c)
		}
	}
	return fwd, aft
}

func (t *AircraftType) aftFirst() []Compartment {
	out := append([]Compartment(nil), t.Compartments...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Arm > out[j].Arm })
	return out
}

func capacityOf(comps []Compartment, used map[string]int) int {
	n := 0
	for _, c := range comps {
		n += c.Max - used[c.Name]
	}
	return n
}

func avgArm(comps []Compartment) float64 {
	if len(comps) == 0 {
		return 0
	}
	sum, wsum := 0.0, 0.0
	for _, c := range comps {
		sum += c.Arm * float64(c.Max)
		wsum += float64(c.Max)
	}
	return sum / wsum
}

// buildULDs packs kg of contents into containers at the compartment's
// positions, fullest first.
func (l *Load) buildULDs(c Compartment, kg int, contents string, f *Flight) {
	used := map[string]bool{}
	for _, u := range l.ULDs {
		used[u.Position] = true
	}
	seq := len(l.ULDs) + 1
	for _, pos := range c.Positions {
		if kg <= 0 {
			break
		}
		if used[pos] {
			continue
		}
		take := min(kg, c.ULDMax)
		l.ULDs = append(l.ULDs, ULD{
			Position: pos, ID: fmt.Sprintf("AKE%05d%s", 10000+seq, f.Carrier),
			Dest: f.Dest, Weight: take, Contents: contents,
		})
		seq++
		kg -= take
	}
}

// Loadsheet renders the document the captain signs, in the classic layout.
func (t *AircraftType) Loadsheet(f *Flight, l *Load, at time.Time) string {
	var b strings.Builder
	line := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	line("LOADSHEET                        CHECKED   APPROVED   EDNO")
	line("ALL WEIGHTS IN KILOS                                    01")
	line("FROM/TO  FLIGHT   A/C REG  VERSION    CREW  DATE   TIME")
	reg := f.Registration
	if reg == "" {
		reg = "TBA"
	}
	crew := f.Crew
	if crew == "" {
		crew = "-/-"
	}
	line("%-3s %-3s  %-8s %-8s %-10s %-5s %-6s %s", f.Board, f.Dest, f.Flight, reg, f.Version, crew, f.Date, at.Format("1504"))
	holds := make([]string, 0, len(t.Compartments))
	for _, c := range t.Compartments {
		holds = append(holds, fmt.Sprintf("%s/%d", c.Name, l.Holds[c.Name]))
	}
	line("LOAD IN COMPARTMENTS   %d   %s", l.BagKilos+l.Cargo+l.Mail, strings.Join(holds, " "))
	pax := []string{}
	for _, c := range []string{"F", "C", "Y"} {
		if n, ok := l.ByCompartment[c]; ok && n > 0 {
			pax = append(pax, fmt.Sprintf("%d", n))
		}
	}
	line("PASSENGER/CABIN BAG    %d/%d/%d  TTL %s  CAB 0  BAG %d/%d",
		l.Adults, l.Children, l.Infants, strings.Join(pax, "/"), l.Bags, l.BagKilos)
	line("TOTAL TRAFFIC LOAD     %d", l.TrafficLoad)
	line("DRY OPERATING WEIGHT   %d", l.DOW)
	line("ZERO FUEL WEIGHT ACTUAL %d  MAX %d  ADJ", l.ZFW, l.MZFW)
	line("TAKE OFF FUEL          %d", l.TakeOffFuel)
	line("TAKE OFF WEIGHT ACTUAL %d  MAX %d  ADJ", l.TOW, l.MTOW)
	line("TRIP FUEL              %d", l.TripFuel)
	line("LANDING WEIGHT ACTUAL  %d  MAX %d  ADJ", l.LAW, l.MLW)
	line("BALANCE AND SEATING CONDITIONS          LAST MINUTE CHANGES")
	line("DOI %.1f  LIZFW %.1f  MACZFW %.1f", l.DOI, l.LIZFW, l.MACZFW)
	line("LITOW %.1f  MACTOW %.1f", l.LITOW, l.MACTOW)
	line("UNDERLOAD BEFORE LMC   %d", l.Underload)
	if len(l.Violations) > 0 {
		line("SI *** LOAD NOT LEGAL ***")
		for _, v := range l.Violations {
			line("   %s", strings.ToUpper(v))
		}
	} else {
		line("SI NIL")
	}
	line("PREPARED BY JETWAY DCS %s", at.Format("02JAN 1504"))
	return strings.TrimRight(b.String(), "\n")
}

// ---------------------------------------------------------------- LDM

// LDM is a load message: what the aircraft carries, by destination and by
// compartment, for the arrival station and operations (AHM 583).
//
// The layout follows the freely published worked examples: an
// identification line of flight/day, registration, version and crew; then
// one line per destination with passenger counts, the hold total and each
// compartment's weight, passengers per cabin and standbys; then SI.
type LDM struct {
	Flight       string           `json:"flight"`
	Day          string           `json:"day"`
	Registration string           `json:"registration"`
	Version      string           `json:"version"`
	Crew         string           `json:"crew"`
	Destinations []LDMDestination `json:"destinations"`
	SI           []string         `json:"si,omitempty"`
}

// LDMDestination is one destination's load.
type LDMDestination struct {
	Dest     string         `json:"dest"`
	Adults   int            `json:"adults"`
	Children int            `json:"children"`
	Infants  int            `json:"infants"`
	Total    int            `json:"total"` // hold weight
	Holds    map[string]int `json:"holds"`
	// Pax is passengers per cabin, in the order the version names them.
	Pax []int `json:"pax"`
	PAD []int `json:"pad"`
	// Extra keeps destination fields this profile does not model -- PRF,
	// DHC, B138/1794 and whatever else a sender's house style adds --
	// verbatim, so nothing on the wire is lost.
	Extra []string `json:"extra,omitempty"`
}

// BuildLDM renders the load message.
func BuildLDM(f *Flight, l *Load) string {
	var b strings.Builder
	b.WriteString("LDM\n")
	reg := f.Registration
	if reg == "" {
		reg = "TBA"
	}
	crew := f.Crew
	if crew == "" {
		crew = "0/0"
	}
	day := f.Date
	if len(day) >= 2 {
		day = day[:2]
	}
	fmt.Fprintf(&b, "%s/%s.%s.%s.%s\n", f.Flight, day, reg, f.Version, crew)
	holdNames := make([]string, 0, len(l.Holds))
	for name := range l.Holds {
		holdNames = append(holdNames, name)
	}
	sort.Strings(holdNames)
	total := 0
	for _, n := range holdNames {
		total += l.Holds[n]
	}
	dest := f.Dest
	if dest == "" {
		dest = "XXX"
	}
	fmt.Fprintf(&b, "-%s.%d/%d/%d.T%d", dest, l.Adults, l.Children, l.Infants, total)
	for _, n := range holdNames {
		fmt.Fprintf(&b, ".%s/%d", n, l.Holds[n])
	}
	comps := versionCompartments(f.Version)
	b.WriteString(".PAX")
	for _, c := range comps {
		fmt.Fprintf(&b, "/%d", l.ByCompartment[c])
	}
	b.WriteString(".PAD")
	for range comps {
		b.WriteString("/0")
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "SI BAG %d/%d CGO %d MAIL %d", l.Bags, l.BagKilos, l.Cargo, l.Mail)
	if len(l.Violations) > 0 {
		b.WriteString(" LOAD NOT LEGAL")
	}
	return b.String()
}

// versionCompartments lists the cabin codes a version string names, in order.
func versionCompartments(version string) []string {
	var out []string
	for _, r := range version {
		if r >= 'A' && r <= 'Z' {
			out = append(out, string(r))
		}
	}
	if len(out) == 0 {
		return []string{"Y"}
	}
	return out
}

var ldmIDRe = regexp.MustCompile(`^([A-Z0-9]{2}[A-Z]?\d{1,4}[A-Z]?)/(\d{1,2})\.([A-Z0-9-]{2,10})\.([A-Z0-9]+)\.(\d+/\d+)$`)

// ParseLDM reads a load message.
func ParseLDM(text string) (*LDM, error) {
	ls := lines(text)
	if len(ls) < 3 || ls[0] != string(KindLDM) {
		return nil, fmt.Errorf("dcs: not an LDM")
	}
	sm := ldmIDRe.FindStringSubmatch(strings.TrimSpace(ls[1]))
	if sm == nil {
		return nil, fmt.Errorf("dcs: LDM identification %q not understood", ls[1])
	}
	m := &LDM{Flight: sm[1], Day: sm[2], Registration: sm[3], Version: sm[4], Crew: sm[5]}
	for _, ln := range ls[2:] {
		t := strings.TrimSpace(ln)
		switch {
		case strings.HasPrefix(t, "SI"):
			m.SI = append(m.SI, strings.TrimSpace(strings.TrimPrefix(t, "SI")))
		case strings.HasPrefix(t, "-"):
			d, err := parseLDMDest(t)
			if err != nil {
				return nil, err
			}
			m.Destinations = append(m.Destinations, d)
		default:
			m.SI = append(m.SI, t)
		}
	}
	return m, nil
}

func parseLDMDest(t string) (LDMDestination, error) {
	// -JFK.161/119/43.T9335.1/2105.4/5330.PAX/48/275.PAD/0/0
	fields := strings.Split(strings.TrimPrefix(t, "-"), ".")
	if len(fields) < 2 {
		return LDMDestination{}, fmt.Errorf("dcs: LDM destination %q too short", t)
	}
	d := LDMDestination{Dest: fields[0], Holds: map[string]int{}}
	counts := strings.Split(fields[1], "/")
	ints := make([]int, len(counts))
	for i, c := range counts {
		ints[i], _ = strconv.Atoi(c)
	}
	switch len(ints) {
	case 3:
		d.Adults, d.Children, d.Infants = ints[0], ints[1], ints[2]
	case 4:
		// Male/female/child/infant, the older form.
		d.Adults, d.Children, d.Infants = ints[0]+ints[1], ints[2], ints[3]
	default:
		if len(ints) > 0 {
			d.Adults = ints[0]
		}
	}
	for _, fld := range fields[2:] {
		switch {
		case strings.HasPrefix(fld, "T"):
			d.Total, _ = strconv.Atoi(fld[1:])
		case strings.HasPrefix(fld, "PAX/"):
			for _, n := range strings.Split(fld[4:], "/") {
				v, _ := strconv.Atoi(n)
				d.Pax = append(d.Pax, v)
			}
		case strings.HasPrefix(fld, "PAD/"):
			for _, n := range strings.Split(fld[4:], "/") {
				v, _ := strconv.Atoi(n)
				d.PAD = append(d.PAD, v)
			}
		default:
			name, kg, ok := strings.Cut(fld, "/")
			if !ok || !allDigits(name) {
				d.Extra = append(d.Extra, fld)
				continue
			}
			d.Holds[name], _ = strconv.Atoi(kg)
		}
	}
	return d, nil
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------- CPM

// CPM is a container/pallet distribution message: which ULD is at which
// position, for which destination, at what weight, holding what (AHM 587).
//
// The layout follows the freely published worked example: identification of
// flight/day, registration and cabin version; one line per position with the
// ULD identifier, destination, weight and contents code, or N for an empty
// position; bulk compartments without a ULD; then SI.
type CPM struct {
	Flight       string   `json:"flight"`
	Day          string   `json:"day"`
	Registration string   `json:"registration"`
	Version      string   `json:"version"`
	Positions    []ULD    `json:"positions"`
	SI           []string `json:"si,omitempty"`
}

// BuildCPM renders the container distribution for a containerised aircraft.
func BuildCPM(f *Flight, l *Load) string {
	var b strings.Builder
	b.WriteString("CPM\n")
	reg := f.Registration
	if reg == "" {
		reg = "TBA"
	}
	day := f.Date
	if len(day) >= 2 {
		day = day[:2]
	}
	fmt.Fprintf(&b, "%s/%s.%s.%s\n", f.Flight, day, reg, f.Version)
	for _, u := range l.ULDs {
		fmt.Fprintf(&b, "-%s/%s/%s/%d/%s\n", u.Position, u.ID, u.Dest, u.Weight, u.Contents)
	}
	// Bulk compartments carry their weight without a ULD.
	names := make([]string, 0, len(l.Holds))
	for n := range l.Holds {
		names = append(names, n)
	}
	sort.Strings(names)
	uldComp := map[string]bool{}
	for _, u := range l.ULDs {
		if len(u.Position) > 0 {
			uldComp[u.Position[:1]] = true
		}
	}
	for _, n := range names {
		if uldComp[n] || l.Holds[n] == 0 {
			continue
		}
		fmt.Fprintf(&b, "-%s/%s/%d/B\n", n, f.Dest, l.Holds[n])
	}
	fmt.Fprintf(&b, "SI %d ULD", len(l.ULDs))
	return b.String()
}

var cpmIDRe = regexp.MustCompile(`^([A-Z0-9]{2}[A-Z]?\d{1,4}[A-Z]?)/(\d{1,2})\.([A-Z0-9-]{2,10})\.([A-Z0-9]+)$`)

// ParseCPM reads a container distribution message.
func ParseCPM(text string) (*CPM, error) {
	ls := lines(text)
	if len(ls) < 2 || ls[0] != string(KindCPM) {
		return nil, fmt.Errorf("dcs: not a CPM")
	}
	sm := cpmIDRe.FindStringSubmatch(strings.TrimSpace(ls[1]))
	if sm == nil {
		return nil, fmt.Errorf("dcs: CPM identification %q not understood", ls[1])
	}
	m := &CPM{Flight: sm[1], Day: sm[2], Registration: sm[3], Version: sm[4]}
	for _, ln := range ls[2:] {
		t := strings.TrimSpace(ln)
		switch {
		case strings.HasPrefix(t, "SI"):
			m.SI = append(m.SI, strings.TrimSpace(strings.TrimPrefix(t, "SI")))
		case strings.HasPrefix(t, "-"):
			parts := strings.Split(strings.TrimPrefix(t, "-"), "/")
			u := ULD{Position: parts[0]}
			switch len(parts) {
			case 2:
				// -13L/N: an empty position.
				u.Contents = parts[1]
			case 4:
				// -5/IST/50/BY: bulk, no ULD.
				u.Dest = parts[1]
				u.Weight, _ = strconv.Atoi(parts[2])
				u.Contents = parts[3]
			default:
				if len(parts) >= 5 {
					u.ID, u.Dest = parts[1], parts[2]
					u.Weight, _ = strconv.Atoi(parts[3])
					u.Contents = strings.Join(parts[4:], "/")
				}
			}
			m.Positions = append(m.Positions, u)
		default:
			m.SI = append(m.SI, t)
		}
	}
	return m, nil
}
