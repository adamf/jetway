package fare

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Segment is one flight of the itinerary to price.
type Segment struct {
	Carrier     string
	Origin      string
	Destination string
	Class       string
	Depart      time.Time
	// Arrive, when known, is what a stop is measured from: the wait at a
	// connection is the next departure less this arrival. Unknown, the wait
	// is not checked.
	Arrive time.Time
	// FlightNum is the flight number, for the fare calculation line.
	FlightNum string
	// Miles is the segment's ticketed mileage. A through fare is divided
	// between the segments it covers by their miles, and a routing's
	// mileage limit counts them; unknown, the division is equal.
	Miles int
}

// Request is an itinerary and who is travelling on it.
type Request struct {
	Segments   []Segment
	Passengers []PaxType
	// Purchased is when the fare is being sold; advance purchase rules
	// count from it. Zero means now.
	Purchased time.Time
	// Sale is the point of sale, for the fares that may only be sold by
	// some sellers.
	Sale Sale
	// Open, when set, lets the pricing choose each segment's booking class
	// instead of taking the one booked: it reports whether a class can be
	// sold on a segment (by index), and the cheapest fares whose classes
	// are open win. The quote says which class each segment was priced in.
	// This is the lowest-fare search a seller runs before the sell; nil
	// prices the itinerary as booked.
	Open func(segment int, class string) bool
	// Distance, when set, is the miles between two points not flown: the
	// gap of an open jaw, which may not be longer than the shortest of
	// the unit's components. Unset, a trip that comes home to its origin
	// from another point is an open jaw whatever the gap, and one that
	// ends somewhere else is not an open jaw at all.
	Distance func(a, b string) int
}

// SegmentFare is what one passenger pays for one segment: its share of
// the fare component that covers it.
type SegmentFare struct {
	Basis string `json:"basis"`
	Class string `json:"class"`
	Base  Money  `json:"base"`
}

// TaxLine is one tax on one passenger's ticket.
type TaxLine struct {
	Code   string `json:"code"`
	Amount Money  `json:"amount"`
	// CarrierImposed marks the carrier's own fee.
	CarrierImposed bool `json:"carrier_imposed,omitempty"`
}

// Component is one fare component of a priced itinerary: the fare and the
// segments it covers, by index, with what it costs an adult.
type Component struct {
	Fare Fare `json:"fare"`
	// From and To are the first and last segment covered.
	From int `json:"from"`
	To   int `json:"to"`
	// Amount is the fare plus its surcharges.
	Amount    Money `json:"amount"`
	Surcharge Money `json:"surcharge,omitempty"`
	// Unit is the index of the priceable unit the component belongs to.
	Unit int `json:"unit"`
}

// UnitKind is the shape of a priceable unit.
type UnitKind string

const (
	OneWayUnit    UnitKind = "one way"
	RoundTripUnit UnitKind = "round trip"
	OpenJawUnit   UnitKind = "open jaw"
	CircleUnit    UnitKind = "circle trip"
)

// Unit is one priceable unit: its shape and its components, by index into
// the quote's Components.
type Unit struct {
	Kind       UnitKind `json:"kind"`
	Components []int    `json:"components"`
}

// PassengerQuote is one passenger's price.
type PassengerQuote struct {
	Type     PaxType       `json:"type"`
	Segments []SegmentFare `json:"segments"`
	Base     Money         `json:"base"`
	Taxes    []TaxLine     `json:"taxes"`
	// CarrierFees is the part of the taxes that is the carrier's own.
	CarrierFees Money `json:"carrier_fees,omitempty"`
	Total       Money `json:"total"`
}

// Quote is the priced itinerary.
type Quote struct {
	Currency   string           `json:"currency"`
	Passengers []PassengerQuote `json:"passengers"`
	Base       Money            `json:"base"`
	Taxes      Money            `json:"taxes"`
	// CarrierFees is the part of Taxes that is the carriers' own.
	CarrierFees Money `json:"carrier_fees,omitempty"`
	Total       Money `json:"total"`
	// Components and Units say how the itinerary was divided: which fare
	// pays for which flights, and which fares were priced together.
	Components []Component `json:"components,omitempty"`
	Units      []Unit      `json:"units,omitempty"`
	// Calc is the fare calculation line an adult's ticket prints.
	Calc string `json:"calc,omitempty"`
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

// maxSegments bounds the search. The number of ways to divide a journey
// grows faster than exponentially with its flights (de Marcken shows the
// general problem is not even decidable), and no ticket has room for more
// coupons than this.
const maxSegments = 16

// maxRun is the most flights one fare component covers here.
const maxRun = 4

// candidate is a fare that could cover a run of segments, with what it
// costs there.
type candidate struct {
	fare      Fare
	amount    int64
	surcharge int64
	classes   []string // the class each segment of the run is priced in
}

type pricing struct {
	req       Request
	t         Tariff
	purchased time.Time
	n         int
	cands     map[[2]int][]candidate
	// why is the refusal to report if nothing sells.
	why      failure
	filed    []bool // a fare in the class exists for some run covering the segment
	best     int64
	bestSet  []Component
	bestUnit []Unit
	classes  []string // the class each segment is priced in, for the best division
	found    bool
}

// failure is one refusal met during the search. When nothing sells, the
// refusal worth telling is the one from the attempt that came nearest:
// the division that priced the most units before it failed, then the
// larger unit, then the dearer fare.
type failure struct {
	depth, size int
	amount      int64
	seg         int
	reason      string
	set         bool
}

func (a failure) before(b failure) bool {
	if a.depth != b.depth {
		return a.depth < b.depth
	}
	if a.size != b.size {
		return a.size < b.size
	}
	return a.amount <= b.amount
}

// fail records a refusal: depth is how many units of the division had
// priced before it (-1 for a fare refused before any division), size the
// number of components in the unit that refused.
func (p *pricing) fail(seg, depth, size int, amount int64, reason string) {
	f := failure{depth: depth, size: size, amount: amount, seg: seg, reason: reason, set: true}
	if !p.why.set || p.why.before(f) {
		p.why = f
	}
}

// Price prices a request against a tariff. It divides the itinerary into
// fare components and priceable units every way the fares allow and
// returns the cheapest division whose every fare passes its rules: the
// lowest price for the classes as booked, or with Request.Open set the
// lowest price in any class that can be sold. Then it adds the
// passenger-type discount and the taxes.
func Price(t Tariff, req Request) (*Quote, error) {
	if t == nil {
		return nil, errors.New("fare: no tariff")
	}
	if len(req.Segments) == 0 || len(req.Passengers) == 0 {
		return nil, errors.New("fare: nothing to price")
	}
	if len(req.Segments) > maxSegments {
		return nil, fmt.Errorf("fare: %d segments is more than one ticket holds", len(req.Segments))
	}
	p := &pricing{req: req, t: t, purchased: req.Purchased, n: len(req.Segments), cands: map[[2]int][]candidate{}}
	if p.purchased.IsZero() {
		p.purchased = time.Now().UTC()
	}
	p.filed = make([]bool, p.n)
	p.gather()
	for i, ok := range p.filed {
		if !ok {
			return nil, &ErrNoFare{Segment: req.Segments[i], Reason: "class not filed on the market"}
		}
	}
	p.divide(0, nil)
	if !p.found {
		if p.why.set {
			return nil, &ErrNoFare{Segment: req.Segments[p.why.seg], Reason: p.why.reason}
		}
		return nil, &ErrNoFare{Segment: req.Segments[0], Reason: "no combination of fares covers the itinerary"}
	}
	return p.quote(), nil
}

// gather finds, for every run of consecutive segments a fare could cover,
// the fares that pass the rules a component can be judged on by itself.
func (p *pricing) gather() {
	segs := p.req.Segments
	for i := 0; i < p.n; i++ {
		for j := i; j < p.n && j < i+maxRun; j++ {
			if j > i && !strings.EqualFold(segs[j-1].Destination, segs[j].Origin) {
				break // a surface break: no fare flies over it
			}
			if j > i && strings.EqualFold(segs[j].Destination, segs[i].Origin) {
				break // back where it started: that is two components
			}
			run := segs[i : j+1]
			seen := map[string]bool{}
			for _, s := range run {
				c := strings.ToUpper(s.Carrier)
				if seen[c] {
					continue
				}
				seen[c] = true
				for _, f := range p.t.Fares(c, run[0].Origin, run[len(run)-1].Destination) {
					p.consider(i, j, f)
				}
			}
			sort.SliceStable(p.cands[[2]int{i, j}], func(a, b int) bool {
				return p.cands[[2]int{i, j}][a].amount < p.cands[[2]int{i, j}][b].amount
			})
		}
	}
}

// consider holds one fare against one run.
func (p *pricing) consider(i, j int, f Fare) {
	run := p.req.Segments[i : j+1]
	classes := make([]string, len(run))
	for k, s := range run {
		want := f.Class
		if !strings.EqualFold(s.Carrier, f.Carrier) {
			if !containsFold(f.Rule.Carriers, s.Carrier) {
				return // not this fare's to fly on that carrier
			}
			if pc, ok := f.Rule.PartnerClass[strings.ToUpper(s.Carrier)]; ok {
				want = pc
			}
		}
		if p.req.Open != nil {
			if !p.req.Open(i+k, want) {
				return
			}
		} else if !strings.EqualFold(s.Class, want) {
			return
		}
		classes[k] = strings.ToUpper(want)
	}
	// From here the fare is filed for the class on this run; a refusal is
	// a rule's.
	for k := i; k <= j; k++ {
		p.filed[k] = true
	}
	amount := f.OneWay.Amount
	if why := p.componentRule(f, run); why != "" {
		p.fail(i, -1, 0, amount, why)
		return
	}
	var sur int64
	for _, sc := range f.Rule.Surcharges {
		if len(sc.Days) > 0 && !hasDay(sc.Days, run[0].Depart.Weekday()) {
			continue
		}
		if sc.From != "" && !strings.EqualFold(sc.From, run[0].Origin) {
			continue
		}
		sur += sc.Amount.Amount
	}
	key := [2]int{i, j}
	p.cands[key] = append(p.cands[key], candidate{fare: f, amount: amount + sur, surcharge: sur, classes: classes})
}

// componentRule checks what can be judged from the component alone: who
// may sell the fare, its route, its dates and its days. It returns the
// reason for a refusal, or "".
func (p *pricing) componentRule(f Fare, run []Segment) string {
	r := f.Rule
	first := run[0].Depart
	if why := r.Sale.check(p.req.Sale, p.purchased); why != "" {
		return why
	}
	if len(run) == 1 && r.NoNonstop {
		return "nonstop travel not permitted"
	}
	if len(run) > 1 {
		if r.NonstopOnly {
			return "nonstop travel only"
		}
		if why := f.Routing.check(run); why != "" {
			return why
		}
		limit := time.Duration(r.MaxStopHours) * time.Hour
		if limit == 0 {
			limit = 24 * time.Hour
		}
		for k := 1; k < len(run); k++ {
			if run[k-1].Arrive.IsZero() || run[k].Depart.IsZero() {
				continue
			}
			if run[k].Depart.Sub(run[k-1].Arrive) > limit {
				return fmt.Sprintf("stopover at %s not permitted", strings.ToUpper(run[k].Origin))
			}
		}
	}
	if !r.SeasonFrom.IsZero() && first.Before(r.SeasonFrom) {
		return "outside season"
	}
	if !r.SeasonTo.IsZero() && first.After(r.SeasonTo) {
		return "outside season"
	}
	for _, b := range r.Blackouts {
		if !first.Before(b.From) && !first.After(b.To) {
			return "blackout date"
		}
	}
	if len(r.Days) > 0 && !hasDay(r.Days, first.Weekday()) {
		return "day of week"
	}
	if r.DepartFrom > 0 || r.DepartTo > 0 {
		min := first.Hour()*60 + first.Minute()
		to := r.DepartTo
		if to == 0 {
			to = 24 * 60
		}
		if min < r.DepartFrom || min >= to {
			return "time of day"
		}
	}
	return ""
}

func (s SaleRule) check(sale Sale, at time.Time) string {
	if !s.From.IsZero() && at.Before(s.From) {
		return "not yet on sale"
	}
	if !s.To.IsZero() && at.After(s.To) {
		return "no longer on sale"
	}
	if len(s.Countries) > 0 && !containsFold(s.Countries, sale.Country) {
		return "not sold in this country"
	}
	if len(s.Channels) > 0 && !containsFold(s.Channels, sale.Channel) {
		return "not sold through this channel"
	}
	if len(s.Agencies) > 0 && !containsFold(s.Agencies, sale.Agency) {
		return "not sold by this agency"
	}
	if len(s.Accounts) > 0 && !containsFold(s.Accounts, sale.Account) {
		return "sold to a contract only"
	}
	return ""
}

func (r Routing) check(run []Segment) string {
	if !r.Any {
		limit := r.MaxVia
		if limit == 0 {
			limit = 1
		}
		if len(r.Via) == 0 {
			return "nonstop travel only"
		}
		if len(run)-1 > limit {
			return "routing permits fewer connections"
		}
		var used []string
		for k := 1; k < len(run); k++ {
			via := strings.ToUpper(run[k].Origin)
			if !containsFold(r.Via, via) {
				return "routing does not permit " + via
			}
			used = append(used, via)
		}
		for _, x := range r.Excludes {
			if containsFold(used, x[0]) && containsFold(used, x[1]) {
				return fmt.Sprintf("routing permits %s or %s, not both", strings.ToUpper(x[0]), strings.ToUpper(x[1]))
			}
		}
	}
	if r.MaxMiles > 0 {
		miles := 0
		for _, s := range run {
			miles += s.Miles
		}
		if miles > r.MaxMiles {
			return "exceeds the maximum permitted mileage"
		}
	}
	return ""
}

// divide walks every way to cover the segments from start with fare
// components, and for each complete cover groups the components into
// priceable units.
func (p *pricing) divide(start int, runs [][2]int) {
	if start == p.n {
		p.group(runs)
		return
	}
	for j := start; j < p.n && j < start+maxRun; j++ {
		if len(p.cands[[2]int{start, j}]) == 0 {
			continue
		}
		p.divide(j+1, append(runs, [2]int{start, j}))
	}
}

// group assigns a cover's components to priceable units every way their
// geometry allows and prices each assignment.
func (p *pricing) group(runs [][2]int) {
	used := make([]bool, len(runs))
	var units [][]int
	var walk func()
	walk = func() {
		first := -1
		for i, u := range used {
			if !u {
				first = i
				break
			}
		}
		if first < 0 {
			p.priceUnits(runs, units)
			return
		}
		used[first] = true
		// A unit of one, two, three or four components beginning with the
		// first one still free.
		var extend func(members []int, from int)
		extend = func(members []int, from int) {
			if _, ok := p.shape(runs, members); ok {
				units = append(units, append([]int(nil), members...))
				walk()
				units = units[:len(units)-1]
			}
			if len(members) == 4 {
				return
			}
			for k := from; k < len(runs); k++ {
				if used[k] {
					continue
				}
				used[k] = true
				extend(append(members, k), k+1)
				used[k] = false
			}
		}
		extend([]int{first}, first+1)
		used[first] = false
	}
	walk()
}

func (p *pricing) ends(run [2]int) (from, to string) {
	return strings.ToUpper(p.req.Segments[run[0]].Origin), strings.ToUpper(p.req.Segments[run[1]].Destination)
}

// shape is the geometry of a set of components taken in journey order.
func (p *pricing) shape(runs [][2]int, members []int) (UnitKind, bool) {
	switch len(members) {
	case 1:
		return OneWayUnit, true
	case 2:
		a1, a2 := p.ends(runs[members[0]])
		b1, b2 := p.ends(runs[members[1]])
		switch {
		case a2 == b1 && b2 == a1:
			return RoundTripUnit, true
		case b2 == a1 && a2 != b1:
			// Open at the turnaround: out A-B, back C-A. The traveller
			// comes home, so the shape is plain; the gap is measured when
			// it can be.
			if p.req.Distance != nil && p.req.Distance(a2, b1) > p.shortest(runs, members) {
				return "", false
			}
			return OpenJawUnit, true
		case a2 == b1 && b2 != a1:
			// Open at the origin: out A-B, back B-C. Without the distance
			// from C to A this cannot be told from a journey that simply
			// goes on, so it is an open jaw only when the gap is known to
			// be shorter than the flights.
			if p.req.Distance == nil || p.req.Distance(b2, a1) > p.shortest(runs, members) {
				return "", false
			}
			return OpenJawUnit, true
		}
		return "", false
	default:
		// A circle: each component starts where the last ended, and the
		// last ends where the first began.
		for k := range members {
			_, to := p.ends(runs[members[k]])
			from, _ := p.ends(runs[members[(k+1)%len(members)]])
			if to != from {
				return "", false
			}
		}
		return CircleUnit, true
	}
}

// shortest is the fewest miles any of the components flies, or a very
// large number when the segments carry none.
func (p *pricing) shortest(runs [][2]int, members []int) int {
	best := 1 << 30
	for _, m := range members {
		miles := 0
		for k := runs[m][0]; k <= runs[m][1]; k++ {
			miles += p.req.Segments[k].Miles
		}
		if miles > 0 && miles < best {
			best = miles
		}
	}
	return best
}

// priceUnits prices one assignment of components to units: for each unit
// the cheapest fares that pass the rules judged across the unit, and
// keeps the assignment if it is the cheapest yet.
func (p *pricing) priceUnits(runs [][2]int, units [][]int) {
	var total int64
	comps := make([]Component, len(runs))
	var out []Unit
	for u, members := range units {
		kind, _ := p.shape(runs, members)
		pick, cost, ok := p.cheapest(runs, members, kind, len(units) > 1, u)
		if !ok {
			return
		}
		total += cost
		if p.found && total > p.best {
			return
		}
		idx := make([]int, len(members))
		for k, m := range members {
			c := pick[k]
			comps[m] = Component{Fare: c.fare, From: runs[m][0], To: runs[m][1],
				Amount: Money{Amount: c.amount, Currency: c.fare.OneWay.Currency}, Unit: u}
			if c.surcharge != 0 {
				comps[m].Surcharge = Money{Amount: c.surcharge, Currency: c.fare.OneWay.Currency}
			}
			idx[k] = m
		}
		out = append(out, Unit{Kind: kind, Components: idx})
	}
	// A tie goes to the division with fewer components -- one through fare
	// rather than two local ones at the same money -- and then to the one
	// with fewer units, so a round trip is priced as a round trip.
	if p.found && total == p.best && (len(runs) > len(p.bestSet) || (len(runs) == len(p.bestSet) && len(out) >= len(p.bestUnit))) {
		return
	}
	p.found, p.best, p.bestSet, p.bestUnit = true, total, comps, out
	p.classes = p.classesOf(runs, comps)
}

// cheapest finds the lowest-cost choice of one fare per component of a
// unit that passes the unit's rules.
func (p *pricing) cheapest(runs [][2]int, members []int, kind UnitKind, shared bool, depth int) ([]candidate, int64, bool) {
	lists := make([][]candidate, len(members))
	for k, m := range members {
		lists[k] = p.cands[[2]int{runs[m][0], runs[m][1]}]
	}
	pick := make([]candidate, len(members))
	var best []candidate
	var bestCost int64
	found := false
	var walk func(k int, cost int64)
	walk = func(k int, cost int64) {
		if found && cost >= bestCost {
			return
		}
		if k == len(members) {
			if why, at := p.unitRule(runs, members, pick, kind, shared); why != "" {
				p.fail(runs[members[at]][0], depth, len(members), pick[at].amount, why)
				return
			}
			best, bestCost, found = append([]candidate(nil), pick...), cost, true
			return
		}
		for _, c := range lists[k] {
			pick[k] = c
			walk(k+1, cost+c.amount)
		}
	}
	walk(0, 0)
	return best, bestCost, found
}

// unitRule checks the rules that are measured across a priceable unit. It
// returns the reason and which component's fare refused.
func (p *pricing) unitRule(runs [][2]int, members []int, pick []candidate, kind UnitKind, shared bool) (string, int) {
	segs := p.req.Segments
	firstDep := segs[runs[members[0]][0]].Depart
	lastDep := segs[runs[members[len(members)-1]][0]].Depart
	stay, measured := 0, kind != OneWayUnit
	satNight := false
	if measured {
		stay = int(lastDep.Sub(firstDep).Hours() / 24)
		satNight = saturdayNight(firstDep, lastDep)
	} else if n := p.n; n > 1 && strings.EqualFold(segs[n-1].Destination, segs[0].Origin) {
		// Used one way inside a journey that comes back: the journey's own
		// return is the stay.
		measured = true
		stay = int(segs[n-1].Depart.Sub(segs[0].Depart).Hours() / 24)
		satNight = saturdayNight(segs[0].Depart, segs[n-1].Depart)
	}
	for k, c := range pick {
		f, r := c.fare, c.fare.Rule
		if kind == OneWayUnit && f.RoundTrip {
			return "round trip required", k
		}
		if kind == OpenJawUnit && r.NoOpenJaw {
			return "open jaw not permitted", k
		}
		if shared && r.NoEndOnEnd {
			return "may not be combined with another fare", k
		}
		if r.AdvancePurchaseDays > 0 && firstDep.Sub(p.purchased) < time.Duration(r.AdvancePurchaseDays)*24*time.Hour {
			return fmt.Sprintf("%d-day advance purchase", r.AdvancePurchaseDays), k
		}
		if measured {
			if r.MinStayDays > 0 && stay < r.MinStayDays {
				return fmt.Sprintf("%d-day minimum stay", r.MinStayDays), k
			}
			if r.MaxStayDays > 0 && stay > r.MaxStayDays {
				return fmt.Sprintf("%d-day maximum stay", r.MaxStayDays), k
			}
			if r.SaturdayNight && !satNight {
				return "Saturday night stay required", k
			}
		}
		for o, d := range pick {
			if o == k {
				continue
			}
			if len(r.UnitCarriers) > 0 && !strings.EqualFold(d.fare.Carrier, f.Carrier) && !containsFold(r.UnitCarriers, d.fare.Carrier) {
				return "may not be combined with a " + strings.ToUpper(d.fare.Carrier) + " fare", k
			}
			if r.UnitSameBasis && !strings.EqualFold(d.fare.Basis, f.Basis) {
				return "may be combined only with the same fare", k
			}
		}
	}
	return "", 0
}

// saturdayNight reports whether a Saturday night falls between two
// moments: whether a Sunday begins after the first and at or before the
// second, on the first one's clock.
func saturdayNight(from, to time.Time) bool {
	to = to.In(from.Location())
	// The first Sunday midnight after from.
	d := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, from.Location())
	for i := 1; i <= 7; i++ {
		next := d.AddDate(0, 0, i)
		if next.Weekday() == time.Sunday {
			return !to.Before(next)
		}
	}
	return false
}

// classesOf is the class each segment was priced in, for the cheapest
// division, from the candidates chosen.
func (p *pricing) classesOf(runs [][2]int, comps []Component) []string {
	out := make([]string, p.n)
	for _, c := range comps {
		for k := c.From; k <= c.To; k++ {
			out[k] = p.classIn(c.Fare, k)
		}
	}
	return out
}

// classIn is the booking class a fare is sold in on one segment of its run.
func (p *pricing) classIn(f Fare, seg int) string {
	s := p.req.Segments[seg]
	if !strings.EqualFold(s.Carrier, f.Carrier) {
		if pc, ok := f.Rule.PartnerClass[strings.ToUpper(s.Carrier)]; ok {
			return strings.ToUpper(pc)
		}
	}
	return strings.ToUpper(f.Class)
}

// quote turns the cheapest division into the priced itinerary.
func (p *pricing) quote() *Quote {
	segs := p.req.Segments
	cur := p.bestSet[0].Fare.OneWay.Currency
	q := &Quote{Currency: cur, Components: p.bestSet, Units: p.bestUnit}
	// Each segment's share of its component, by miles when every segment
	// of the component carries them, equally otherwise; the odd cents go
	// to the first segment.
	type share struct {
		basis, class string
		amount       int64
	}
	shares := make([]share, p.n)
	for _, c := range p.bestSet {
		n := c.To - c.From + 1
		miles, known := 0, true
		for k := c.From; k <= c.To; k++ {
			if segs[k].Miles <= 0 {
				known = false
			}
			miles += segs[k].Miles
		}
		var given int64
		for k := c.From; k <= c.To; k++ {
			part := c.Amount.Amount / int64(n)
			if known && miles > 0 {
				part = c.Amount.Amount * int64(segs[k].Miles) / int64(miles)
			}
			shares[k] = share{basis: c.Fare.Basis, class: p.classes[k], amount: part}
			given += part
		}
		shares[c.From].amount += c.Amount.Amount - given
	}
	var taxes []Tax
	if tx, ok := p.t.(Taxer); ok {
		taxes = tx.TaxesOn(segs)
	} else {
		taxes = p.t.TaxesFor(segs[0].Carrier)
	}
	for _, pt := range p.req.Passengers {
		discount, ok := Discount[pt]
		if !ok {
			discount = 1
		}
		pq := PassengerQuote{Type: pt}
		for _, sh := range shares {
			base := Money{Amount: sh.amount, Currency: cur}.Scale(discount)
			pq.Segments = append(pq.Segments, SegmentFare{Basis: sh.basis, Class: sh.class, Base: base})
			pq.Base = pq.Base.Add(base)
		}
		for _, tx := range taxes {
			if pt == Infant && !tx.Infants {
				continue
			}
			line := TaxLine{Code: tx.Code, CarrierImposed: tx.CarrierImposed}
			switch tx.Kind {
			case PercentOfBase:
				line.Amount = pq.Base.Scale(tx.Percent / 100)
			case PerSegment:
				n := 0
				for _, s := range segs {
					if s.Miles > 0 && ((tx.MinMiles > 0 && s.Miles < tx.MinMiles) || (tx.MaxMiles > 0 && s.Miles > tx.MaxMiles)) {
						continue
					}
					n++
				}
				line.Amount = Money{Amount: tx.Amount.Amount * int64(n), Currency: tx.Amount.Currency}
			case PerTicket:
				line.Amount = tx.Amount
			case PerEnplanement:
				n := 0
				for _, s := range segs {
					if len(tx.Airports) == 0 || containsFold(tx.Airports, s.Origin) {
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
			if tx.CarrierImposed {
				pq.CarrierFees = pq.CarrierFees.Add(line.Amount)
				q.CarrierFees = q.CarrierFees.Add(line.Amount)
			}
		}
		pq.Total = pq.Total.Add(pq.Base)
		q.Base = q.Base.Add(pq.Base)
		q.Total = q.Total.Add(pq.Total)
		q.Passengers = append(q.Passengers, pq)
	}
	q.Calc = p.calc()
	return q
}

// calc is the fare calculation line: each point and carrier in order, an
// X/ before a point that is only a connection inside a component, the
// amount and basis after each component, and the total. A surface break
// prints as /-.
func (p *pricing) calc() string {
	segs := p.req.Segments
	var b strings.Builder
	b.WriteString(strings.ToUpper(segs[0].Origin))
	var total int64
	for ci, c := range p.bestSet {
		if ci > 0 && !strings.EqualFold(segs[c.From].Origin, segs[c.From-1].Destination) {
			b.WriteString(" /-" + strings.ToUpper(segs[c.From].Origin))
		}
		for k := c.From; k <= c.To; k++ {
			b.WriteString(" " + strings.ToUpper(segs[k].Carrier) + " ")
			if k < c.To {
				b.WriteString("X/")
			}
			b.WriteString(strings.ToUpper(segs[k].Destination))
		}
		fmt.Fprintf(&b, " %d.%02d%s", c.Fare.OneWay.Amount/100, c.Fare.OneWay.Amount%100, strings.ToUpper(c.Fare.Basis))
		if c.Surcharge.Amount != 0 {
			fmt.Fprintf(&b, " Q%d.%02d", c.Surcharge.Amount/100, c.Surcharge.Amount%100)
		}
		total += c.Amount.Amount
	}
	fmt.Fprintf(&b, " %s%d.%02d END", p.bestSet[0].Fare.OneWay.Currency, total/100, total%100)
	return b.String()
}

func hasDay(days []time.Weekday, d time.Weekday) bool {
	for _, x := range days {
		if x == d {
			return true
		}
	}
	return false
}

func containsFold(xs []string, x string) bool {
	for _, v := range xs {
		if strings.EqualFold(v, x) {
			return true
		}
	}
	return false
}
