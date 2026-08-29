package ssim

import (
	"regexp"
	"strings"
)

// Recognizer turns one line of message text into a contribution to the message.
// It reports whether it claimed the line.
type Recognizer struct {
	Name  string
	Match func(line string, m *Message) bool
}

// Profile is an ordered set of recognizers, tried first to last.
//
// Schedule feeds differ between carriers and between the systems that relay
// them, and the manual that would settle the differences is not public. Giving
// each link its own profile is how a deployment absorbs that without forking
// the package.
type Profile struct {
	Name        string
	Recognizers []Recognizer
}

// Clone returns a copy that can be extended without affecting the original.
func (p *Profile) Clone(name string) *Profile {
	return &Profile{Name: name, Recognizers: append([]Recognizer{}, p.Recognizers...)}
}

// Prepend adds recognizers ahead of the existing ones, so a carrier-specific
// rule takes precedence over the generic grammar.
func (p *Profile) Prepend(rs ...Recognizer) *Profile {
	p.Recognizers = append(append([]Recognizer{}, rs...), p.Recognizers...)
	return p
}

var (
	// A flight designator with an optional operational suffix, optionally
	// followed by a date or a date range on the same line.
	flightRe = regexp.MustCompile(`^([A-Z0-9]{2}) ?(\d{1,4})([A-Z])?(?:/(.*))?$`)

	// A period: two dates, or one, optionally followed by a day pattern.
	periodRe = regexp.MustCompile(`^(\d{2}[A-Z]{3}\d{0,2})(?:\s*-?\s*(\d{2}[A-Z]{3}\d{0,2}))?(?:\s+([1-7]{1,7}))?$`)

	// A leg: two station codes with times, and optionally an aircraft type.
	legRe = regexp.MustCompile(
		`^([A-Z]{3}) ?(\d{4})? ?([A-Z]{3}) ?(\d{4}(?:[+-]\d)?)? ?([A-Z0-9]{3})?$`)

	// Equipment on a line of its own.
	equipRe = regexp.MustCompile(`^(?:EQT )?([A-Z0-9]{3})$`)

	timeModeRe = regexp.MustCompile(`^(UTC|LT)$`)
)

// Default is the generic profile.
//
// Order matters: the period and leg shapes can both match short uppercase
// lines, so the more specific pattern is tried first.
var Default = &Profile{
	Name: "ssim-default",
	Recognizers: []Recognizer{
		{Name: "flight", Match: matchFlight},
		{Name: "period", Match: matchPeriod},
		{Name: "leg", Match: matchLeg},
		{Name: "equipment", Match: matchEquipment},
	},
}

func matchFlight(line string, m *Message) bool {
	// A flight designator is only meaningful before any leg has been read;
	// afterwards a similar-looking line is something else.
	if m.Flight.Carrier != "" {
		return false
	}
	f := flightRe.FindStringSubmatch(line)
	if f == nil {
		return false
	}
	m.Flight = Flight{Carrier: f[1], Number: f[2], Suffix: f[3]}
	// A date or range may ride on the same line after a solidus, which is the
	// usual ad hoc form.
	if f[4] != "" {
		if p := periodRe.FindStringSubmatch(strings.TrimSpace(f[4])); p != nil {
			m.Period = Period{From: p[1], To: p[2], Days: p[3]}
		} else {
			m.Fragments = append(m.Fragments, f[4])
		}
	}
	return true
}

func matchPeriod(line string, m *Message) bool {
	if m.Period.From != "" {
		return false
	}
	p := periodRe.FindStringSubmatch(line)
	if p == nil {
		return false
	}
	m.Period = Period{From: p[1], To: p[2], Days: p[3]}
	return true
}

func matchLeg(line string, m *Message) bool {
	l := legRe.FindStringSubmatch(line)
	if l == nil || l[1] == "" || l[3] == "" {
		return false
	}
	m.Legs = append(m.Legs, Leg{
		Board: l[1], Depart: l[2], Off: l[3], Arrive: l[4], Equipment: l[5],
	})
	return true
}

func matchEquipment(line string, m *Message) bool {
	if len(m.Legs) > 0 || m.Equipment != "" {
		return false
	}
	e := equipRe.FindStringSubmatch(line)
	if e == nil {
		return false
	}
	m.Equipment = e[1]
	return true
}

// IsSchedule reports whether Type B message text is a schedule message.
//
// Classification is by content: the first non-blank line names the message
// type, which is the only thing about these messages that is not dialect.
func IsSchedule(text string) bool {
	_, ok := kindOf(text)
	return ok
}

func kindOf(text string) (Kind, bool) {
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		switch Kind(strings.ToUpper(line)) {
		case KindSSM:
			return KindSSM, true
		case KindASM:
			return KindASM, true
		}
		return "", false
	}
	return "", false
}

// Parse decodes schedule message text using the profile.
//
// Parsing is lenient, as everywhere else on the teletype side: a line no
// recognizer claims becomes a fragment and the message still decodes. The
// alternative is refusing traffic because a carrier writes a field in an order
// this build has not seen, which loses the very evidence needed to fix it.
func (p *Profile) Parse(text string) (*Message, error) {
	kind, ok := kindOf(text)
	if !ok {
		return nil, errNotSchedule
	}
	m := &Message{Kind: kind, TimeMode: UTC}

	lines := strings.Split(text, "\n")
	seenKind := false
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		n := i + 1

		if !seenKind {
			seenKind = true
			continue // the message identifier itself
		}
		if m.Action == "" {
			if timeModeRe.MatchString(line) {
				m.TimeMode = TimeMode(line)
				continue
			}
			if a := Action(line); a.Meaning() != "" {
				m.Action = a
				if !a.ValidFor(kind) {
					// Real: the sets differ, and an action from the wrong set
					// means the sender and this profile disagree about what the
					// message is.
					m.diag(Warn, n, "action_not_in_set",
						"%s is not an action %s uses", a, kind)
				}
				continue
			}
		}

		claimed := false
		for _, r := range p.Recognizers {
			if r.Match(line, m) {
				claimed = true
				break
			}
		}
		if !claimed {
			m.Fragments = append(m.Fragments, line)
		}
	}

	if m.Action == "" {
		m.diag(Error, 0, "no_action", "message carries no action identifier")
	}
	if m.Flight.Carrier == "" {
		m.diag(Error, 0, "no_flight", "message names no flight")
	}
	if len(m.Fragments) > 0 {
		m.diag(Info, 0, "unrecognised_lines",
			"%d line(s) no recognizer claimed; they are kept verbatim", len(m.Fragments))
	}
	return m, nil
}

// Parse decodes with the default profile.
func Parse(text string) (*Message, error) { return Default.Parse(text) }

type scheduleError string

func (e scheduleError) Error() string { return string(e) }

const errNotSchedule = scheduleError("ssim: text is not an SSM or ASM message")

// Build renders a schedule message as Type B text.
//
// The layout written here is this package's own: it is what Parse reads back,
// and it is documented as a profile rather than as the standard, because the
// standard is not public.
func (m *Message) Build() string {
	var b strings.Builder
	b.WriteString(string(m.Kind))
	b.WriteByte('\n')
	mode := m.TimeMode
	if mode == "" {
		mode = UTC
	}
	b.WriteString(string(mode))
	b.WriteByte('\n')
	b.WriteString(string(m.Action))
	b.WriteByte('\n')

	flight := m.Flight.String()
	if m.Period.From != "" && m.Period.Single() && m.Period.Days == "" {
		b.WriteString(flight + "/" + m.Period.From)
		b.WriteByte('\n')
	} else {
		b.WriteString(flight)
		b.WriteByte('\n')
		if m.Period.From != "" {
			b.WriteString(m.Period.From)
			if !m.Period.Single() {
				b.WriteString("-" + m.Period.To)
			}
			if m.Period.Days != "" {
				b.WriteString(" " + m.Period.Days)
			}
			b.WriteByte('\n')
		}
	}
	if m.Equipment != "" && len(m.Legs) == 0 {
		b.WriteString(m.Equipment)
		b.WriteByte('\n')
	}
	for _, l := range m.Legs {
		parts := []string{l.Board}
		if l.Depart != "" {
			parts = append(parts, l.Depart)
		}
		parts = append(parts, l.Off)
		if l.Arrive != "" {
			parts = append(parts, l.Arrive)
		}
		if l.Equipment != "" {
			parts = append(parts, l.Equipment)
		}
		b.WriteString(strings.Join(parts, " "))
		b.WriteByte('\n')
	}
	for _, f := range m.Fragments {
		b.WriteString(f)
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}
