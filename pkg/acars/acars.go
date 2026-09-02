// Package acars reads and writes the OOOI reports an aircraft's datalink
// produces -- Out of the gate, Off the ground, On the ground, In to the gate
// -- as they reach an airline's host over the ground network.
//
// An aircraft talks to a datalink service provider over the air (ARINC 618);
// the provider forwards to the airline as a ground-ground message (ARINC
// 620) that rides the same teletype network as everything else, with a
// standard message identifier of DEP for the Out and Off reports and ARR for
// On and In. Both ARINC documents are sold. This package is built from OAG's
// freely published "ACARS: Data Elements and Message Examples", whose
// verbatim examples it reproduces and is tested against:
//
//	QU ANPOCJA
//	.DDLXCXA 010030
//	DEP
//	FI JA401/AN CC-AWE/DA SPJC/DS SCEL/OT 0030/FB /BF
//	DT DDL LIM 010030 M17A
//
// The FI line carries the flight identification and, after it,
// slash-separated element identifiers: AN registration, DA aerodrome of
// departure, DS destination station, AD aerodrome of arrival, OT/OF/ON/IN
// the four times, FB fuel on board, BF boarded fuel. The DT line is the
// provider's communication service information: provider, ground station,
// time, message number. Per this repository's rule the format is inferred
// rather than conformant: elements this package does not know are kept
// verbatim, and a free-text block after the DT line is kept whole.
package acars

import (
	"fmt"
	"strings"
)

// Kind is the standard message identifier.
type Kind string

const (
	KindDEP Kind = "DEP" // Out and Off reports
	KindARR Kind = "ARR" // On and In reports
)

// Service is the DT line: who carried the message and from where.
type Service struct {
	Provider string // e.g. DDL
	Station  string // ground station, e.g. LIM
	Time     string // DDHHMM
	Number   string // message number, e.g. M17A
}

// Message is one OOOI report.
type Message struct {
	Kind Kind
	// Flight is the identification as written: JA401, HX0112.
	Flight       string
	Registration string // AN
	Departure    string // DA, ICAO
	Destination  string // DS, ICAO
	Arrival      string // AD, ICAO
	// The four times, HHMM UTC, empty when the report does not carry them.
	Out, Off, On, In string
	FuelOnBoard      string // FB, as written
	BoardedFuel      string // BF, as written
	Service          *Service
	// Elements keeps FI-line identifiers this package does not model, in
	// order, as identifier and value.
	Elements []Element
	// Text keeps any free-text block after the DT line, verbatim.
	Text string
}

// Element is one unmodelled identifier/value pair from the FI line.
type Element struct {
	ID    string
	Value string
}

// IsOOOI reports whether a Type B text is an OOOI report: a DEP or ARR
// identifier followed by an FI line.
func IsOOOI(text string) bool {
	ls := lines(text)
	if len(ls) < 2 {
		return false
	}
	switch Kind(ls[0]) {
	case KindDEP, KindARR:
	default:
		return false
	}
	return strings.HasPrefix(ls[1], "FI ")
}

func lines(text string) []string {
	var out []string
	for _, ln := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if t := strings.TrimSpace(ln); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// Build renders the report.
func Build(m *Message) (string, error) {
	if m.Kind != KindDEP && m.Kind != KindARR {
		return "", fmt.Errorf("acars: kind must be DEP or ARR, not %q", m.Kind)
	}
	if m.Flight == "" {
		return "", fmt.Errorf("acars: a report needs a flight identification")
	}
	var b strings.Builder
	b.WriteString(string(m.Kind) + "\n")
	b.WriteString("FI " + m.Flight)
	add := func(id, v string) {
		if v != "" {
			b.WriteString("/" + id + " " + v)
		}
	}
	add("AN", m.Registration)
	add("DA", m.Departure)
	add("DS", m.Destination)
	add("AD", m.Arrival)
	add("OT", m.Out)
	add("OF", m.Off)
	add("ON", m.On)
	add("IN", m.In)
	add("FB", m.FuelOnBoard)
	add("BF", m.BoardedFuel)
	for _, e := range m.Elements {
		add(e.ID, e.Value)
	}
	b.WriteString("\n")
	if s := m.Service; s != nil {
		fmt.Fprintf(&b, "DT %s %s %s %s\n", s.Provider, s.Station, s.Time, s.Number)
	}
	if m.Text != "" {
		b.WriteString(strings.TrimRight(m.Text, "\n") + "\n")
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// Parse reads a report.
func Parse(text string) (*Message, error) {
	ls := lines(text)
	if len(ls) < 2 {
		return nil, fmt.Errorf("acars: message too short")
	}
	m := &Message{Kind: Kind(ls[0])}
	if m.Kind != KindDEP && m.Kind != KindARR {
		return nil, fmt.Errorf("acars: identifier %q is not DEP or ARR", ls[0])
	}
	if !strings.HasPrefix(ls[1], "FI ") {
		return nil, fmt.Errorf("acars: no FI line")
	}
	parts := strings.Split(strings.TrimPrefix(ls[1], "FI "), "/")
	m.Flight = strings.TrimSpace(parts[0])
	for _, p := range parts[1:] {
		id, val, _ := strings.Cut(strings.TrimSpace(p), " ")
		val = strings.TrimSpace(val)
		switch id {
		case "AN":
			m.Registration = val
		case "DA":
			m.Departure = val
		case "DS":
			m.Destination = val
		case "AD":
			m.Arrival = val
		case "OT", "OUT":
			m.Out = val
		case "OF", "OFF":
			m.Off = val
		case "ON":
			m.On = val
		case "IN":
			m.In = val
		case "FB":
			m.FuelOnBoard = val
		case "BF":
			m.BoardedFuel = val
		default:
			if id != "" {
				m.Elements = append(m.Elements, Element{ID: id, Value: val})
			}
		}
	}
	var body []string
	for _, ln := range ls[2:] {
		if strings.HasPrefix(ln, "DT ") && m.Service == nil {
			f := strings.Fields(ln)
			s := &Service{}
			if len(f) > 1 {
				s.Provider = f[1]
			}
			if len(f) > 2 {
				s.Station = f[2]
			}
			if len(f) > 3 {
				s.Time = f[3]
			}
			if len(f) > 4 {
				s.Number = f[4]
			}
			m.Service = s
			continue
		}
		body = append(body, ln)
	}
	m.Text = strings.Join(body, "\n")
	return m, nil
}
