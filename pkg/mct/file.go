package mct

import (
	"bufio"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The transmission file (section 8.9): fixed 200-byte records. Type 1 is
// the header, type 2 an MCT record, type 3 a carrier's partner list for
// the connection-building filter and type 4 the trailer. Before 1
// November 2022 there were no partner lists and the trailer was type 3;
// Parse reads both and Write produces the current form.

const recordLength = 200

const title = "MINIMUM CONNECT TIME DATA SET"

// PartnerList is one carrier's list for the connection-building filter.
type PartnerList struct {
	Carrier  string
	Partners []string
}

// File is a transmission file.
type File struct {
	// Creator is the aggregator's free-format reference.
	Creator string
	// Created is when the file was made, in UTC.
	Created time.Time
	// Full says the file replaces everything held; otherwise it carries
	// adds and deletes, and each record's Action says which.
	Full     bool
	Records  []Record
	Partners []PartnerList
}

// Table indexes the file's records and partner lists.
func (f *File) Table() *Table {
	t := NewTable(f.Records)
	merged := map[string][]string{}
	for _, p := range f.Partners {
		merged[strings.ToUpper(p.Carrier)] = append(merged[strings.ToUpper(p.Carrier)], p.Partners...)
	}
	for c, ps := range merged {
		t.SetPartners(c, ps)
	}
	return t
}

// field is bytes from..to of a record, counted from one as the manual
// counts them, with the padding trimmed.
func field(line string, from, to int) string {
	if from > len(line) {
		return ""
	}
	if to > len(line) {
		to = len(line)
	}
	return strings.TrimSpace(line[from-1 : to])
}

func parseDate(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse("02Jan06", s[:2]+strings.ToUpper(s[2:3])+strings.ToLower(s[3:5])+s[5:])
}

func formatDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return strings.ToUpper(t.Format("02Jan06"))
}

// Parse reads a transmission file. A record that does not follow the
// layout stops the read with its line number: a connecting time
// misread is a passenger who misses a flight.
func Parse(r io.Reader) (*File, error) {
	f := &File{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1024), 1<<20)
	n, serial := 0, 0
	sawHeader, sawTrailer := false, false
	for sc.Scan() {
		n++
		line := strings.TrimRight(sc.Text(), "\r\n")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if len(line) > recordLength {
			return nil, fmt.Errorf("mct: line %d: %d bytes, a record is %d", n, len(line), recordLength)
		}
		if sawTrailer {
			return nil, fmt.Errorf("mct: line %d: data after the trailer", n)
		}
		// Header and MCT records are numbered in order. A partner list and
		// the trailer carry a check reference instead.
		if s := field(line, 195, 200); s != "" && (line[0] == '1' || line[0] == '2') {
			got, err := strconv.Atoi(s)
			if err != nil || got != serial+1 {
				return nil, fmt.Errorf("mct: line %d: record serial number %q, want %06d", n, s, serial+1)
			}
			serial = got
		}
		switch line[0] {
		case '1':
			if field(line, 2, 31) != title {
				return nil, fmt.Errorf("mct: line %d: not an MCT data set: %q", n, field(line, 2, 31))
			}
			sawHeader = true
			f.Creator = field(line, 32, 66)
			if d := field(line, 67, 73); d != "" {
				day, err := parseDate(d)
				if err != nil {
					return nil, fmt.Errorf("mct: line %d: creation date %q", n, d)
				}
				hh, mm := 0, 0
				if tm := field(line, 74, 77); len(tm) == 4 {
					hh, _ = strconv.Atoi(tm[:2])
					mm, _ = strconv.Atoi(tm[2:])
				}
				f.Created = day.Add(time.Duration(hh)*time.Hour + time.Duration(mm)*time.Minute)
			}
			switch field(line, 78, 78) {
			case "F":
				f.Full = true
			case "U":
			default:
				return nil, fmt.Errorf("mct: line %d: content indicator %q is neither F nor U", n, field(line, 78, 78))
			}
		case '2':
			if !sawHeader {
				return nil, fmt.Errorf("mct: line %d: a record before the header", n)
			}
			rec, err := parseRecord(line)
			if err != nil {
				return nil, fmt.Errorf("mct: line %d: %w", n, err)
			}
			f.Records = append(f.Records, rec)
		case '3':
			if field(line, 2, 4) == "" {
				// The trailer of the format valid to 31 October 2022.
				if err := checkTrailer(line, serial); err != nil {
					return nil, fmt.Errorf("mct: line %d: %w", n, err)
				}
				sawTrailer = true
				continue
			}
			pl := PartnerList{Carrier: field(line, 2, 4)}
			body := line[4:]
			if len(body) > 190 {
				body = body[:190]
			}
			for i := 0; i+2 <= len(body); i += 2 {
				if c := strings.TrimSpace(body[i : i+2]); c != "" {
					pl.Partners = append(pl.Partners, c)
				}
			}
			f.Partners = append(f.Partners, pl)
		case '4':
			if err := checkTrailer(line, serial); err != nil {
				return nil, fmt.Errorf("mct: line %d: %w", n, err)
			}
			sawTrailer = true
		default:
			return nil, fmt.Errorf("mct: line %d: record type %q", n, line[:1])
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if !sawHeader {
		return nil, fmt.Errorf("mct: no header record")
	}
	if !sawTrailer {
		return nil, fmt.Errorf("mct: no trailer record: the file is cut short")
	}
	return f, nil
}

// checkTrailer holds the trailer's end code and its serial number check
// reference, which repeats the serial of the record before it.
func checkTrailer(line string, serial int) error {
	if len(line) < 194 || line[193] != 'E' {
		return fmt.Errorf("trailer without its end code")
	}
	if s := field(line, 195, 200); s != "" {
		got, err := strconv.Atoi(s)
		if err != nil || got != serial {
			return fmt.Errorf("trailer checks serial %q, the last record was %06d", s, serial)
		}
	}
	return nil
}

func parseRecord(line string) (Record, error) {
	var r Record
	var err error
	r.Arrival = field(line, 2, 4)
	if t := field(line, 5, 8); t != "" {
		if len(t) != 4 {
			return r, fmt.Errorf("time %q is not HHMM", t)
		}
		hh, e1 := strconv.Atoi(t[:2])
		mm, e2 := strconv.Atoi(t[2:])
		if e1 != nil || e2 != nil || mm > 59 {
			return r, fmt.Errorf("time %q is not HHMM", t)
		}
		r.Minutes = hh*60 + mm
	}
	r.Status = field(line, 9, 10)
	r.Departure = field(line, 11, 13)
	r.ArrCarrier = field(line, 14, 15)
	r.ArrCodeshare = field(line, 16, 16) == "Y"
	r.ArrOperating = field(line, 17, 18)
	r.DepCarrier = field(line, 19, 20)
	r.DepCodeshare = field(line, 21, 21) == "Y"
	r.DepOperating = field(line, 22, 23)
	r.ArrAircraft, r.ArrBody = field(line, 24, 26), field(line, 27, 27)
	r.DepAircraft, r.DepBody = field(line, 28, 30), field(line, 31, 31)
	r.ArrTerminal, r.DepTerminal = field(line, 32, 33), field(line, 34, 35)
	r.PrevCountry, r.PrevStation = field(line, 36, 37), field(line, 38, 40)
	r.NextCountry, r.NextStation = field(line, 41, 42), field(line, 43, 45)
	num := func(from, to int) (int, error) {
		s := field(line, from, to)
		if s == "" {
			return 0, nil
		}
		return strconv.Atoi(s)
	}
	if r.ArrFlightFrom, err = num(46, 49); err != nil {
		return r, fmt.Errorf("arrival flight number range: %w", err)
	}
	if r.ArrFlightTo, err = num(50, 53); err != nil {
		return r, fmt.Errorf("arrival flight number range: %w", err)
	}
	if r.DepFlightFrom, err = num(54, 57); err != nil {
		return r, fmt.Errorf("departure flight number range: %w", err)
	}
	if r.DepFlightTo, err = num(58, 61); err != nil {
		return r, fmt.Errorf("departure flight number range: %w", err)
	}
	r.PrevState, r.NextState = field(line, 62, 63), field(line, 64, 65)
	r.PrevRegion, r.NextRegion = field(line, 66, 68), field(line, 69, 71)
	if r.From, err = parseDate(field(line, 72, 78)); err != nil {
		return r, fmt.Errorf("effective from date %q", field(line, 72, 78))
	}
	if r.To, err = parseDate(field(line, 79, 85)); err != nil {
		return r, fmt.Errorf("effective to date %q", field(line, 79, 85))
	}
	r.Suppress = field(line, 87, 87) == "Y"
	r.SuppressRegion, r.SuppressCountry, r.SuppressState = field(line, 88, 90), field(line, 91, 92), field(line, 93, 94)
	r.Submitter = field(line, 95, 96)
	if r.Filed, err = parseDate(field(line, 97, 103)); err != nil {
		return r, fmt.Errorf("filing date %q", field(line, 97, 103))
	}
	r.Action = field(line, 104, 104)
	return r, r.validate()
}

// validate holds a record to the conditions the manual puts on its
// fields.
func (r *Record) validate() error {
	switch r.Status {
	case DD, DI, ID, II:
	default:
		return fmt.Errorf("status %q is not DD, DI, ID or II", r.Status)
	}
	for _, f := range []struct {
		what, v string
		n       int
	}{{"station", r.Arrival, 3}, {"station", r.Departure, 3}, {"carrier", r.ArrCarrier, 2}, {"carrier", r.DepCarrier, 2},
		{"operating carrier", r.ArrOperating, 2}, {"operating carrier", r.DepOperating, 2}, {"terminal", r.ArrTerminal, 2}, {"terminal", r.DepTerminal, 2},
		{"aircraft type", r.ArrAircraft, 3}, {"aircraft type", r.DepAircraft, 3}, {"country", r.PrevCountry, 2}, {"country", r.NextCountry, 2},
		{"state", r.PrevState, 2}, {"state", r.NextState, 2}, {"region", r.PrevRegion, 3}, {"region", r.NextRegion, 3},
		{"station", r.PrevStation, 3}, {"station", r.NextStation, 3}, {"submitting carrier", r.Submitter, 2},
		{"suppression region", r.SuppressRegion, 3}, {"suppression country", r.SuppressCountry, 2}, {"suppression state", r.SuppressState, 2}} {
		if len(f.v) > f.n {
			return fmt.Errorf("%s %q is longer than its %d bytes", f.what, f.v, f.n)
		}
	}
	for _, b := range []string{r.ArrBody, r.DepBody} {
		if b != "" && b != "W" && b != "N" {
			return fmt.Errorf("aircraft body %q is neither W nor N", b)
		}
	}
	if r.Minutes < 0 || r.Minutes > 99*60+59 {
		return fmt.Errorf("time of %d minutes does not fit HHMM", r.Minutes)
	}
	for _, n := range []int{r.ArrFlightFrom, r.ArrFlightTo, r.DepFlightFrom, r.DepFlightTo} {
		if n < 0 || n > 9999 {
			return fmt.Errorf("flight number %d does not fit four digits", n)
		}
	}
	switch {
	case !r.Suppress && r.Minutes == 0 && (r.Arrival != "" || r.Departure != ""):
		// A time of 0000 is not a connecting time anyone files.
		return fmt.Errorf("a record that is not a suppression needs a time")
	case !r.Suppress && r.Arrival == "":
		return fmt.Errorf("only a suppression may leave the station blank")
	case (r.Arrival == "") != (r.Departure == ""):
		return fmt.Errorf("arrival and departure station are filled together")
	case r.ArrAircraft != "" && r.ArrBody != "", r.DepAircraft != "" && r.DepBody != "":
		return fmt.Errorf("aircraft type and aircraft body may not be used together")
	case (r.ArrFlightFrom == 0) != (r.ArrFlightTo == 0), (r.DepFlightFrom == 0) != (r.DepFlightTo == 0):
		return fmt.Errorf("a flight number range needs both ends")
	case r.ArrFlightFrom > r.ArrFlightTo, r.DepFlightFrom > r.DepFlightTo:
		return fmt.Errorf("a flight number range runs backwards")
	case r.PrevState != "" && r.PrevCountry == "", r.NextState != "" && r.NextCountry == "":
		return fmt.Errorf("a state needs its country")
	case r.SuppressState != "" && r.SuppressCountry == "":
		return fmt.Errorf("a suppression state needs its country")
	case r.PrevStation != "" && (r.PrevCountry != "" || r.PrevRegion != ""), r.NextStation != "" && (r.NextCountry != "" || r.NextRegion != ""):
		return fmt.Errorf("a previous or next station may not be used with a country or a region")
	case r.PrevRegion != "" && r.PrevCountry != "", r.NextRegion != "" && r.NextCountry != "":
		return fmt.Errorf("a previous or next region may not be used with a country")
	case r.ArrCodeshare && r.ArrCarrier == "" && r.ArrOperating == "", r.DepCodeshare && r.DepCarrier == "" && r.DepOperating == "":
		return fmt.Errorf("a codeshare indicator needs a carrier or an operating carrier")
	case !r.ArrCodeshare && r.ArrOperating != "", !r.DepCodeshare && r.DepOperating != "":
		return fmt.Errorf("an operating carrier needs the codeshare indicator")
	}
	return nil
}

// Encode is the record as its 200 bytes, without the serial number, which
// belongs to its place in a file.
func (r *Record) Encode() (string, error) {
	if err := r.validate(); err != nil {
		return "", err
	}
	b := []byte(strings.Repeat(" ", recordLength))
	put := func(from int, s string) { copy(b[from-1:], strings.ToUpper(s)) }
	put(1, "2")
	put(2, r.Arrival)
	if !r.Suppress || r.Minutes > 0 {
		put(5, fmt.Sprintf("%02d%02d", r.Minutes/60, r.Minutes%60))
	}
	put(9, r.Status)
	put(11, r.Departure)
	put(14, r.ArrCarrier)
	if r.ArrCodeshare {
		put(16, "Y")
	}
	put(17, r.ArrOperating)
	put(19, r.DepCarrier)
	if r.DepCodeshare {
		put(21, "Y")
	}
	put(22, r.DepOperating)
	put(24, r.ArrAircraft)
	put(27, r.ArrBody)
	put(28, r.DepAircraft)
	put(31, r.DepBody)
	put(32, r.ArrTerminal)
	put(34, r.DepTerminal)
	put(36, r.PrevCountry)
	put(38, r.PrevStation)
	put(41, r.NextCountry)
	put(43, r.NextStation)
	flight := func(from int, n int) {
		if n != 0 {
			put(from, fmt.Sprintf("%04d", n))
		}
	}
	flight(46, r.ArrFlightFrom)
	flight(50, r.ArrFlightTo)
	flight(54, r.DepFlightFrom)
	flight(58, r.DepFlightTo)
	put(62, r.PrevState)
	put(64, r.NextState)
	put(66, r.PrevRegion)
	put(69, r.NextRegion)
	put(72, formatDate(r.From))
	put(79, formatDate(r.To))
	switch {
	case r.Suppress:
		put(87, "Y")
	case !r.StationStandard():
		put(87, "N")
	}
	put(88, r.SuppressRegion)
	put(91, r.SuppressCountry)
	put(93, r.SuppressState)
	put(95, r.Submitter)
	put(97, formatDate(r.Filed))
	put(104, r.Action)
	return string(b), nil
}

// Write writes the file in the current format: header, records, partner
// lists, trailer, each 200 bytes and a line end, numbered in order.
func (f *File) Write(w io.Writer) error {
	serial := 0
	emit := func(b []byte, numbered bool) error {
		if numbered {
			serial++
			copy(b[194:], fmt.Sprintf("%06d", serial))
		}
		_, err := w.Write(append(b, '\n'))
		return err
	}
	blank := func() []byte { return []byte(strings.Repeat(" ", recordLength)) }
	h := blank()
	h[0] = '1'
	copy(h[1:], title)
	creator := f.Creator
	if len(creator) > 35 {
		creator = creator[:35]
	}
	copy(h[31:], creator)
	if !f.Created.IsZero() {
		copy(h[66:], formatDate(f.Created.UTC()))
		copy(h[73:], f.Created.UTC().Format("1504"))
	}
	h[77] = 'U'
	if f.Full {
		h[77] = 'F'
	}
	if err := emit(h, true); err != nil {
		return err
	}
	for i := range f.Records {
		line, err := f.Records[i].Encode()
		if err != nil {
			return fmt.Errorf("mct: record %d (%s): %w", i+1, f.Records[i].String(), err)
		}
		if err := emit([]byte(line), true); err != nil {
			return err
		}
	}
	lists := append([]PartnerList(nil), f.Partners...)
	sort.SliceStable(lists, func(i, j int) bool { return lists[i].Carrier < lists[j].Carrier })
	for _, pl := range lists {
		// 95 partners fit one record; a longer list takes several.
		for start := 0; start == 0 || start < len(pl.Partners); start += 95 {
			b := blank()
			b[0] = '3'
			copy(b[1:], strings.ToUpper(pl.Carrier))
			end := min(start+95, len(pl.Partners))
			for k, p := range pl.Partners[start:end] {
				copy(b[4+2*k:], strings.ToUpper(p))
			}
			// A partner record repeats the serial of the record before it.
			copy(b[194:], fmt.Sprintf("%06d", serial))
			if err := emit(b, false); err != nil {
				return err
			}
		}
	}
	t := blank()
	t[0] = '4'
	t[193] = 'E'
	copy(t[194:], fmt.Sprintf("%06d", serial))
	return emit(t, false)
}
