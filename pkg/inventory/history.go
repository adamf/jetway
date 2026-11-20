package inventory

import (
	"encoding/binary"
	"errors"
	"math"
)

// A forecast learned from a flight's own departures.
//
// EMSRb needs the demand still to come by class. A revenue management
// system takes it from the bookings of the flight's earlier departures,
// and two things are wrong with those bookings as they stand.
//
// A class that was closed took no bookings while it was closed. Read as
// demand, the bookings understate it, the next forecast protects fewer
// seats, and the class closes sooner: the forecast feeds on its own
// controls. The cure is to unconstrain the record. Here that is done by
// exposure: bookings are divided by the share of the time they could be
// made, which is the maximum-likelihood rate of a Poisson stream that
// was observed part of the time.
//
// A traveller who would have paid a higher fare buys the lower one when
// it is open. Read class by class, the higher fare then has little
// demand, the forecast protects little for it, and the lower fare stays
// open longer: this is the spiral-down of Cooper, Homem-de-Mello and
// Kleywegt (Operations Research 54(5), 2006). The cure is the marginal
// revenue transformation of Fiig, Isler, Hopperstad and Belobaba
// ("Optimization of mixed fare structures", Journal of Revenue and
// Pricing Management 9, 2010). The record is kept not by class but by
// strategy: which class was the lowest on sale, for how long, and what
// was sold in all classes meanwhile. A strategy's total demand and total
// revenue place it on a plane. The strategies on the upper convex hull
// are the ones worth using, and each brings an increment of demand at a
// marginal revenue. Those increments are independent demands, which is
// what EMSRb assumes, so the optimiser needs no change.
//
// The record is by time frame as well, because both effects move through
// the booking window: who books 40 days out is not who books 4 days out.

// Method is how a History reads its record.
type Method int

const (
	// FareAdjusted reads the record by strategy and applies the marginal
	// revenue transformation. It is the default.
	FareAdjusted Method = iota
	// Unconstrained reads each class's bookings as that class's own
	// demand, divided by the share of the time the class was on sale. It
	// corrects for closure and not for buy-down.
	Unconstrained
	// SalesAsDemand reads each class's bookings as its demand. It is here
	// to be measured against: a forecaster that does this spirals down.
	SalesAsDemand
)

// History is the booking record of one flight's cabin over its past
// departures, and the forecast taken from it. The zero value is not
// usable; make one with NewHistory. A History is not safe for concurrent
// use.
type History struct {
	// Classes are the cabin's booking classes in fare order, highest
	// first.
	Classes []string
	// Frames is how many time frames the booking window is cut into,
	// counted from the earliest.
	Frames int
	// Memory is how many departures the forecast remembers: each new
	// departure is weighed against the record as one in (Memory+1)/2,
	// which is the weight a moving average over Memory departures gives
	// at its centre. Zero is 26.
	Memory float64
	// Spread is the variance of demand over its mean. Zero is one, a
	// Poisson stream's. Parties and day-to-day swings in demand make it
	// larger.
	Spread float64
	// Method is how the record is read.
	Method Method
	// Salt varies, strategy by strategy, how long the record goes on
	// believing a strategy it no longer sees. A carrier gives each
	// flight its own, so that its flights do not all try their closed
	// classes again on the same day.
	Salt uint64

	index  map[string]int
	n      int
	weight float64
	// The record, by frame and strategy. Strategy s is "class s is the
	// lowest on sale"; strategy len(Classes) is "nothing is on sale".
	open, booked, revenue []float32
	// Seats booked by frame and class.
	class []float32

	est []frameEstimate // nil when the record has changed
}

// NewHistory makes an empty record for a cabin's classes, highest fare
// first, over a booking window of the given number of time frames.
func NewHistory(classes []string, frames int) *History {
	h := &History{Classes: append([]string(nil), classes...), Frames: frames}
	h.alloc()
	return h
}

func (h *History) alloc() {
	h.index = make(map[string]int, len(h.Classes))
	for i, c := range h.Classes {
		h.index[c] = i
	}
	states := len(h.Classes) + 1
	h.open = make([]float32, h.Frames*states)
	h.booked = make([]float32, h.Frames*states)
	h.revenue = make([]float32, h.Frames*states)
	h.class = make([]float32, h.Frames*len(h.Classes))
}

// Departures is how many departures the record holds.
func (h *History) Departures() int { return h.n }

// Log is one departure being recorded. The caller reports how long each
// strategy was in force in each time frame and what was sold under it;
// History.Record then adds the departure to the record.
type Log struct {
	h                     *History
	open, booked, revenue []float64
	class                 []float64
}

// Begin starts the record of a departure.
func (h *History) Begin() *Log {
	states := len(h.Classes) + 1
	return &Log{h: h,
		open:    make([]float64, h.Frames*states),
		booked:  make([]float64, h.Frames*states),
		revenue: make([]float64, h.Frames*states),
		class:   make([]float64, h.Frames*len(h.Classes)),
	}
}

func (l *Log) state(frame int, lowest string) int {
	if frame < 0 || frame >= l.h.Frames {
		return -1
	}
	s := len(l.h.Classes)
	if lowest != "" {
		i, ok := l.h.index[lowest]
		if !ok {
			return -1
		}
		s = i
	}
	return frame*(len(l.h.Classes)+1) + s
}

// Expose reports that, for an amount of a time frame, lowest was the
// lowest class on sale; "" says nothing was. The amount is in any unit
// that is the same through a frame: seconds, or shoppers who looked. Only
// its share of the frame's total is kept.
func (l *Log) Expose(frame int, lowest string, amount float64) {
	if i := l.state(frame, lowest); i >= 0 && amount > 0 {
		l.open[i] += amount
	}
}

// Sale reports seats booked in a class during a time frame, while lowest
// was the lowest class on sale, and the fare revenue they brought.
func (l *Log) Sale(frame int, lowest, class string, seats int, revenue float64) {
	i := l.state(frame, lowest)
	c, ok := l.h.index[class]
	if i < 0 || !ok || seats <= 0 {
		return
	}
	l.booked[i] += float64(seats)
	l.revenue[i] += revenue
	l.class[frame*len(l.h.Classes)+c] += float64(seats)
}

// Record adds a departure to the record. What the record held is weighed
// down first, so the forecast follows a flight whose demand changes.
func (h *History) Record(l *Log) {
	if l == nil || l.h != h {
		return
	}
	memory := h.Memory
	if memory <= 0 {
		memory = 26
	}
	keep := float32(1 - 2/(memory+1))
	for i := range h.open {
		h.open[i] *= keep
		h.booked[i] *= keep
		h.revenue[i] *= keep
	}
	for i := range h.class {
		h.class[i] *= keep
	}
	h.weight = h.weight*float64(keep) + 1
	h.n++
	states := len(h.Classes) + 1
	for f := 0; f < h.Frames; f++ {
		var total float64
		for s := 0; s < states; s++ {
			total += l.open[f*states+s]
		}
		if total <= 0 {
			continue
		}
		for s := 0; s < states; s++ {
			i := f*states + s
			h.open[i] += float32(l.open[i] / total)
			h.booked[i] += float32(l.booked[i])
			h.revenue[i] += float32(l.revenue[i])
		}
		for c := range h.Classes {
			i := f*len(h.Classes) + c
			h.class[i] += float32(l.class[i])
		}
	}
	h.est = nil
}

// trusted is the least time on record under a strategy, in frames, for
// what was sold under it to be believed. Below it the strategy counts as
// never tried. The record is weighed down at each departure, so a
// strategy that is no longer used falls under this in time and is tried
// again: with the default memory, a strategy in force for one whole
// frame is believed for about 3 weeks of departures. An estimate.
const trusted = 0.2

// believed is the time on record a strategy needs in one frame: trusted,
// moved by up to half either way from strategy to strategy and from salt
// to salt. Strategies that were all last used on one day are then
// forgotten, and tried again, over 2 weeks and not on one day.
func (h *History) believed(frame, state int) float64 {
	// splitmix64 of the salt and the place.
	z := h.Salt + uint64(frame)*0x9e3779b97f4a7c15 + uint64(state)*0xbf58476d1ce4e5b9 + 0x94d049bb133111eb
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	z ^= z >> 31
	return trusted * (0.5 + float64(z>>11)/float64(1<<53))
}

// frameEstimate is what the record says of one time frame.
type frameEstimate struct {
	// By class, for the fare-adjusted reading: the demand that opening the
	// class adds and what a sale in it is worth. A value that is not a
	// number says the record does not know, and the class is worth its
	// fare; minus infinity says the class should not be opened.
	demand, value []float64
	// rate is each class's own bookings a frame, for the methods that
	// read class by class; known says the class was on sale to be booked.
	rate  []float64
	known []bool
}

func (h *History) estimates() []frameEstimate {
	if h.est != nil {
		return h.est
	}
	n := len(h.Classes)
	states := n + 1
	est := make([]frameEstimate, h.Frames)
	for f := range est {
		e := frameEstimate{demand: make([]float64, n), value: make([]float64, n), rate: make([]float64, n), known: make([]bool, n)}
		// Class by class. A class is on sale under every strategy whose
		// lowest class is it or one below it.
		var onSale float64
		for c := n - 1; c >= 0; c-- {
			e.value[c] = math.NaN()
			onSale += float64(h.open[f*states+c])
			booked := float64(h.class[f*n+c])
			switch h.Method {
			case SalesAsDemand:
				if h.weight > 0 {
					e.rate[c], e.known[c] = booked/h.weight, true
				}
			default:
				if onSale >= trusted {
					e.rate[c], e.known[c] = booked/onSale, true
				}
			}
		}
		// Strategy by strategy. Opening one more class cannot draw less
		// demand, so strategies whose record says it does are pooled
		// (the pool-adjacent-violators rule), weighed by their time.
		// Each keeps its own average fare.
		type block struct {
			open, booked, revenue float64
			members               []int
		}
		var blocks []block
		for s := 0; s < n; s++ {
			i := f*states + s
			if float64(h.open[i]) < h.believed(f, s) {
				continue
			}
			blocks = append(blocks, block{float64(h.open[i]), float64(h.booked[i]), float64(h.revenue[i]), []int{s}})
			for k := len(blocks) - 1; k > 0 && blocks[k-1].booked*blocks[k].open > blocks[k].booked*blocks[k-1].open; k-- {
				blocks[k-1].open += blocks[k].open
				blocks[k-1].booked += blocks[k].booked
				blocks[k-1].revenue += blocks[k].revenue
				blocks[k-1].members = append(blocks[k-1].members, blocks[k].members...)
				blocks = blocks[:k]
			}
		}
		var q, tr []float64
		var at []int
		for _, b := range blocks {
			for _, s := range b.members {
				i := f*states + s
				paid := 0.0
				switch {
				case h.booked[i] > 0:
					paid = float64(h.revenue[i]) / float64(h.booked[i])
				case b.booked > 0:
					paid = b.revenue / b.booked
				}
				q = append(q, b.booked/b.open)
				tr = append(tr, b.booked/b.open*paid)
				at = append(at, s)
			}
		}
		demand, fare := MarginalRevenue(q, tr)
		for k, s := range at {
			e.demand[s], e.value[s] = demand[k], fare[k]
		}
		est[f] = e
	}
	h.est = est
	return est
}

// MarginalRevenue is the marginal revenue transformation of Fiig, Isler,
// Hopperstad and Belobaba (2010). Strategy k has one more class on sale
// than strategy k-1, in fare order from the highest. q[k] is the total
// demand strategy k draws in all its classes and tr[k] the total revenue.
// The strategies on the upper convex hull of those points, from the
// origin, are the efficient ones. For each of them the result gives the
// demand it adds to the efficient strategy before it and the revenue
// each unit of that demand adds: the partitioned demand and the adjusted
// fare. Demands that are independent of each other and priced at the
// adjusted fares trace the same hull, so an optimiser that assumes
// independent demand, given these, controls the original fares
// correctly.
//
// With independent demands the transformation changes nothing. With
// travellers who all buy the lowest fare on sale it gives f(k)Q(k) -
// f(k-1)Q(k-1) over Q(k) - Q(k-1), which goes negative once a lower fare
// loses more to buy-down than it gains.
//
// A strategy under the hull has no demand of its own. It takes the
// adjusted fare of the next efficient strategy, so that it opens only
// with it, or minus infinity when there is none: it should not be used.
// A strategy that draws exactly what an efficient one before it draws
// takes that one's adjusted fare. A strategy that draws nothing has no
// adjusted fare, and the result is not a number.
//
// q must not decrease.
func MarginalRevenue(q, tr []float64) (demand, fare []float64) {
	n := len(q)
	demand, fare = make([]float64, n), make([]float64, n)
	// The upper hull, as indexes into q and tr; -1 is the origin.
	x := func(i int) float64 {
		if i < 0 {
			return 0
		}
		return q[i]
	}
	y := func(i int) float64 {
		if i < 0 {
			return 0
		}
		return tr[i]
	}
	hull := []int{-1}
	for i := 0; i < n; i++ {
		top := hull[len(hull)-1]
		if x(i) <= x(top) {
			if y(i) <= y(top) || top < 0 {
				continue // no more demand, and no more revenue for it
			}
			hull = hull[:len(hull)-1]
		}
		for len(hull) >= 2 {
			a, b := hull[len(hull)-2], hull[len(hull)-1]
			// b is on the hull only if it lies above the line from a to i.
			if (x(b)-x(a))*(y(i)-y(a)) < (y(b)-y(a))*(x(i)-x(a)) {
				break
			}
			hull = hull[:len(hull)-1]
		}
		hull = append(hull, i)
	}
	on := make([]bool, n)
	for k := 1; k < len(hull); k++ {
		a, b := hull[k-1], hull[k]
		on[b] = true
		demand[b] = x(b) - x(a)
		fare[b] = (y(b) - y(a)) / demand[b]
	}
	k := 0 // hull[k] is the last hull point at or before strategy i
	for i := 0; i < n; i++ {
		for k+1 < len(hull) && hull[k+1] <= i {
			k++
		}
		if on[i] {
			continue
		}
		a := hull[k]
		// The hull's revenue at this strategy's demand.
		at, next := y(a), math.Inf(-1)
		if k+1 < len(hull) {
			b := hull[k+1]
			next = fare[b]
			at += next * (x(i) - x(a))
		}
		switch {
		case y(i) < at-1e-9*math.Max(1, math.Abs(at)):
			fare[i] = next
		case x(i) > x(a):
			fare[i] = next // on the segment to the next efficient strategy
		case a < 0:
			fare[i] = math.NaN()
		default:
			fare[i] = fare[a]
		}
	}
	return
}

// Control is how one class is to be sold during one time frame.
type Control struct {
	Class string
	// Value is what a seat sold in the class is worth to the carrier in
	// this frame: the marginal revenue of having the class on sale, or
	// its fare where the record does not say.
	Value float64
	// Protect is the seats to hold back from the class for demand still
	// to come that is worth more. The class may be sold while the seats
	// left exceed it.
	Protect float64
	// Closed says having the class on sale in this frame is not expected
	// to add revenue.
	Closed bool
}

// view is one frame's classes as the optimiser sees them: the value of a
// sale in each, the demand that comes with opening each, and the classes
// not to open.
func (h *History) view(e frameEstimate, fares []float64) (value, demand []float64, closed []bool) {
	n := len(h.Classes)
	value, demand, closed = make([]float64, n), make([]float64, n), make([]bool, n)
	for c := 0; c < n; c++ {
		value[c] = fares[c]
		if h.Method != FareAdjusted {
			if e.known[c] {
				demand[c] = e.rate[c]
			}
			continue
		}
		// A class the record does not know is worth its fare until the
		// record says otherwise.
		if v := e.value[c]; !math.IsNaN(v) {
			value[c], demand[c] = v, e.demand[c]
			closed[c] = v <= 0
		}
	}
	return
}

// Controls is how to sell each class from a time frame on, highest class
// first. share is the part of that frame still to come, one at its start.
// fare gives each class's fare, in the unit the revenue was recorded in.
//
// The protection is EMSRb's. For each class, the demand still to come
// that is worth more is pooled, in this frame and every later one, and
// seats are held for the pool up to the point where the chance of
// selling one more equals the ratio of the class's value to the pool's
// mean value.
func (h *History) Controls(frame int, share float64, fare func(class string) float64) []Control {
	n := len(h.Classes)
	out := make([]Control, n)
	for c := range out {
		out[c].Class = h.Classes[c]
	}
	if frame < 0 || frame >= h.Frames {
		return out
	}
	share = math.Max(0, math.Min(1, share))
	fares := make([]float64, n)
	for c, cl := range h.Classes {
		fares[c] = fare(cl)
	}
	spread := h.Spread
	if spread <= 0 {
		spread = 1
	}
	est := h.estimates()
	type increment struct{ demand, value float64 }
	var pool []increment
	var now []float64
	var shut []bool
	for f := frame; f < h.Frames; f++ {
		value, demand, closed := h.view(est[f], fares)
		scale := 1.0
		if f == frame {
			now, shut, scale = value, closed, share
		}
		for c := 0; c < n; c++ {
			if d := demand[c] * scale; d > 0 && value[c] > 0 {
				pool = append(pool, increment{d, value[c]})
			}
		}
	}
	for c := 0; c < n; c++ {
		out[c].Value = now[c]
		if shut[c] || now[c] <= 0 {
			out[c].Closed = true
			continue
		}
		var mean, revenue float64
		for _, i := range pool {
			if i.value > now[c] {
				mean += i.demand
				revenue += i.demand * i.value
			}
		}
		if mean <= 0 {
			continue
		}
		// P(pool demand > protect) = value / the pool's mean value.
		p := now[c] / (revenue / mean)
		out[c].Protect = math.Max(0, mean+math.Sqrt(spread*mean)*normalQuantile(1-p))
	}
	return out
}

// ToCome is the demand still to come from a time frame on, as EMSRb
// takes it: for each class the demand that opening it adds, the mean
// value of that demand, and its spread. share is the part of the frame
// still to come.
func (h *History) ToCome(frame int, share float64, fare func(class string) float64) []ClassDemand {
	n := len(h.Classes)
	fares := make([]float64, n)
	for c, cl := range h.Classes {
		fares[c] = fare(cl)
	}
	spread := h.Spread
	if spread <= 0 {
		spread = 1
	}
	share = math.Max(0, math.Min(1, share))
	mean, revenue := make([]float64, n), make([]float64, n)
	est := h.estimates()
	for f := max(frame, 0); f < h.Frames; f++ {
		value, demand, _ := h.view(est[f], fares)
		scale := 1.0
		if f == frame {
			scale = share
		}
		for c := 0; c < n; c++ {
			if d := demand[c] * scale; d > 0 && value[c] > 0 {
				mean[c] += d
				revenue[c] += d * value[c]
			}
		}
	}
	out := make([]ClassDemand, 0, n)
	for c := 0; c < n; c++ {
		d := ClassDemand{Class: h.Classes[c], Fare: fares[c]}
		if mean[c] > 0 {
			d.Fare, d.Mean, d.StdDev = revenue[c]/mean[c], mean[c], math.Sqrt(spread*mean[c])
		}
		out = append(out, d)
	}
	return out
}

// Authorizations turns a frame's controls into the nested authorisations
// an Inventory sells under: each class may take the cabin less the seats
// sold in the classes above it and less its protection. A closed class
// may take none.
func Authorizations(seats int, sold map[string]int, controls []Control) []Level {
	out := make([]Level, 0, len(controls))
	above := 0
	for i, c := range controls {
		auth := seats - above - int(math.Round(c.Protect))
		if c.Closed || auth < 0 {
			auth = 0
		}
		if i > 0 && auth > out[i-1].Authorized {
			auth = out[i-1].Authorized
		}
		out = append(out, Level{Class: c.Class, Authorized: auth})
		above += sold[c.Class]
	}
	return out
}

// historyMagic leads a History's binary form.
const historyMagic = "JWH1"

// MarshalBinary is the record in a compact form, for a caller that keeps
// it across restarts. Memory, Spread and Method are the caller's and are
// not in it.
func (h *History) MarshalBinary() ([]byte, error) {
	b := []byte(historyMagic)
	b = binary.AppendUvarint(b, uint64(len(h.Classes)))
	for _, c := range h.Classes {
		b = binary.AppendUvarint(b, uint64(len(c)))
		b = append(b, c...)
	}
	b = binary.AppendUvarint(b, uint64(h.Frames))
	b = binary.AppendUvarint(b, uint64(h.n))
	b = binary.LittleEndian.AppendUint64(b, math.Float64bits(h.weight))
	for _, a := range [][]float32{h.open, h.booked, h.revenue, h.class} {
		for _, v := range a {
			b = binary.LittleEndian.AppendUint32(b, math.Float32bits(v))
		}
	}
	return b, nil
}

// UnmarshalBinary reads the form MarshalBinary writes.
func (h *History) UnmarshalBinary(b []byte) error {
	bad := errors.New("inventory: not a history record")
	if len(b) < len(historyMagic) || string(b[:len(historyMagic)]) != historyMagic {
		return bad
	}
	b = b[len(historyMagic):]
	uv := func() (uint64, bool) {
		v, k := binary.Uvarint(b)
		if k <= 0 {
			return 0, false
		}
		b = b[k:]
		return v, true
	}
	nc, ok := uv()
	if !ok || nc > 64 {
		return bad
	}
	classes := make([]string, nc)
	for i := range classes {
		l, ok := uv()
		if !ok || uint64(len(b)) < l {
			return bad
		}
		classes[i], b = string(b[:l]), b[l:]
	}
	frames, ok := uv()
	if !ok || frames > 4096 {
		return bad
	}
	n, ok := uv()
	if !ok || len(b) < 8 {
		return bad
	}
	weight := math.Float64frombits(binary.LittleEndian.Uint64(b))
	b = b[8:]
	states := int(nc) + 1
	want := 4 * (3*int(frames)*states + int(frames)*int(nc))
	if len(b) != want {
		return bad
	}
	h.Classes, h.Frames, h.n, h.weight, h.est = classes, int(frames), int(n), weight, nil
	h.alloc()
	for _, a := range [][]float32{h.open, h.booked, h.revenue, h.class} {
		for i := range a {
			a[i] = math.Float32frombits(binary.LittleEndian.Uint32(b))
			b = b[4:]
		}
	}
	return nil
}
