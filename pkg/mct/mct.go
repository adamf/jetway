// Package mct is the minimum connecting time: the shortest interval in
// which a passenger and their bags can get from one flight to the next at
// a station, which every reservations and ticketing system is required to
// respect when it builds a connection (IATA Resolution 765).
//
// The data is three kinds of record. A station standard is what the
// airlines at an airport agree for each of the four status pairs --
// domestic to domestic, domestic to international, and so on. An
// exception is a carrier's own interval for some of its connections,
// shorter or longer, qualified by carrier, codeshare, flight number,
// terminal, aircraft, and where the passenger came from or goes on to. A
// suppression forbids a connection outright. Many records can match one
// connection, and the standard says which wins: a fixed priority order
// over the data elements, departure side before arrival side, in which
// the record that fills an element the other leaves blank is the more
// specific. This package holds the records, reads and writes the
// transmission file, and answers the question for a connection.
//
// It follows IATA's Standard Schedules Information Manual, chapter 8
// (issue of March 2021), which IATA publishes free: the 200-byte record
// layouts of section 8.9.3 byte for byte, the hierarchy table of section
// 8.6, the global defaults and tie rules of section 8.10, and the
// connection-building filter effective 1 November 2022. The tests use the
// chapter's own sample records and its worked examples. No station's
// times are carried here: the file is the aggregators' (OAG, Cirium) and
// whoever deploys this supplies it.
package mct

import (
	"fmt"
	"strings"
	"time"
)

// The four international/domestic status pairs: the status of the
// arriving flight, then of the departing one.
const (
	DD = "DD"
	DI = "DI"
	ID = "ID"
	II = "II"
)

// Record is one MCT record: a station standard, an exception or a
// suppression. An empty field does not qualify the record.
type Record struct {
	// Arrival and Departure are the stations; they differ for a connection
	// between two airports. Both are empty only on a geographic
	// suppression.
	Arrival, Departure string
	// Minutes is the interval. Suppression records carry none.
	Minutes int
	// Status is DD, DI, ID or II.
	Status string
	// The arriving flight's carrier, whether the record is for codeshare
	// flights, and the carrier that operates them.
	ArrCarrier   string
	ArrCodeshare bool
	ArrOperating string
	// The same for the departing flight.
	DepCarrier   string
	DepCodeshare bool
	DepOperating string
	// Aircraft are IATA three-character types and must match the schedule
	// exactly; Body is W (wide) or N (narrow). A record has one or the
	// other for a flight, never both.
	ArrAircraft, DepAircraft string
	ArrBody, DepBody         string
	ArrTerminal, DepTerminal string
	// Where the passenger came from and goes on to: a station, or a state
	// with its country, or a country, or a region. Only one form each.
	PrevCountry, PrevStation, PrevState, PrevRegion string
	NextCountry, NextStation, NextState, NextRegion string
	// Flight number ranges, both ends included; zero is not set.
	ArrFlightFrom, ArrFlightTo int
	DepFlightFrom, DepFlightTo int
	// From and To bound the local dates of the departing flight.
	From, To time.Time
	// Suppress forbids the connection. The three fields name the area a
	// geographic suppression covers; all empty with no stations is the
	// world.
	Suppress                                       bool
	SuppressRegion, SuppressCountry, SuppressState string
	// Submitter, Filed and Action are the aggregator's: who filed it,
	// when, and A (add) or D (delete) in an update file.
	Submitter string
	Filed     time.Time
	Action    string
}

// StationStandard reports whether the record names no carrier.
func (r *Record) StationStandard() bool {
	return r.ArrCarrier == "" && r.ArrOperating == "" && r.DepCarrier == "" && r.DepOperating == ""
}

// Flight is one side of a connection as the schedule publishes it.
type Flight struct {
	// Carrier and Number are the flight as it is sold. Operating and
	// OperatingNumber are set when the schedule says another carrier
	// flies it (a DEI 50 on the leg).
	Carrier         string
	Number          int
	Operating       string
	OperatingNumber int
	Aircraft        string // IATA type, e.g. 320
	Body            string // W or N
	Terminal        string
	// The other end of the flight: where the arriving flight came from,
	// or where the departing one goes.
	Station, State, Country, Region string
}

func (f Flight) codeshare() bool {
	return f.Operating != "" && !strings.EqualFold(f.Operating, f.Carrier)
}

// Connection is a passenger changing from one flight to another.
type Connection struct {
	Arrival, Departure string // the stations; equal at one airport
	Status             string // DD, DI, ID or II
	In, Out            Flight
	// Date is the local date of the departing flight.
	Date time.Time
	// Country, State and Region are the connecting station's, for the
	// suppressions filed over an area.
	Country, State, Region string
}

// Answer is what the table says about a connection.
type Answer struct {
	// Minutes is the minimum connecting time. Zero with Suppressed set.
	Minutes int
	// Suppressed says the connection may not be built at all.
	Suppressed bool
	// Record is the record that decided, nil when no record matched and
	// the global default applied.
	Record *Record
	// NotPartners says the connection-building filter refused the pair
	// before any record was read.
	NotPartners bool
}

// Global defaults (section 8.10.2), in minutes.
const (
	DefaultDD           = 30
	DefaultDI           = 60
	DefaultID           = 90
	DefaultII           = 90
	DefaultInterStation = 240
)

// Table answers for connections from a set of records.
type Table struct {
	byStation map[string][]*Record
	global    []*Record
	// partners is the connection-building filter: for each carrier that
	// filed a list, the carriers it accepts.
	partners map[string]map[string]bool
}

// NewTable indexes records. Records marked for deletion are left out.
func NewTable(records []Record) *Table {
	t := &Table{byStation: map[string][]*Record{}, partners: map[string]map[string]bool{}}
	for i := range records {
		t.Add(records[i])
	}
	return t
}

// Add adds one record.
func (t *Table) Add(r Record) {
	if strings.EqualFold(r.Action, "D") {
		return
	}
	rec := r
	if rec.Arrival == "" && rec.Departure == "" {
		t.global = append(t.global, &rec)
		return
	}
	k := strings.ToUpper(rec.Arrival + "/" + rec.Departure)
	t.byStation[k] = append(t.byStation[k], &rec)
}

// SetPartners files a carrier's list for the connection-building filter:
// the only carriers it accepts interline connections with.
func (t *Table) SetPartners(carrier string, partners []string) {
	m := map[string]bool{}
	for _, p := range partners {
		m[strings.ToUpper(p)] = true
	}
	t.partners[strings.ToUpper(carrier)] = m
}

// permitted applies the connection-building filter (section 8.2). A
// carrier that has filed a list connects only with the carriers on it; a
// carrier with no list accepts everyone; a carrier always connects to
// itself. The marketing and the operating carrier of each flight are all
// tested, so a flight sold by one carrier and flown by another must be
// acceptable under both names.
func (t *Table) permitted(c Connection) bool {
	sides := func(f Flight) []string {
		out := []string{strings.ToUpper(f.Carrier)}
		if f.codeshare() {
			out = append(out, strings.ToUpper(f.Operating))
		}
		return out
	}
	for _, a := range sides(c.In) {
		for _, b := range sides(c.Out) {
			if a == b {
				continue
			}
			if la, ok := t.partners[a]; ok && !la[b] {
				return false
			}
			if lb, ok := t.partners[b]; ok && !lb[a] {
				return false
			}
		}
	}
	return true
}

// Lookup is the minimum connecting time for a connection: the most
// specific record that applies, by the hierarchy; the global default when
// none does.
func (t *Table) Lookup(c Connection) Answer {
	if !t.permitted(c) {
		return Answer{Suppressed: true, NotPartners: true}
	}
	var best *Record
	consider := func(recs []*Record) {
		for _, r := range recs {
			if !r.applies(c) {
				continue
			}
			if best == nil || outranks(r, best) {
				best = r
			}
		}
	}
	consider(t.byStation[strings.ToUpper(c.Arrival+"/"+c.Departure)])
	consider(t.global)
	if best == nil {
		return Answer{Minutes: globalDefault(c)}
	}
	if best.Suppress {
		return Answer{Suppressed: true, Record: best}
	}
	return Answer{Minutes: best.Minutes, Record: best}
}

// Check reports whether a connection with the given arrival and departure
// times may be built, and the answer it was held against.
func (t *Table) Check(c Connection, arrive, depart time.Time) (bool, Answer) {
	a := t.Lookup(c)
	if a.Suppressed {
		return false, a
	}
	return depart.Sub(arrive) >= time.Duration(a.Minutes)*time.Minute, a
}

func globalDefault(c Connection) int {
	if !strings.EqualFold(c.Arrival, c.Departure) {
		return DefaultInterStation
	}
	switch strings.ToUpper(c.Status) {
	case DD:
		return DefaultDD
	case DI:
		return DefaultDI
	case ID:
		return DefaultID
	}
	return DefaultII
}

// applies reports whether every field the record fills matches the
// connection.
func (r *Record) applies(c Connection) bool {
	if r.Status != "" && !strings.EqualFold(r.Status, c.Status) {
		return false
	}
	if r.Arrival != "" && !strings.EqualFold(r.Arrival, c.Arrival) {
		return false
	}
	if r.Departure != "" && !strings.EqualFold(r.Departure, c.Departure) {
		return false
	}
	if !carrierApplies(r.ArrCarrier, r.ArrCodeshare, r.ArrOperating, r.ArrFlightFrom, r.ArrFlightTo, c.In) {
		return false
	}
	if !carrierApplies(r.DepCarrier, r.DepCodeshare, r.DepOperating, r.DepFlightFrom, r.DepFlightTo, c.Out) {
		return false
	}
	if !eq(r.ArrAircraft, c.In.Aircraft) || !eq(r.DepAircraft, c.Out.Aircraft) || !eq(r.ArrBody, c.In.Body) || !eq(r.DepBody, c.Out.Body) {
		return false
	}
	if !eq(r.ArrTerminal, c.In.Terminal) || !eq(r.DepTerminal, c.Out.Terminal) {
		return false
	}
	if !eq(r.PrevStation, c.In.Station) || !eq(r.PrevState, c.In.State) || !eq(r.PrevCountry, c.In.Country) || !regionMatches(r.PrevRegion, c.In.Region) {
		return false
	}
	if !eq(r.NextStation, c.Out.Station) || !eq(r.NextState, c.Out.State) || !eq(r.NextCountry, c.Out.Country) || !regionMatches(r.NextRegion, c.Out.Region) {
		return false
	}
	if !c.Date.IsZero() {
		d := dateOnly(c.Date)
		if !r.From.IsZero() && d.Before(dateOnly(r.From)) {
			return false
		}
		if !r.To.IsZero() && d.After(dateOnly(r.To)) {
			return false
		}
	}
	if !eq(r.SuppressState, c.State) || !eq(r.SuppressCountry, c.Country) || !regionMatches(r.SuppressRegion, c.Region) {
		return false
	}
	return true
}

// carrierApplies holds one side of a record against one flight. A record
// without the codeshare indicator is for the carrier that operates the
// flight: it applies to the carrier's own flights and to flights it
// operates under another's code. A record with the indicator is for
// codeshare flights only: sold by its carrier, or operated by its
// operating carrier, or both when it names both. A flight number range
// counts the marketing flight on a codeshare record and the operating
// flight otherwise.
func carrierApplies(carrier string, share bool, operating string, from, to int, f Flight) bool {
	number := f.Number
	if share {
		if !f.codeshare() {
			return false
		}
		if carrier != "" && !strings.EqualFold(carrier, f.Carrier) {
			return false
		}
		if operating != "" && !strings.EqualFold(operating, f.Operating) {
			return false
		}
	} else {
		operator := f.Carrier
		if f.codeshare() {
			operator = f.Operating
			if f.OperatingNumber != 0 {
				number = f.OperatingNumber
			}
		}
		if carrier != "" && !strings.EqualFold(carrier, operator) {
			return false
		}
		if carrier == "" && (from != 0 || to != 0) {
			return false
		}
	}
	if from != 0 || to != 0 {
		if number < from || number > to {
			return false
		}
	}
	return true
}

func eq(field, value string) bool {
	return field == "" || strings.EqualFold(strings.TrimSpace(field), strings.TrimSpace(value))
}

// regionMatches holds a region field against a station's region. A
// station in the Schengen area is also in Europe, so a record for EUR
// applies to a Schengen station; the more specific SCH wins the tie.
func regionMatches(field, value string) bool {
	if field == "" || strings.EqualFold(field, value) {
		return true
	}
	return strings.EqualFold(field, "EUR") && strings.EqualFold(value, "SCH")
}

func dateOnly(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// filled lists, in the hierarchy's order (section 8.6), whether the
// record fills each of the 31 elements.
func (r *Record) filled() [31]bool {
	return [31]bool{
		r.DepCodeshare, r.DepCarrier != "", r.DepOperating != "",
		r.ArrCodeshare, r.ArrCarrier != "", r.ArrOperating != "",
		r.DepFlightFrom != 0, r.DepFlightTo != 0, r.ArrFlightFrom != 0, r.ArrFlightTo != 0,
		r.DepTerminal != "", r.ArrTerminal != "",
		r.NextStation != "", r.PrevStation != "", r.NextState != "", r.PrevState != "",
		r.NextCountry != "", r.PrevCountry != "", r.NextRegion != "", r.PrevRegion != "",
		r.DepAircraft != "", r.ArrAircraft != "", r.DepBody != "", r.ArrBody != "",
		!r.From.IsZero(), !r.To.IsZero(),
		r.Departure != "", r.Arrival != "",
		r.SuppressState != "", r.SuppressCountry != "", r.SuppressRegion != "",
	}
}

// outranks reports whether a is more applicable than b: the first element
// of the hierarchy that exactly one of them fills decides, for the one
// that fills it. Records that fill the same elements are separated by the
// tie rules of section 8.10.3: the narrower departure flight range, the
// narrower arrival range, then Schengen over Europe in the next, previous
// and suppression regions.
func outranks(a, b *Record) bool {
	fa, fb := a.filled(), b.filled()
	for i := range fa {
		if fa[i] != fb[i] {
			return fa[i]
		}
	}
	if wa, wb := a.DepFlightTo-a.DepFlightFrom, b.DepFlightTo-b.DepFlightFrom; wa != wb {
		return wa < wb
	}
	if wa, wb := a.ArrFlightTo-a.ArrFlightFrom, b.ArrFlightTo-b.ArrFlightFrom; wa != wb {
		return wa < wb
	}
	for _, pair := range [][2]string{{a.NextRegion, b.NextRegion}, {a.PrevRegion, b.PrevRegion}, {a.SuppressRegion, b.SuppressRegion}} {
		sa, sb := strings.EqualFold(pair[0], "SCH"), strings.EqualFold(pair[1], "SCH")
		if sa != sb {
			return sa
		}
	}
	return false
}

// String is the record in a line, for a log or a display.
func (r *Record) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s-%s %s", r.Arrival, r.Departure, r.Status)
	if r.Suppress {
		b.WriteString(" suppressed")
	} else {
		fmt.Fprintf(&b, " %02d%02d", r.Minutes/60, r.Minutes%60)
	}
	side := func(label, carrier string, share bool, op string) {
		if carrier == "" && op == "" {
			return
		}
		b.WriteString(" " + label + " " + carrier)
		if share {
			b.WriteString("*" + op)
		}
	}
	side("from", r.ArrCarrier, r.ArrCodeshare, r.ArrOperating)
	side("to", r.DepCarrier, r.DepCodeshare, r.DepOperating)
	return b.String()
}
