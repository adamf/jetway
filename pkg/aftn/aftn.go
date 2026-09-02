// Package aftn implements the Aeronautical Fixed Telecommunication Network
// message envelope: the network air traffic services talk over, and Type B's
// cousin -- the same telegraph heritage, a different address plan.
//
// The format is specified, not inferred: ICAO Annex 10 Volume II chapter 4
// is public. A message is a heading (the start-of-message signal ZCZC and a
// transmission identification), an address line (a two-letter priority
// indicator and up to twenty-one eight-letter addressee indicators), an
// origin line (a six-figure filing time and the eight-letter originator
// indicator), the text, and the end-of-message signal NNNN. Annex 10's own
// page-copy example is the fixture this package is tested against:
//
//	ZCZC LPA183
//	GG LGGGZRZX LGATKLMW
//	201838 EGLLKLMW
//	(text)
//	NNNN
//
// An addressee indicator is an ICAO location indicator (four letters), a
// three-letter designator for the organisation or unit, and a filler or
// department letter. Priorities are SS distress, DD urgency, FF flight
// safety, GG meteorological, flight regularity and aeronautical information,
// KK administrative. A message may not exceed 2 100 characters from ZCZC to
// NNNN inclusive.
package aftn

import (
	"fmt"
	"regexp"
	"strings"
)

// Priority is the two-letter priority indicator.
type Priority string

const (
	PriorityDistress   Priority = "SS"
	PriorityUrgency    Priority = "DD"
	PrioritySafety     Priority = "FF"
	PriorityRegularity Priority = "GG"
	PriorityAdmin      Priority = "KK"
)

// MaxAddressees is the most addressee indicators one transmission carries.
const MaxAddressees = 21

// MaxLength is the character limit from ZCZC to NNNN inclusive.
const MaxLength = 2100

// Message is one AFTN message.
type Message struct {
	// TransmissionID is the circuit identification and channel sequence
	// number from the heading, e.g. LPA183. Empty when the heading carried
	// none, which a message handed straight to a terminal may.
	TransmissionID string
	Priority       Priority
	Addressees     []string
	// FilingTime is the six-figure date-time group DDHHMM.
	FilingTime string
	Originator string
	// Text is the message text with line endings normalised to "\n".
	Text string
}

var (
	indicatorRe = regexp.MustCompile(`^[A-Z]{8}$`)
	priorityRe  = regexp.MustCompile(`^(SS|DD|FF|GG|KK)$`)
	dtgRe       = regexp.MustCompile(`^\d{6}$`)
	// addressLineRe recognises the address line of a message, which is how
	// an AFTN message is told apart from a Type B one: a priority indicator
	// and eight-letter addressees rather than QU and seven-character ones.
	addressLineRe = regexp.MustCompile(`^(SS|DD|FF|GG|KK)( [A-Z]{8}){1,21}\s*$`)
)

// ValidIndicator reports whether s is a well-formed addressee or originator
// indicator.
func ValidIndicator(s string) bool { return indicatorRe.MatchString(s) }

// Looks reports whether raw bytes carry an AFTN message: an address line in
// the AFTN form within the first lines, after an optional ZCZC heading.
func Looks(raw []byte) bool {
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	for i, ln := range lines {
		if i > 3 {
			break
		}
		t := strings.TrimSpace(ln)
		if t == "" || strings.HasPrefix(t, "ZCZC") {
			continue
		}
		return addressLineRe.MatchString(t)
	}
	return false
}

// EncodeOptions control rendering.
type EncodeOptions struct {
	// CRLF ends lines with carriage return and line feed, as a teleprinter
	// circuit expects; the default is a bare line feed.
	CRLF bool
}

// Encode renders the message.
func (m *Message) Encode(opts EncodeOptions) ([]byte, error) {
	if !priorityRe.MatchString(string(m.Priority)) {
		return nil, fmt.Errorf("aftn: priority %q is not SS, DD, FF, GG or KK", m.Priority)
	}
	if len(m.Addressees) == 0 {
		return nil, fmt.Errorf("aftn: a message needs at least one addressee")
	}
	if len(m.Addressees) > MaxAddressees {
		return nil, fmt.Errorf("aftn: %d addressees; one transmission carries at most %d", len(m.Addressees), MaxAddressees)
	}
	for _, a := range m.Addressees {
		if !ValidIndicator(a) {
			return nil, fmt.Errorf("aftn: addressee %q is not an eight-letter indicator", a)
		}
	}
	if !ValidIndicator(m.Originator) {
		return nil, fmt.Errorf("aftn: originator %q is not an eight-letter indicator", m.Originator)
	}
	if !dtgRe.MatchString(m.FilingTime) {
		return nil, fmt.Errorf("aftn: filing time %q is not DDHHMM", m.FilingTime)
	}
	nl := "\n"
	if opts.CRLF {
		nl = "\r\n"
	}
	var b strings.Builder
	b.WriteString("ZCZC")
	if m.TransmissionID != "" {
		b.WriteString(" " + m.TransmissionID)
	}
	b.WriteString(nl)
	b.WriteString(string(m.Priority) + " " + strings.Join(m.Addressees, " ") + nl)
	b.WriteString(m.FilingTime + " " + m.Originator + nl)
	text := strings.TrimRight(strings.ReplaceAll(m.Text, "\r\n", "\n"), "\n")
	if text != "" {
		b.WriteString(strings.ReplaceAll(text, "\n", nl) + nl)
	}
	b.WriteString("NNNN" + nl)
	out := b.String()
	if len(out) > MaxLength {
		return nil, fmt.Errorf("aftn: message is %d characters; the limit from ZCZC to NNNN is %d", len(out), MaxLength)
	}
	return []byte(out), nil
}

// Parse reads a message. The heading is optional, because a message handed
// over by a terminal rather than a circuit may start at the address line.
func Parse(raw []byte) (*Message, error) {
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	var clean []string
	for _, ln := range lines {
		clean = append(clean, strings.TrimRight(ln, " \t"))
	}
	// Drop leading blank lines.
	for len(clean) > 0 && strings.TrimSpace(clean[0]) == "" {
		clean = clean[1:]
	}
	if len(clean) < 2 {
		return nil, fmt.Errorf("aftn: message too short")
	}
	m := &Message{}
	i := 0
	if strings.HasPrefix(strings.TrimSpace(clean[0]), "ZCZC") {
		rest := strings.Fields(strings.TrimPrefix(strings.TrimSpace(clean[0]), "ZCZC"))
		if len(rest) > 0 {
			m.TransmissionID = rest[0]
		}
		i++
	}
	if i >= len(clean) {
		return nil, fmt.Errorf("aftn: no address line")
	}
	addr := strings.Fields(strings.TrimSpace(clean[i]))
	if len(addr) < 2 || !priorityRe.MatchString(addr[0]) {
		return nil, fmt.Errorf("aftn: %q is not an address line", clean[i])
	}
	m.Priority = Priority(addr[0])
	for _, a := range addr[1:] {
		if !ValidIndicator(a) {
			return nil, fmt.Errorf("aftn: addressee %q is not an eight-letter indicator", a)
		}
		m.Addressees = append(m.Addressees, a)
	}
	i++
	if i >= len(clean) {
		return nil, fmt.Errorf("aftn: no origin line")
	}
	org := strings.Fields(strings.TrimSpace(clean[i]))
	if len(org) < 2 || !dtgRe.MatchString(org[0]) || !ValidIndicator(org[1]) {
		return nil, fmt.Errorf("aftn: %q is not an origin line", clean[i])
	}
	m.FilingTime, m.Originator = org[0], org[1]
	i++
	var text []string
	for _, ln := range clean[i:] {
		if strings.TrimSpace(ln) == "NNNN" {
			break
		}
		text = append(text, ln)
	}
	for len(text) > 0 && strings.TrimSpace(text[len(text)-1]) == "" {
		text = text[:len(text)-1]
	}
	m.Text = strings.Join(text, "\n")
	return m, nil
}
