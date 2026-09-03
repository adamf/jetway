package ssim

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// The SSIM chapter 7 file is the schedule as carriers, airports and
// distribution systems exchange it in bulk: fixed 200-character records,
// a header (type 1), one carrier record (type 2), a flight leg record per
// leg and period (type 3), segment data records for what a leg alone
// cannot say (type 4: codeshare partners, traffic restrictions), and a
// trailer (type 5). The manual is sold; the column layout below is the
// one two independent open-source parsers (sthonnard/ssimparser in R,
// wcagreen/rusty-ssim in Rust) implement identically, and this package
// reads and writes to it. That is inference from reproductions, not
// conformance: the fields both parsers name are here, positions they
// leave blank are left blank, and a record of a type this package does
// not know is kept verbatim as a fragment.

// File is one carrier's schedule as a chapter 7 file carries it.
type File struct {
	Carrier  string
	TimeMode TimeMode
	// Season is the IATA season, e.g. W25 or S26.
	Season string
	// From and To are the schedule's validity; Created and Released the
	// file's dates.
	From, To time.Time
	Created  time.Time
	Released time.Time
	Title    string
	// Status is the schedule status, C for a complete schedule.
	Status  string
	Creator string
	Legs    []FlightLeg
	Data    []SegmentData
	// Fragments holds every record of a type this package does not lay
	// out, verbatim, so a dialect gap is visible as data.
	Fragments []string
}

// FlightLeg is a type 3 record: one leg of a flight over a period.
type FlightLeg struct {
	Carrier string
	Number  string // as written, up to four characters
	Suffix  string
	// Variation and Sequence are the itinerary variation identifier and
	// the leg's position in the flight.
	Variation int
	Sequence  int
	// ServiceType is the kind of flight: J scheduled passenger, C charter,
	// F cargo, and so on.
	ServiceType string
	From, To    time.Time
	// Days is the frequency pattern, digits 1 (Monday) to 7 (Sunday) that
	// operate; a blank position for a day that does not.
	Days string
	// FrequencyRate is blank for weekly, 2 for fortnightly.
	FrequencyRate string

	Board string
	// STD and STA are the aircraft's scheduled times, HHMM in the file's
	// time mode; PaxSTD and PaxSTA the passenger times when they differ
	// (a remote stand, a long taxi).
	STD, PaxSTD string
	// DepVariation and ArrVariation are the stations' offsets from UTC,
	// +HHMM, so a local-time file can be placed on the clock.
	DepVariation string
	DepTerminal  string
	Off          string
	STA, PaxSTA  string
	ArrVariation string
	ArrTerminal  string
	Equipment    string
	// Classes is the booking designators offered (PRBD); Modifier the
	// booking modifier (PRBM).
	Classes  string
	Modifier string
	Meal     string
	Joint    string
	// MCTStatus is the two minimum-connecting-time status characters.
	MCTStatus string
	// SecureFlight is the Secure Flight indicator.
	SecureFlight string
	// Disclosure is the operating airline disclosure: L for a leg flown
	// by another carrier under this designator (a codeshare marketing
	// leg), whose operator a DEI 010 segment record names.
	Disclosure string
	// Onward is the next flight the aircraft operates, when the record
	// says.
	OnwardCarrier, OnwardNumber, OnwardSuffix string
	Owner, CockpitCrew, CabinCrew             string
	TrafficRestriction                        string
	// Configuration is the aircraft configuration or version.
	Configuration string
	// DepDateVariation and ArrDateVariation are the day offsets of the
	// leg's departure and arrival from the flight's date.
	DepDateVariation, ArrDateVariation int
	Serial                             int
}

// Flight returns the leg's flight designator.
func (l FlightLeg) Flight() Flight {
	return Flight{Carrier: l.Carrier, Number: l.Number, Suffix: l.Suffix}
}

// SegmentData is a type 4 record: one data element for one leg or
// segment, identified by its DEI.
type SegmentData struct {
	Carrier, Number, Suffix string
	Variation, Sequence     int
	ServiceType             string
	// BoardIndicator and OffIndicator are X when the point named is the
	// leg's own board or off point.
	BoardIndicator, OffIndicator string
	// DEI is the data element identifier, e.g. 010 the operating carrier's
	// flight (on a codeshare marketing leg), 050 the marketing carriers'
	// flights (on the operated leg), 127 the on-time performance code.
	DEI        string
	Board, Off string
	Data       string
	Serial     int
}

// Data element identifiers this package uses itself.
const (
	DEIOperatingFlight  = "010" // the flight that actually operates a marketing leg
	DEIMarketingFlights = "050" // the marketing flights sold on an operated leg
)

const recordLen = 200

// Write renders the file: header, carrier record, legs, segment data,
// trailer, each padded to 200 characters and newline-terminated, with
// serial numbers assigned in order.
func (f *File) Write(w io.Writer) error {
	bw := bufio.NewWriter(w)
	serial := 0
	emit := func(rec []byte) {
		serial++
		put(rec, 195, 200, fmt.Sprintf("%06d", serial), false)
		bw.Write(rec)
		bw.WriteByte('\n')
	}
	rec := blank()
	put(rec, 1, 35, "1AIRLINE STANDARD SCHEDULE DATA SET", false)
	emit(rec)

	rec = blank()
	mode := "U"
	if f.TimeMode == LocalTime {
		mode = "L"
	}
	put(rec, 1, 1, "2", false)
	put(rec, 2, 2, mode, false)
	put(rec, 3, 5, f.Carrier, false)
	put(rec, 11, 13, f.Season, false)
	put(rec, 15, 21, ssimDate(f.From), false)
	put(rec, 22, 28, ssimDate(f.To), false)
	put(rec, 29, 35, ssimDate(f.Created), false)
	put(rec, 36, 64, f.Title, false)
	put(rec, 65, 71, ssimDate(f.Released), false)
	put(rec, 72, 72, f.Status, false)
	put(rec, 73, 107, f.Creator, false)
	if !f.Created.IsZero() {
		put(rec, 191, 194, f.Created.Format("1504"), false)
	}
	emit(rec)

	for _, l := range f.Legs {
		rec = blank()
		put(rec, 1, 1, "3", false)
		put(rec, 2, 2, l.Suffix, false)
		put(rec, 3, 5, l.Carrier, false)
		put(rec, 6, 9, l.Number, true)
		put(rec, 10, 11, fmt.Sprintf("%02d", l.Variation), false)
		put(rec, 12, 13, fmt.Sprintf("%02d", l.Sequence), false)
		put(rec, 14, 14, l.ServiceType, false)
		put(rec, 15, 21, ssimDate(l.From), false)
		put(rec, 22, 28, ssimDate(l.To), false)
		put(rec, 29, 35, l.Days, false)
		put(rec, 36, 36, l.FrequencyRate, false)
		put(rec, 37, 39, l.Board, false)
		put(rec, 40, 43, orElse(l.PaxSTD, l.STD), false)
		put(rec, 44, 47, l.STD, false)
		put(rec, 48, 52, l.DepVariation, false)
		put(rec, 53, 54, l.DepTerminal, false)
		put(rec, 55, 57, l.Off, false)
		put(rec, 58, 61, l.STA, false)
		put(rec, 62, 65, orElse(l.PaxSTA, l.STA), false)
		put(rec, 66, 70, l.ArrVariation, false)
		put(rec, 71, 72, l.ArrTerminal, false)
		put(rec, 73, 75, l.Equipment, false)
		put(rec, 76, 95, l.Classes, false)
		put(rec, 96, 100, l.Modifier, false)
		put(rec, 101, 110, l.Meal, false)
		put(rec, 111, 119, l.Joint, false)
		put(rec, 120, 121, l.MCTStatus, false)
		put(rec, 122, 122, l.SecureFlight, false)
		put(rec, 129, 131, l.Owner, false)
		put(rec, 132, 134, l.CockpitCrew, false)
		put(rec, 135, 137, l.CabinCrew, false)
		put(rec, 138, 140, l.OnwardCarrier, false)
		put(rec, 141, 144, l.OnwardNumber, true)
		put(rec, 146, 146, l.OnwardSuffix, false)
		put(rec, 149, 149, l.Disclosure, false)
		put(rec, 150, 160, l.TrafficRestriction, false)
		put(rec, 173, 192, l.Configuration, false)
		if l.DepDateVariation != 0 {
			put(rec, 193, 193, strconv.Itoa(l.DepDateVariation), false)
		}
		if l.ArrDateVariation != 0 {
			put(rec, 194, 194, strconv.Itoa(l.ArrDateVariation), false)
		}
		emit(rec)
	}
	for _, d := range f.Data {
		rec = blank()
		put(rec, 1, 1, "4", false)
		put(rec, 2, 2, d.Suffix, false)
		put(rec, 3, 5, d.Carrier, false)
		put(rec, 6, 9, d.Number, true)
		put(rec, 10, 11, fmt.Sprintf("%02d", d.Variation), false)
		put(rec, 12, 13, fmt.Sprintf("%02d", d.Sequence), false)
		put(rec, 14, 14, d.ServiceType, false)
		put(rec, 29, 29, orElse(d.BoardIndicator, "X"), false)
		put(rec, 30, 30, orElse(d.OffIndicator, "X"), false)
		put(rec, 31, 33, d.DEI, false)
		put(rec, 34, 36, d.Board, false)
		put(rec, 37, 39, d.Off, false)
		put(rec, 40, 194, d.Data, false)
		emit(rec)
	}
	rec = blank()
	put(rec, 1, 1, "5", false)
	put(rec, 3, 5, f.Carrier, false)
	put(rec, 6, 12, ssimDate(orTime(f.Released, f.Created)), false)
	// The serial number check reference is the trailer's own serial, which
	// is also the record count.
	put(rec, 189, 194, fmt.Sprintf("%06d", serial+1), false)
	emit(rec)
	return bw.Flush()
}

// ParseFile reads a chapter 7 file. Padding records (a line of zeros,
// which files carry to fill their blocks) are skipped; records of other
// types are kept verbatim as fragments. Short lines are accepted and read
// as if space-padded, because files arrive with their trailing blanks
// trimmed more often than not.
func ParseFile(r io.Reader) (*File, error) {
	f := &File{TimeMode: UTC}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), 1<<20)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" {
			continue
		}
		if len(line) < recordLen {
			line += strings.Repeat(" ", recordLen-len(line))
		}
		switch line[0] {
		case '0':
			continue
		case '1':
			// The header says only what the file is.
		case '2':
			if get(line, 2, 2) == "L" {
				f.TimeMode = LocalTime
			}
			f.Carrier = get(line, 3, 5)
			f.Season = get(line, 11, 13)
			f.From = parseSSIMDate(get(line, 15, 21))
			f.To = parseSSIMDate(get(line, 22, 28))
			f.Created = parseSSIMDate(get(line, 29, 35))
			if hm := get(line, 191, 194); len(hm) == 4 && !f.Created.IsZero() {
				if t, err := time.Parse("1504", hm); err == nil {
					f.Created = f.Created.Add(time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute)
				}
			}
			f.Title = get(line, 36, 64)
			f.Released = parseSSIMDate(get(line, 65, 71))
			f.Status = get(line, 72, 72)
			f.Creator = get(line, 73, 107)
		case '3':
			l := FlightLeg{
				Suffix: get(line, 2, 2), Carrier: get(line, 3, 5), Number: strings.TrimLeft(get(line, 6, 9), "0"),
				Variation: atoi(get(line, 10, 11)), Sequence: atoi(get(line, 12, 13)), ServiceType: get(line, 14, 14),
				From: parseSSIMDate(get(line, 15, 21)), To: parseSSIMDate(get(line, 22, 28)),
				Days: line[28:35], FrequencyRate: get(line, 36, 36),
				Board: get(line, 37, 39), PaxSTD: get(line, 40, 43), STD: get(line, 44, 47), DepVariation: get(line, 48, 52), DepTerminal: get(line, 53, 54),
				Off: get(line, 55, 57), STA: get(line, 58, 61), PaxSTA: get(line, 62, 65), ArrVariation: get(line, 66, 70), ArrTerminal: get(line, 71, 72),
				Equipment: get(line, 73, 75), Classes: get(line, 76, 95), Modifier: get(line, 96, 100), Meal: get(line, 101, 110), Joint: get(line, 111, 119),
				MCTStatus: get(line, 120, 121), SecureFlight: get(line, 122, 122),
				Owner: get(line, 129, 131), CockpitCrew: get(line, 132, 134), CabinCrew: get(line, 135, 137),
				OnwardCarrier: get(line, 138, 140), OnwardNumber: strings.TrimLeft(get(line, 141, 144), "0"), OnwardSuffix: get(line, 146, 146),
				Disclosure: get(line, 149, 149), TrafficRestriction: get(line, 150, 160), Configuration: get(line, 173, 192),
				DepDateVariation: atoi(get(line, 193, 193)), ArrDateVariation: atoi(get(line, 194, 194)), Serial: atoi(get(line, 195, 200)),
			}
			if l.Number == "" && get(line, 6, 9) != "" {
				l.Number = "0"
			}
			if l.PaxSTD == l.STD {
				l.PaxSTD = ""
			}
			if l.PaxSTA == l.STA {
				l.PaxSTA = ""
			}
			f.Legs = append(f.Legs, l)
		case '4':
			f.Data = append(f.Data, SegmentData{
				Suffix: get(line, 2, 2), Carrier: get(line, 3, 5), Number: strings.TrimLeft(get(line, 6, 9), "0"),
				Variation: atoi(get(line, 10, 11)), Sequence: atoi(get(line, 12, 13)), ServiceType: get(line, 14, 14),
				BoardIndicator: get(line, 29, 29), OffIndicator: get(line, 30, 30), DEI: get(line, 31, 33),
				Board: get(line, 34, 36), Off: get(line, 37, 39), Data: get(line, 40, 194), Serial: atoi(get(line, 195, 200)),
			})
		case '5':
			// The trailer's check reference is advisory; a file cut short is
			// visible as a missing trailer, which callers may test for.
		default:
			f.Fragments = append(f.Fragments, strings.TrimRight(line, " "))
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("ssim: read file: %w", err)
	}
	if n == 0 {
		return nil, fmt.Errorf("ssim: empty file")
	}
	return f, nil
}

// OperatingFlight is the flight that operates a marketing leg, from its
// DEI 010 segment data, or the zero Flight when the leg carries none.
func (f *File) OperatingFlight(l FlightLeg) Flight {
	for _, d := range f.Data {
		if d.DEI != DEIOperatingFlight || d.Carrier != l.Carrier || d.Number != l.Number || d.Suffix != l.Suffix ||
			d.Variation != l.Variation || d.Sequence != l.Sequence {
			continue
		}
		return parseDesignator(d.Data)
	}
	return Flight{}
}

// parseDesignator reads "OO 3000" or "OO3000" or "OO3000A".
func parseDesignator(s string) Flight {
	s = strings.TrimSpace(s)
	if len(s) < 3 {
		return Flight{}
	}
	carrier := strings.TrimSpace(s[:2])
	rest := strings.TrimSpace(s[2:])
	if len(s) >= 3 && s[2] != ' ' && (s[2] < '0' || s[2] > '9') {
		carrier, rest = s[:3], strings.TrimSpace(s[3:])
	}
	num := strings.TrimRightFunc(rest, func(r rune) bool { return r < '0' || r > '9' })
	suffix := strings.TrimPrefix(rest, num)
	return Flight{Carrier: carrier, Number: strings.TrimLeft(num, "0"), Suffix: suffix}
}

func blank() []byte { return []byte(strings.Repeat(" ", recordLen)) }

// put writes s into the 1-based inclusive columns start..end, left-
// justified (right when right is set), truncating what does not fit. A
// fixed-width record cannot carry a control character -- a newline in a
// field would end the record early and shift every column after it, which
// the fuzzer found -- so anything outside printable ASCII becomes a space.
func put(rec []byte, start, end int, s string, right bool) {
	w := end - start + 1
	if len(s) > w {
		s = s[:w]
	}
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e {
			return ' '
		}
		return r
	}, s)
	if right {
		s = strings.Repeat(" ", w-len(s)) + s
	}
	copy(rec[start-1:], s)
}

func get(line string, start, end int) string {
	if end > len(line) {
		end = len(line)
	}
	if start > end {
		return ""
	}
	return strings.TrimSpace(line[start-1 : end])
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func orElse(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func orTime(a, b time.Time) time.Time {
	if !a.IsZero() {
		return a
	}
	return b
}

// ssimDate is DDMMMYY, upper case; the zero time is blank.
func ssimDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return strings.ToUpper(t.Format("02Jan06"))
}

func parseSSIMDate(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if len(s) != 7 {
		return time.Time{}
	}
	t, err := time.Parse("02Jan06", s[:2]+strings.ToUpper(s[2:3])+strings.ToLower(s[3:5])+s[5:])
	if err != nil {
		return time.Time{}
	}
	return t
}
