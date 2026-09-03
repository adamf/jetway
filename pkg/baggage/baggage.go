// Package baggage implements the Baggage Source Message and the Baggage
// Processed Message: how a check-in system tells the sortation world a bag
// exists, and how the sortation world reports what it did with it.
//
// The formats are IATA Recommended Practice 1745. The practice is paywalled;
// this implementation is reconstructed from freely published reproductions
// and worked examples. The element grammar is simple and uniform -- a dotted
// letter identifies each line, an oblique separates its fields -- so unknown
// elements are carried verbatim rather than dropped: a bag message a profile
// cannot fully read is still evidence about a bag.
package baggage

import (
	"fmt"
	"strings"
)

// Kind distinguishes the source message from the processed report.
type Kind string

const (
	KindBSM Kind = "BSM"
	KindBPM Kind = "BPM"
	// KindBUM is the Baggage Unaccompanied Message of the same family: a
	// bag travelling without its passenger -- short-shipped at the door
	// and rushed on a later flight -- announced to the station that will
	// receive it, with the flight it rides and whose bag it is. Same
	// element grammar as the BSM; the practice's own text for it was not
	// available, so it is this package's profile.
	KindBUM Kind = "BUM"
)

// FlightLeg is one flight reference: .F outbound, .I inbound, .O onward.
type FlightLeg struct {
	Flight string // e.g. SR101
	Date   string // DDMMM
	City   string // destination for .F/.O, origin for .I
	Class  string // optional
}

// Tag is one baggage licence plate run: a ten-digit number and how many
// consecutive tags it stands for.
type Tag struct {
	Number string
	Count  int
}

// Message is one baggage message.
type Message struct {
	Kind Kind
	// Change is CHG or DEL on an update to an earlier message, empty on an
	// original.
	Change string
	// Version is the .V payload verbatim: version digit, then L (local),
	// T (transfer) or X (terminating), then the airport, e.g. 1TZRH.
	Version  string
	Outbound *FlightLeg
	Inbound  *FlightLeg
	Onward   *FlightLeg
	Tags     []Tag
	// Surname and Givens are the .P name.
	Surname string
	Givens  []string
	// Elements holds every line not modelled above, verbatim and in order:
	// .S reconciliation, .W weight, .E exceptions, .X screening, .U loads.
	Elements []string
}

// IsBaggage reports whether a Type B text is a BSM or BPM.
func IsBaggage(text string) bool {
	first := firstLine(text)
	for _, k := range []string{"BSM", "BPM", "BUM"} {
		if first == k || strings.HasPrefix(first, k+" ") {
			return true
		}
	}
	return false
}

func firstLine(text string) string {
	for _, ln := range strings.Split(text, "\n") {
		if t := strings.TrimSpace(ln); t != "" {
			return t
		}
	}
	return ""
}

// Build renders the message.
func Build(m *Message) (string, error) {
	if m.Kind != KindBSM && m.Kind != KindBPM && m.Kind != KindBUM {
		return "", fmt.Errorf("baggage: kind must be BSM, BPM or BUM, not %q", m.Kind)
	}
	if len(m.Tags) == 0 {
		return "", fmt.Errorf("baggage: a bag message without a tag is about nothing")
	}
	var b strings.Builder
	b.WriteString(string(m.Kind) + "\n")
	if m.Change != "" {
		b.WriteString(m.Change + "\n")
	}
	if m.Version != "" {
		fmt.Fprintf(&b, ".V/%s\n", m.Version)
	}
	leg := func(id string, l *FlightLeg) {
		if l == nil {
			return
		}
		s := fmt.Sprintf(".%s/%s/%s/%s", id, l.Flight, l.Date, l.City)
		if l.Class != "" {
			s += "/" + l.Class
		}
		b.WriteString(s + "\n")
	}
	leg("I", m.Inbound)
	leg("F", m.Outbound)
	leg("O", m.Onward)
	for _, tag := range m.Tags {
		count := tag.Count
		if count <= 0 {
			count = 1
		}
		fmt.Fprintf(&b, ".N/%s%03d\n", tag.Number, count)
	}
	if m.Surname != "" {
		s := ".P/" + m.Surname
		for _, g := range m.Givens {
			s += "/" + g
		}
		b.WriteString(s + "\n")
	}
	for _, e := range m.Elements {
		b.WriteString(e + "\n")
	}
	fmt.Fprintf(&b, "END%s", m.Kind)
	return b.String(), nil
}

// Parse reads a baggage message.
func Parse(text string) (*Message, error) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var clean []string
	for _, ln := range lines {
		if t := strings.TrimSpace(ln); t != "" {
			clean = append(clean, t)
		}
	}
	if len(clean) < 2 {
		return nil, fmt.Errorf("baggage: message too short")
	}
	m := &Message{Kind: Kind(clean[0])}
	if m.Kind != KindBSM && m.Kind != KindBPM && m.Kind != KindBUM {
		return nil, fmt.Errorf("baggage: identifier %q is not BSM, BPM or BUM", clean[0])
	}
	for _, ln := range clean[1:] {
		switch {
		case ln == "END"+string(m.Kind):
			if len(m.Tags) == 0 {
				return nil, fmt.Errorf("baggage: message carries no .N tag element")
			}
			return m, nil
		case ln == "CHG" || ln == "DEL":
			m.Change = ln
		case strings.HasPrefix(ln, ".V/"):
			m.Version = ln[3:]
		case strings.HasPrefix(ln, ".F/"), strings.HasPrefix(ln, ".I/"), strings.HasPrefix(ln, ".O/"):
			l, err := parseLeg(ln)
			if err != nil {
				return nil, err
			}
			switch ln[1] {
			case 'F':
				m.Outbound = l
			case 'I':
				m.Inbound = l
			case 'O':
				m.Onward = l
			}
		case strings.HasPrefix(ln, ".N/"):
			tag, err := parseTag(ln)
			if err != nil {
				return nil, err
			}
			m.Tags = append(m.Tags, tag)
		case strings.HasPrefix(ln, ".P/"):
			parts := strings.Split(ln[3:], "/")
			m.Surname = parts[0]
			m.Givens = append(m.Givens, parts[1:]...)
		default:
			m.Elements = append(m.Elements, ln)
		}
	}
	return nil, fmt.Errorf("baggage: message has no END%s line", m.Kind)
}

func parseLeg(ln string) (*FlightLeg, error) {
	parts := strings.Split(ln[3:], "/")
	if len(parts) < 3 {
		return nil, fmt.Errorf("baggage: flight element %q needs flight, date and city", ln)
	}
	l := &FlightLeg{Flight: parts[0], Date: parts[1], City: parts[2]}
	if len(parts) > 3 {
		l.Class = parts[3]
	}
	return l, nil
}

func parseTag(ln string) (Tag, error) {
	body := ln[3:]
	// Two published shapes: a thirteen-digit run where the last three digits
	// are the consecutive count, or an explicit /count.
	if i := strings.IndexByte(body, '/'); i >= 0 {
		var count int
		if _, err := fmt.Sscanf(body[i+1:], "%d", &count); err != nil {
			return Tag{}, fmt.Errorf("baggage: tag element %q has an unreadable count", ln)
		}
		return Tag{Number: body[:i], Count: count}, nil
	}
	if len(body) == 13 {
		var count int
		if _, err := fmt.Sscanf(body[10:], "%d", &count); err != nil {
			return Tag{}, fmt.Errorf("baggage: tag element %q has an unreadable count", ln)
		}
		return Tag{Number: body[:10], Count: count}, nil
	}
	if len(body) == 10 {
		return Tag{Number: body, Count: 1}, nil
	}
	return Tag{}, fmt.Errorf("baggage: tag element %q is not a licence plate", ln)
}
