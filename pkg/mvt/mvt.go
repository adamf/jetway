// Package mvt reads and writes IATA aircraft movement messages: MVT, its MVA
// variant, and DIV diversions.
//
// A movement message is what makes a flight's day observable. AD says the
// aircraft left the gate and the ground; AA says it touched down and parked;
// ED, EO and EA revise the estimates in between; DL says why the plan slipped,
// in coded form. These are the messages an operations centre lives on, and in
// the simulator they are what animates an aircraft between airports.
//
// The governing document is IATA AHM 780/781, which is a paid publication this
// project has not bought. The grammar here is built from OAG's freely published
// "MVT / MVA / DIV Aircraft Movement Messages: Data Elements and Message
// Examples", whose element tables and verbatim examples reproduce the AHM
// forms. Per this repository's rule, that makes the package inferred rather
// than conformant: the shapes below match every published example, lines this
// parser does not recognise are kept verbatim, and nothing is discarded.
package mvt

import (
	"fmt"
	"regexp"
	"strings"
)

// Kind is the standard message identifier on the first line.
type Kind string

const (
	KindMVT Kind = "MVT" // aircraft movement
	KindMVA Kind = "MVA" // movement advice, same body as MVT
	KindDIV Kind = "DIV" // in-flight diversion
)

// TimePair is a two-part time group such as off-blocks/airborne or
// touchdown/on-blocks.
//
// Times are HHMM in UTC -- every time on a movement message is UTC. DayA and
// DayB carry the optional two-digit day of month for the six-digit form
// ("AD211100/211115"); they are empty in the plain four-digit form. Second may
// be empty, and in the slash-only form ("AD/1200") First is empty instead:
// the sender knew the airborne time but not off-blocks.
type TimePair struct {
	DayA, First  string
	DayB, Second string
}

// Delay is one coded reason with its duration.
//
// Codes are the two-digit numeric AHM reason codes ("72"); Duration is HHMM.
// The wire form packs two delays as DL64/72/0015/0020 -- codes first, then
// durations, positionally matched.
type Delay struct {
	Code     string
	Duration string
}

// ETA is an estimated arrival: a time and, on departure-side messages and
// diversions, the airport it applies to.
type ETA struct {
	Day     string // optional two-digit day
	Time    string // HHMM
	Airport string // empty on a revised-ETA message sent at the arrival station
}

// Message is one movement message.
//
// Every field is optional except the identification: which kind, which flight,
// which day, which tail, at which station. That mirrors the format -- an MVT
// is a bag of elements about one flight leg, and which elements appear depends
// on what kind of day the aircraft is having.
type Message struct {
	Kind       Kind
	Correction bool // a leading COR: this message replaces an earlier one

	// Flight is the designator and number as written, e.g. "SD200".
	Flight string
	// Day is the scheduled UTC day of month of departure, e.g. "21".
	Day string
	// Registration is the airframe, e.g. "PMDFG".
	Registration string
	// Station is the airport of movement: the departure airport on
	// departure-side messages, the arrival airport on arrival-side ones, and
	// the originally intended arrival airport on a DIV.
	Station string

	AD *TimePair // actual departure: off-blocks / airborne
	AA *TimePair // actual arrival: touchdown / on-blocks
	FR *TimePair // return from airborne: touchdown / on-blocks
	RR string    // return to ramp time, HHMM

	EA *ETA   // estimated arrival (touchdown)
	ED string // estimated departure, DDHHMM
	EO string // estimated take-off, HHMM
	EB string // estimated on-block, HHMM
	NI string // next information, DDHHMM

	Delays      []Delay // DL
	ExtraDelays []Delay // EDL
	SubCodes    []string
	// DR is the diversion reason code on a DIV.
	DR string

	// Pax is the seats occupied, possibly split per destination as sent
	// (PX12/134/10 stays three values).
	Pax []int
	// FLD repeats the scheduled UTC day of departure of the leg.
	FLD string
	// SI is free-text supplementary information, always last.
	SI string

	// Unrecognised keeps any line this package could not place, verbatim.
	// The format is inferred (see the package comment); throwing away what a
	// newer or stranger sender wrote would hide exactly the evidence needed
	// to extend the grammar.
	Unrecognised []string
}

// idRe matches the flight identification line: SD200/21.PMDFG.CDG
var idRe = regexp.MustCompile(`^([A-Z0-9]{2}[A-Z]?\d{1,4}[A-Z]?)/(\d{1,2})\.([A-Z0-9-]{2,10})\.([A-Z]{3})$`)

// timePairRe matches AD/AA/FR bodies: 1100/1115, 211100/211115, 1200, /1200.
var timePairRe = regexp.MustCompile(`^(?:(\d{2})?(\d{4}))?(?:/(?:(\d{2})?(\d{4})))?$`)

// IsMovement reports whether a Type B text body is a movement message.
//
// The gateway uses this to branch before the reservation grammar, the same way
// availability and schedule messages branch: an MVT says nothing about any
// booking, and feeding it to the AIRIMP parser would produce a message full of
// unrecognised elements.
func IsMovement(text string) bool {
	first := firstLine(text)
	return first == "MVT" || first == "MVA" || first == "DIV" ||
		strings.HasPrefix(first, "COR MVT") || strings.HasPrefix(first, "COR MVA") ||
		strings.HasPrefix(first, "COR DIV")
}

func firstLine(text string) string {
	for _, ln := range strings.Split(text, "\n") {
		ln = strings.TrimSpace(ln)
		if ln != "" {
			return ln
		}
	}
	return ""
}

// Parse reads a movement message from Type B text.
//
// It is deliberately tolerant: a malformed element becomes an Unrecognised
// line rather than an error, because on a live network the message that does
// not quite match the grammar is the interesting one. The only hard failures
// are a missing identifier and a missing or unreadable flight line.
func Parse(text string) (*Message, error) {
	lines := []string{}
	for _, ln := range strings.Split(text, "\n") {
		if t := strings.TrimRight(ln, " \r"); strings.TrimSpace(t) != "" {
			lines = append(lines, strings.TrimSpace(t))
		}
	}
	if len(lines) < 2 {
		return nil, fmt.Errorf("mvt: a movement message needs an identifier and a flight line")
	}

	m := &Message{}
	head := lines[0]
	if strings.HasPrefix(head, "COR ") {
		m.Correction = true
		head = strings.TrimSpace(strings.TrimPrefix(head, "COR "))
	}
	switch head {
	case "MVT", "MVA", "DIV":
		m.Kind = Kind(head)
	default:
		return nil, fmt.Errorf("mvt: %q is not a movement message identifier", lines[0])
	}

	id := idRe.FindStringSubmatch(lines[1])
	if id == nil {
		return nil, fmt.Errorf("mvt: unreadable flight identification line %q", lines[1])
	}
	m.Flight, m.Day, m.Registration, m.Station = id[1], id[2], id[3], id[4]

	for _, ln := range lines[2:] {
		if !m.applyLine(ln) {
			m.Unrecognised = append(m.Unrecognised, ln)
		}
	}
	return m, nil
}

// applyLine folds one element line into the message, reporting whether it was
// recognised. A line may carry several elements separated by spaces
// ("AD1115 EO1135 EA1530 FRA", "DR71 PX112"), so tokens are consumed in order
// and one unplaceable token fails the whole line into Unrecognised -- keeping
// half a line would silently drop the other half.
func (m *Message) applyLine(line string) bool {
	// SI swallows the rest of the message as free text and always appears last.
	if strings.HasPrefix(line, "SI ") || line == "SI" {
		text := strings.TrimSpace(strings.TrimPrefix(line, "SI"))
		if m.SI == "" {
			m.SI = text
		} else {
			m.SI += "\n" + text
		}
		return true
	}

	tokens := strings.Fields(line)
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		switch {
		case strings.HasPrefix(tok, "AD"):
			p, ok := parsePair(tok[2:])
			if !ok {
				return false
			}
			m.AD = p
		case strings.HasPrefix(tok, "AA"):
			p, ok := parsePair(tok[2:])
			if !ok {
				return false
			}
			m.AA = p
		case strings.HasPrefix(tok, "FR"):
			p, ok := parsePair(tok[2:])
			if !ok {
				return false
			}
			m.FR = p
		case strings.HasPrefix(tok, "RR") && allDigits(tok[2:]) && len(tok) == 6:
			m.RR = tok[2:]
		case strings.HasPrefix(tok, "EA"):
			e, ok := parseETA(tok[2:])
			if !ok {
				return false
			}
			// The airport, when present, is the next bare token: "EA1500 FRA".
			if i+1 < len(tokens) && len(tokens[i+1]) == 3 && allUpper(tokens[i+1]) {
				e.Airport = tokens[i+1]
				i++
			}
			m.EA = e
		case strings.HasPrefix(tok, "EDL"):
			d, ok := parseDelays(tok[3:])
			if !ok {
				return false
			}
			m.ExtraDelays = append(m.ExtraDelays, d...)
		case strings.HasPrefix(tok, "EO") && allDigits(tok[2:]) && len(tok) == 6:
			m.EO = tok[2:]
		case strings.HasPrefix(tok, "EB") && allDigits(tok[2:]) && len(tok) == 6:
			m.EB = tok[2:]
		case strings.HasPrefix(tok, "ED") && allDigits(tok[2:]) && len(tok) == 8:
			m.ED = tok[2:]
		case strings.HasPrefix(tok, "NI") && allDigits(tok[2:]) && len(tok) == 8:
			m.NI = tok[2:]
		case strings.HasPrefix(tok, "DLA"):
			codes := strings.Split(tok[3:], "/")
			for _, c := range codes {
				if len(c) < 2 || len(c) > 3 {
					return false
				}
			}
			m.SubCodes = append(m.SubCodes, codes...)
		case strings.HasPrefix(tok, "DL"):
			d, ok := parseDelays(tok[2:])
			if !ok {
				return false
			}
			m.Delays = append(m.Delays, d...)
		case strings.HasPrefix(tok, "DR") && allDigits(tok[2:]) && len(tok) >= 4:
			m.DR = tok[2:]
		case strings.HasPrefix(tok, "PX"):
			px, ok := parsePax(tok[2:])
			if !ok {
				return false
			}
			m.Pax = px
		case strings.HasPrefix(tok, "FLD") && allDigits(tok[3:]) && len(tok) == 5:
			m.FLD = tok[3:]
		default:
			return false
		}
	}
	return true
}

func parsePair(s string) (*TimePair, bool) {
	g := timePairRe.FindStringSubmatch(s)
	if g == nil || (g[2] == "" && g[4] == "") {
		return nil, false
	}
	return &TimePair{DayA: g[1], First: g[2], DayB: g[3], Second: g[4]}, true
}

func parseETA(s string) (*ETA, bool) {
	switch len(s) {
	case 4:
		if !allDigits(s) {
			return nil, false
		}
		return &ETA{Time: s}, true
	case 6:
		if !allDigits(s) {
			return nil, false
		}
		return &ETA{Day: s[:2], Time: s[2:]}, true
	}
	return nil, false
}

// parseDelays reads the packed delay form: codes first, then durations.
// DL72/0015 is one delay; DL64/72/0015/0020 is two.
func parseDelays(s string) ([]Delay, bool) {
	parts := strings.Split(s, "/")
	if len(parts)%2 != 0 {
		return nil, false
	}
	n := len(parts) / 2
	out := make([]Delay, 0, n)
	for i := 0; i < n; i++ {
		code, dur := parts[i], parts[n+i]
		if len(code) != 2 || !allDigits(code) || len(dur) != 4 || !allDigits(dur) {
			return nil, false
		}
		out = append(out, Delay{Code: code, Duration: dur})
	}
	return out, true
}

func parsePax(s string) ([]int, bool) {
	if s == "" {
		return nil, false
	}
	var out []int
	for _, p := range strings.Split(s, "/") {
		if p == "" || !allDigits(p) {
			return nil, false
		}
		n := 0
		for _, r := range p {
			n = n*10 + int(r-'0')
		}
		out = append(out, n)
	}
	return out, true
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

func allUpper(s string) bool {
	for _, r := range s {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return s != ""
}
