// Package ats implements the ICAO air traffic services messages that follow
// a flight through the AFTN: the filed flight plan and the movement messages
// about it.
//
// The forms are ICAO Doc 4444 (PANS-ATM) Appendix 3. The document itself is
// sold; the message forms are reproduced in full by the FAA's ICAO flight
// planning guidance and by Eurocontrol's IFPS manual, both free, with
// verbatim examples this package is tested against. A message is a
// parenthesised list of numbered fields, each introduced by a hyphen:
//
//	(FPL-UAL1447-IS
//	-A320/M-SDGIRWZ/S
//	-KIAD2130
//	-N0440F360 DCT DAILY J61 HUBBS DCT KEMPR DCT ILM AR21 CRANS FISEL2
//	-KFLL0206
//	-PBN/D1S1 NAV/RNVD1E2A1)
//
//	(DEP-ABC123/A0254-NZAA2347-VTBS-DOF/091120)
//	(ARR-CSA406-LHBP-LKPR0913)
//	(DLA-ABC123-NZAA2345-VTBS-DOF/091120)
//	(CNL-ABC123-NZAA2300-VTBS-DOF/091120)
//
// Field 3 is the message type, 7 the aircraft identification with an
// optional SSR code, 8 the flight rules and type, 9 the aircraft type and
// wake category, 10 the equipment, 13 the departure aerodrome and estimated
// off-block time, 15 the speed, level and route, 16 the destination with
// total estimated elapsed time and alternates, 17 the arrival aerodrome and
// time, 18 other information as keyword/value pairs or a bare 0.
package ats

import (
	"fmt"
	"regexp"
	"strings"
)

// Type is the message type in field 3.
type Type string

const (
	TypeFPL Type = "FPL" // filed flight plan
	TypeDEP Type = "DEP" // departure
	TypeARR Type = "ARR" // arrival
	TypeDLA Type = "DLA" // delay
	TypeCNL Type = "CNL" // cancellation
	TypeCHG Type = "CHG" // modification
)

// Message is one ATS message. Fields the type does not use are empty.
type Message struct {
	Type Type
	// AircraftID is field 7a: the callsign, e.g. BAW117 or N12345. SSR is
	// the optional mode and code, e.g. A0254.
	AircraftID string
	SSR        string
	// Rules and FlightType are field 8: I/V/Y/Z and S/N/G/M/X.
	Rules      string
	FlightType string
	// AircraftType and Wake are field 9: B772 and H.
	AircraftType string
	Wake         string
	// Equipment is field 10 as written, e.g. SDE3FGHIRWXY/LB1.
	Equipment string
	// Departure is field 13a, the ICAO location indicator; EOBT is 13b,
	// HHMM. On an ARR the time is empty.
	Departure string
	EOBT      string
	// Route is field 15 as written, cruising speed and level first.
	Route string
	// Destination is field 16a, EET 16b (HHMM total estimated elapsed time),
	// Alternates 16c.
	Destination string
	EET         string
	Alternates  []string
	// Arrival and ArrivalTime are field 17 on an ARR.
	Arrival     string
	ArrivalTime string
	// Other is field 18: keyword/value pairs in order. Nil renders as 0.
	Other []Item
	// Amendments is field 22 on a CHG: field number and new content.
	Amendments []Item
	// Unparsed keeps fields this package could not place, verbatim.
	Unparsed []string
}

// Item is one keyword/value pair of field 18 or one amendment of field 22.
type Item struct {
	Key   string
	Value string
}

// OtherValue returns the value of a field 18 keyword, or "".
func (m *Message) OtherValue(key string) string {
	for _, it := range m.Other {
		if it.Key == key {
			return it.Value
		}
	}
	return ""
}

// Looks reports whether text is an ATS message: an open parenthesis and a
// known type.
func Looks(text string) bool {
	t := strings.TrimSpace(text)
	if !strings.HasPrefix(t, "(") || len(t) < 5 {
		return false
	}
	switch Type(t[1:4]) {
	case TypeFPL, TypeDEP, TypeARR, TypeDLA, TypeCNL, TypeCHG:
		return true
	}
	return false
}

var (
	aerodromeTimeRe = regexp.MustCompile(`^([A-Z]{4})(\d{4})?$`)
	id7Re           = regexp.MustCompile(`^([A-Z0-9]{2,7})(?:/([A-Z]\d{4}))?$`)
	field8Re        = regexp.MustCompile(`^([IVYZ])([SNGMX])$`)
	field9Re        = regexp.MustCompile(`^(?:(\d{1,2}))?([A-Z0-9]{2,4})/([LMHJ])$`)
	field18Re       = regexp.MustCompile(`\b([A-Z]{3,4})/`)
)

// Build renders the message. Field 15 may hold newlines; they are kept, as
// the FAA's examples wrap the route.
func Build(m *Message) (string, error) {
	if m.AircraftID == "" {
		return "", fmt.Errorf("ats: field 7 needs an aircraft identification")
	}
	var b strings.Builder
	b.WriteString("(" + string(m.Type) + "-" + m.AircraftID)
	if m.SSR != "" {
		b.WriteString("/" + m.SSR)
	}
	dep := m.Departure + m.EOBT
	other := "0"
	if len(m.Other) > 0 {
		var parts []string
		for _, it := range m.Other {
			parts = append(parts, it.Key+"/"+it.Value)
		}
		other = strings.Join(parts, " ")
	}
	switch m.Type {
	case TypeFPL:
		if m.Rules == "" || m.AircraftType == "" || m.Departure == "" || m.Route == "" || m.Destination == "" {
			return "", fmt.Errorf("ats: an FPL needs fields 8, 9, 13, 15 and 16")
		}
		fmt.Fprintf(&b, "-%s%s\n", m.Rules, m.FlightType)
		fmt.Fprintf(&b, "-%s/%s-%s\n", m.AircraftType, m.Wake, m.Equipment)
		fmt.Fprintf(&b, "-%s\n", dep)
		fmt.Fprintf(&b, "-%s\n", m.Route)
		dest := m.Destination + m.EET
		if len(m.Alternates) > 0 {
			dest += " " + strings.Join(m.Alternates, " ")
		}
		fmt.Fprintf(&b, "-%s\n", dest)
		fmt.Fprintf(&b, "-%s)", other)
	case TypeDEP, TypeDLA, TypeCNL:
		if m.Departure == "" || m.Destination == "" {
			return "", fmt.Errorf("ats: a %s needs fields 13 and 16", m.Type)
		}
		fmt.Fprintf(&b, "-%s-%s", dep, m.Destination)
		if len(m.Other) > 0 {
			fmt.Fprintf(&b, "-%s", other)
		}
		b.WriteString(")")
	case TypeARR:
		if m.Departure == "" || m.Arrival == "" {
			return "", fmt.Errorf("ats: an ARR needs fields 13 and 17")
		}
		fmt.Fprintf(&b, "-%s", dep)
		if m.Destination != "" && m.Destination != m.Arrival {
			// Field 16a is carried only when the flight landed elsewhere.
			fmt.Fprintf(&b, "-%s", m.Destination)
		}
		fmt.Fprintf(&b, "-%s%s", m.Arrival, m.ArrivalTime)
		if len(m.Other) > 0 {
			fmt.Fprintf(&b, "-%s", other)
		}
		b.WriteString(")")
	case TypeCHG:
		if m.Departure == "" || m.Destination == "" || len(m.Amendments) == 0 {
			return "", fmt.Errorf("ats: a CHG needs fields 13, 16 and 22")
		}
		fmt.Fprintf(&b, "-%s-%s", dep, m.Destination)
		if len(m.Other) > 0 {
			fmt.Fprintf(&b, "-%s", other)
		}
		for _, a := range m.Amendments {
			fmt.Fprintf(&b, "-%s/%s", a.Key, a.Value)
		}
		b.WriteString(")")
	default:
		return "", fmt.Errorf("ats: unknown message type %q", m.Type)
	}
	return b.String(), nil
}

// Parse reads a message.
func Parse(text string) (*Message, error) {
	t := strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if !strings.HasPrefix(t, "(") || !strings.HasSuffix(t, ")") {
		return nil, fmt.Errorf("ats: message is not parenthesised")
	}
	t = t[1 : len(t)-1]
	// Fields are introduced by a hyphen at the start of the message or after
	// a newline or another field; a hyphen inside field 15 or 18 text follows
	// something other than a field boundary. Splitting on "\n-" and "-" at
	// field starts handles the published forms.
	fields := splitFields(t)
	if len(fields) < 2 {
		return nil, fmt.Errorf("ats: message has no fields")
	}
	m := &Message{Type: Type(fields[0])}
	switch m.Type {
	case TypeFPL, TypeDEP, TypeARR, TypeDLA, TypeCNL, TypeCHG:
	default:
		return nil, fmt.Errorf("ats: unknown message type %q", fields[0])
	}
	id := id7Re.FindStringSubmatch(fields[1])
	if id == nil {
		return nil, fmt.Errorf("ats: field 7 %q is not an aircraft identification", fields[1])
	}
	m.AircraftID, m.SSR = id[1], id[2]
	rest := fields[2:]
	switch m.Type {
	case TypeFPL:
		// Fields 8, 9, 10, 13, 15, 16 and 18, in that order. Fields 9 and 10
		// share a line joined by a hyphen -- B763/H-SXWJ5E3GDHIRYZ/SB2D1 --
		// which the splitter already treats as two fields.
		if len(rest) < 6 {
			return nil, fmt.Errorf("ats: an FPL needs fields 8, 9, 10, 13, 15 and 16; got %d fields", len(rest))
		}
		if f8 := field8Re.FindStringSubmatch(rest[0]); f8 != nil {
			m.Rules, m.FlightType = f8[1], f8[2]
		} else {
			m.Unparsed = append(m.Unparsed, "8:"+rest[0])
		}
		if mm := field9Re.FindStringSubmatch(rest[1]); mm != nil {
			m.AircraftType, m.Wake = mm[2], mm[3]
		} else {
			m.Unparsed = append(m.Unparsed, "9:"+rest[1])
		}
		m.Equipment = rest[2]
		m.Departure, m.EOBT = splitAerodrome(rest[3])
		m.Route = strings.TrimSpace(rest[4])
		destParts := strings.Fields(rest[5])
		if len(destParts) > 0 {
			m.Destination, m.EET = splitAerodrome(destParts[0])
			m.Alternates = destParts[1:]
		}
		if len(rest) > 6 {
			m.Other = parseOther(strings.Join(rest[6:], " "))
		}
	case TypeDEP, TypeDLA, TypeCNL:
		if len(rest) < 2 {
			return nil, fmt.Errorf("ats: a %s needs fields 13 and 16", m.Type)
		}
		m.Departure, m.EOBT = splitAerodrome(rest[0])
		m.Destination, m.EET = splitAerodrome(rest[1])
		if len(rest) > 2 {
			m.Other = parseOther(strings.Join(rest[2:], " "))
		}
	case TypeARR:
		if len(rest) < 2 {
			return nil, fmt.Errorf("ats: an ARR needs fields 13 and 17")
		}
		m.Departure, m.EOBT = splitAerodrome(rest[0])
		last := 1
		if len(rest) >= 3 && aerodromeTimeRe.MatchString(rest[1]) && aerodromeTimeRe.MatchString(rest[2]) {
			// Diverted: field 16a precedes field 17.
			m.Destination, _ = splitAerodrome(rest[1])
			last = 2
		}
		m.Arrival, m.ArrivalTime = splitAerodrome(rest[last])
		if m.Destination == "" {
			m.Destination = m.Arrival
		}
		if len(rest) > last+1 {
			m.Other = parseOther(strings.Join(rest[last+1:], " "))
		}
	case TypeCHG:
		if len(rest) < 3 {
			return nil, fmt.Errorf("ats: a CHG needs fields 13, 16 and 22")
		}
		m.Departure, m.EOBT = splitAerodrome(rest[0])
		m.Destination, m.EET = splitAerodrome(rest[1])
		for _, f := range rest[2:] {
			k, v, ok := strings.Cut(f, "/")
			if ok && isDigits(k) {
				m.Amendments = append(m.Amendments, Item{Key: k, Value: v})
			} else {
				m.Other = append(m.Other, parseOther(f)...)
			}
		}
	}
	return m, nil
}

// splitFields separates the hyphen-introduced fields of a message body.
// A hyphen starts a field when it begins the body, follows a newline, or
// follows the end of a field that itself ended a line; hyphens inside a
// field (a route, a remark) are kept.
func splitFields(body string) []string {
	var fields []string
	var cur strings.Builder
	atStart := true
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch {
		case c == '-' && (atStart || i == 0 || body[i-1] == '\n' || fieldBoundary(body, i)):
			if cur.Len() > 0 || len(fields) == 0 {
				fields = append(fields, strings.TrimSpace(cur.String()))
			}
			cur.Reset()
			atStart = false
		case c == '\n':
			// Continuation of a wrapped field (route) unless the next
			// character opens a new one.
			if i+1 < len(body) && body[i+1] == '-' {
				continue
			}
			cur.WriteByte(' ')
		default:
			cur.WriteByte(c)
			atStart = false
		}
	}
	fields = append(fields, strings.TrimSpace(cur.String()))
	return fields
}

// fieldBoundary reports whether the hyphen at i separates two fields on one
// line, as in DEP-ABC123/A0254-NZAA2347-VTBS: the fields of a movement
// message are short tokens without internal hyphens, so a hyphen after a
// token character is a boundary; inside a route or a remark hyphens are rare
// and, in the published forms, follow a space or a digit within a token like
// N0475F340 (which has none) -- the practical rule is that field 15 and 18
// text never begins with a hyphen and every real field does.
func fieldBoundary(body string, i int) bool {
	if i == 0 {
		return true
	}
	prev := body[i-1]
	return prev != ' ' && prev != '/' && prev != '-'
}

func splitAerodrome(s string) (aerodrome, hhmm string) {
	s = strings.TrimSpace(s)
	if m := aerodromeTimeRe.FindStringSubmatch(s); m != nil {
		return m[1], m[2]
	}
	return s, ""
}

// parseOther reads field 18: KEY/value pairs, values running to the next
// keyword. A bare 0 is nothing.
func parseOther(s string) []Item {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" {
		return nil
	}
	locs := field18Re.FindAllStringIndex(s, -1)
	var out []Item
	for i, loc := range locs {
		key := s[loc[0] : loc[1]-1]
		end := len(s)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		out = append(out, Item{Key: key, Value: strings.TrimSpace(s[loc[1]:end])})
	}
	if len(out) == 0 {
		out = append(out, Item{Key: "RMK", Value: s})
	}
	return out
}

func isDigits(s string) bool {
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
