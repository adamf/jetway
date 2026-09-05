// Package atfm implements the air traffic flow management slot messages the
// Network Manager exchanges with aircraft operators and ATC: the slot
// allocation (SAM) that gives a flight its calculated take-off time, the
// revision (SRM), the cancellation of a slot requirement (SLC), the
// suspension (FLS) and de-suspension (DES) of a flight, and the operator's
// answers (SMM, REA, FCM, SPA, SRJ, RFI, SWM).
//
// The messages are in ADEXP, keyword fields each introduced by a hyphen,
// and their forms and field vocabulary are those of EUROCONTROL's ATFCM
// Users Manual, which is public; this package is tested against the
// manual's own examples verbatim. The regulation cause (REGCAUSE) is the
// manual's cause letter, the phase of flight it applies to and the IATA
// delay code, with the correlation of its Annex D.
//
//	-TITLE SAM
//	-ARCID AMC101
//	-IFPLID AA12345678
//	-ADEP EGLL
//	-ADES LMML
//	-EOBD 160224
//	-EOBT 0950
//	-CTOT 1030
//	-REGUL RMZ24M
//	-TAXITIME 0020
//	-REGCAUSE CE 81
package atfm

import (
	"fmt"
	"regexp"
	"strings"
)

// Title is the message name.
type Title string

// The Network Manager's messages and the operator's replies.
const (
	TitleSAM Title = "SAM" // slot allocation
	TitleSRM Title = "SRM" // slot revision
	TitleSLC Title = "SLC" // slot requirement cancellation
	TitleFLS Title = "FLS" // flight suspension
	TitleDES Title = "DES" // de-suspension
	TitleSIP Title = "SIP" // slot improvement proposal
	TitleRRP Title = "RRP" // rerouteing proposal
	TitleSMM Title = "SMM" // slot missed (operator)
	TitleREA Title = "REA" // ready (operator)
	TitleFCM Title = "FCM" // flight confirmation (operator)
	TitleSPA Title = "SPA" // slot proposal acceptance (operator)
	TitleSRJ Title = "SRJ" // slot proposal rejection (operator)
	TitleRFI Title = "RFI" // ready for improvement (operator)
	TitleSWM Title = "SWM" // slot improvement proposal wanted (operator)
	TitleERR Title = "ERR" // error
)

// Message is one ATFM message. Fields the title does not use are empty;
// Other keeps fields this package does not name, in order and verbatim,
// so a TTO with its nested point, time and level survives a round trip.
type Message struct {
	Title Title
	// ARCID is the callsign; IFPLID the IFPS flight plan identifier.
	ARCID, IFPLID string
	// ADEP and ADES are the ICAO aerodromes; EOBD the date of the estimated
	// off-block time as ddmmyy and EOBT the time as hhmm.
	ADEP, ADES, EOBD, EOBT string
	// CTOT is the calculated take-off time on a SAM, NEWCTOT the revised
	// one on an SRM or SIP, both hhmm.
	CTOT, NEWCTOT string
	// REGUL names the regulations, most penalising first.
	REGUL []string
	// TAXITIME is the taxi time used for the profile, hhmm.
	TAXITIME string
	// REGCAUSE is the cause of the most penalising regulation.
	REGCAUSE *Cause
	// REASON explains an SLC or a rejection: OUTREG, VOID, ...
	REASON string
	// COMMENT is free text; RESPBY the time a response is due.
	COMMENT, RESPBY string
	Other           []Item
}

// Item is one field this package keeps verbatim.
type Item struct {
	Key, Value string
}

// Cause is a REGCAUSE: the regulation's cause letter, the phase of flight
// it affects, and the IATA delay code.
type Cause struct {
	Code     byte // C, I, R, S, T, A, G, E, N, M, P, W, V, O
	Location byte // D departure, E en route, A arrival
	IATA     string
}

// Cause letters, as the manual's Annex D names them.
const (
	CauseATCCapacity      byte = 'C'
	CauseATCIndustrial    byte = 'I'
	CauseATCRouteings     byte = 'R'
	CauseATCStaffing      byte = 'S'
	CauseATCEquipment     byte = 'T'
	CauseAccident         byte = 'A'
	CauseAerodromeCap     byte = 'G'
	CauseAerodromeServ    byte = 'E'
	CauseIndustrialNonATC byte = 'N'
	CauseAirspaceMgmt     byte = 'M'
	CauseSpecialEvent     byte = 'P'
	CauseWeather          byte = 'W'
	CauseEnvironmental    byte = 'V'
	CauseOther            byte = 'O'
)

// CauseName is the manual's name for a cause letter.
func CauseName(code byte) string {
	switch code {
	case CauseATCCapacity:
		return "ATC capacity"
	case CauseATCIndustrial:
		return "ATC industrial action"
	case CauseATCRouteings:
		return "ATC routeings"
	case CauseATCStaffing:
		return "ATC staffing"
	case CauseATCEquipment:
		return "ATC equipment"
	case CauseAccident:
		return "accident/incident"
	case CauseAerodromeCap:
		return "aerodrome capacity"
	case CauseAerodromeServ:
		return "aerodrome services"
	case CauseIndustrialNonATC:
		return "industrial action non-ATC"
	case CauseAirspaceMgmt:
		return "airspace management"
	case CauseSpecialEvent:
		return "special event"
	case CauseWeather:
		return "weather"
	case CauseEnvironmental:
		return "environmental issue"
	case CauseOther:
		return "other"
	}
	return "unknown"
}

// IATACode is the IATA delay code Annex D correlates with a cause at a
// phase of flight: 81 en-route ATC demand, 82 ATC staff/equipment, 83 a
// restriction at the destination, 84 weather at the destination, 89
// restrictions at the departure airport, 98 non-ATC industrial action, 99
// other; "00" when the manual gives none.
func IATACode(code, location byte) string {
	switch location {
	case 'D':
		switch code {
		case CauseAerodromeServ:
			return "99"
		case CauseIndustrialNonATC:
			return "98"
		case CauseATCRouteings:
			return "00"
		}
		return "89"
	case 'E':
		switch code {
		case CauseATCCapacity, CauseATCRouteings, CauseWeather, CauseEnvironmental, CauseOther:
			return "81"
		case CauseATCIndustrial, CauseATCStaffing, CauseATCEquipment, CauseAirspaceMgmt, CauseSpecialEvent:
			return "82"
		}
		return "00"
	case 'A':
		switch code {
		case CauseWeather:
			return "84"
		case CauseAerodromeServ:
			return "99"
		case CauseIndustrialNonATC:
			return "98"
		case CauseATCRouteings:
			return "00"
		}
		return "83"
	}
	return "00"
}

// NewCause is a cause with Annex D's IATA code.
func NewCause(code, location byte) *Cause {
	return &Cause{Code: code, Location: location, IATA: IATACode(code, location)}
}

// String renders the field value: WA 84.
func (c *Cause) String() string {
	if c == nil {
		return ""
	}
	return fmt.Sprintf("%c%c %s", c.Code, c.Location, c.IATA)
}

var causeRe = regexp.MustCompile(`^([A-Z])([DEA])\s+(\d{2})$`)

// ParseCause reads a REGCAUSE value.
func ParseCause(s string) (*Cause, error) {
	m := causeRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return nil, fmt.Errorf("atfm: REGCAUSE %q is not a cause, location and IATA code", s)
	}
	return &Cause{Code: m[1][0], Location: m[2][0], IATA: m[3]}, nil
}

// Looks reports whether text is an ATFM message: it opens with the TITLE
// field.
func Looks(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), "-TITLE ")
}

// primary is the set of top-level fields; a hyphenated token that is not
// one of these is part of the preceding field's value (a TTO's -PTID).
var primary = map[string]bool{
	"TITLE": true, "ARCID": true, "IFPLID": true, "ADEP": true, "ADES": true, "EOBD": true, "EOBT": true,
	"CTOT": true, "NEWCTOT": true, "REGUL": true, "TAXITIME": true, "REGCAUSE": true, "REASON": true,
	"COMMENT": true, "RESPBY": true, "RVR": true, "TTO": true, "ORGRTE": true, "NEWRTE": true, "ORGMSG": true,
	"POSITION": true, "PTOT": true, "MINLINEUP": true, "REJCTOT": true, "RRTEREF": true, "FILTIM": true,
	"ORGN": true, "ADDR": true, "IFPLIDSRC": true, "ERRFIELD": true, "MSGTXT": true, "TAXITIME2": true,
}

// Build renders the message, one field per line, in the manual's order.
func Build(m *Message) (string, error) {
	if m.Title == "" || m.ARCID == "" {
		return "", fmt.Errorf("atfm: a message needs a TITLE and an ARCID")
	}
	var b strings.Builder
	put := func(k, v string) {
		if v != "" {
			fmt.Fprintf(&b, "-%s %s\n", k, v)
		}
	}
	put("TITLE", string(m.Title))
	put("ARCID", m.ARCID)
	put("IFPLID", m.IFPLID)
	put("ADEP", m.ADEP)
	put("ADES", m.ADES)
	put("EOBD", m.EOBD)
	put("EOBT", m.EOBT)
	put("CTOT", m.CTOT)
	put("NEWCTOT", m.NEWCTOT)
	for _, r := range m.REGUL {
		put("REGUL", r)
	}
	put("REASON", m.REASON)
	put("COMMENT", m.COMMENT)
	put("RESPBY", m.RESPBY)
	for _, it := range m.Other {
		put(it.Key, it.Value)
	}
	put("TAXITIME", m.TAXITIME)
	put("REGCAUSE", m.REGCAUSE.String())
	return strings.TrimRight(b.String(), "\n"), nil
}

var fieldRe = regexp.MustCompile(`(?:^|\s)-([A-Z0-9]+)\b`)

// Parse reads a message. Fields may be one per line or run together on a
// line; a field's value runs to the next primary field, so nested fields
// and wrapped comments stay whole.
func Parse(text string) (*Message, error) {
	t := strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if !strings.HasPrefix(t, "-TITLE ") {
		return nil, fmt.Errorf("atfm: message does not open with -TITLE")
	}
	flat := strings.Join(strings.Fields(t), " ")
	locs := fieldRe.FindAllStringSubmatchIndex(flat, -1)
	type raw struct{ key, value string }
	var fields []raw
	for i, loc := range locs {
		key := flat[loc[2]:loc[3]]
		if !primary[key] {
			continue
		}
		end := len(flat)
		for j := i + 1; j < len(locs); j++ {
			if primary[flat[locs[j][2]:locs[j][3]]] {
				end = locs[j][0]
				break
			}
		}
		fields = append(fields, raw{key, strings.TrimSpace(flat[loc[3]:end])})
	}
	m := &Message{}
	for _, f := range fields {
		switch f.key {
		case "TITLE":
			m.Title = Title(f.value)
		case "ARCID":
			m.ARCID = f.value
		case "IFPLID":
			m.IFPLID = f.value
		case "ADEP":
			m.ADEP = f.value
		case "ADES":
			m.ADES = f.value
		case "EOBD":
			m.EOBD = f.value
		case "EOBT":
			m.EOBT = f.value
		case "CTOT":
			m.CTOT = f.value
		case "NEWCTOT":
			m.NEWCTOT = f.value
		case "REGUL":
			m.REGUL = append(m.REGUL, f.value)
		case "TAXITIME":
			m.TAXITIME = f.value
		case "REGCAUSE":
			c, err := ParseCause(f.value)
			if err != nil {
				m.Other = append(m.Other, Item{f.key, f.value})
			} else {
				m.REGCAUSE = c
			}
		case "REASON":
			m.REASON = f.value
		case "COMMENT":
			m.COMMENT = f.value
		case "RESPBY":
			m.RESPBY = f.value
		default:
			m.Other = append(m.Other, Item{f.key, f.value})
		}
	}
	if m.Title == "" || m.ARCID == "" {
		return nil, fmt.Errorf("atfm: message has no TITLE or ARCID")
	}
	return m, nil
}

// OtherValue returns a kept field's value, or "".
func (m *Message) OtherValue(key string) string {
	for _, it := range m.Other {
		if it.Key == key {
			return it.Value
		}
	}
	return ""
}
