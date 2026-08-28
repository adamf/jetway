package airimp

import (
	"fmt"
	"regexp"
	"strings"
)

// Severity and Diagnostic mirror the other codec packages so a gateway can
// present one diagnostic stream regardless of which layer produced it.
type Severity int

const (
	Info Severity = iota
	Warn
	Error
)

func (s Severity) String() string {
	switch s {
	case Info:
		return "info"
	case Warn:
		return "warn"
	case Error:
		return "error"
	}
	return "unknown"
}

// Diagnostic is a non-fatal observation made while parsing message text.
type Diagnostic struct {
	Severity Severity
	Line     int
	Code     string
	Detail   string
}

func (d Diagnostic) String() string {
	return fmt.Sprintf("%s:%d:%s: %s", d.Severity, d.Line, d.Code, d.Detail)
}

// Message is parsed AIRIMP message text.
type Message struct {
	// Identifier is the message identifier when the text carries one on its own
	// line. Much interline traffic omits it and is classified from content
	// instead; see Intent.
	Identifier string
	Elements   []Element
	// Text is the message text exactly as received.
	Text        string
	Profile     string
	Diagnostics []Diagnostic
}

// Segments returns the flight segment elements in wire order.
func (m *Message) Segments() []*Segment {
	var out []*Segment
	for _, e := range m.Elements {
		if s, ok := e.(*Segment); ok {
			out = append(out, s)
		}
	}
	return out
}

// Names returns the name elements in wire order.
func (m *Message) Names() []*Name {
	var out []*Name
	for _, e := range m.Elements {
		if n, ok := e.(*Name); ok {
			out = append(out, n)
		}
	}
	return out
}

// SSRs returns the special service request elements in wire order.
func (m *Message) SSRs() []*SSR {
	var out []*SSR
	for _, e := range m.Elements {
		if s, ok := e.(*SSR); ok {
			out = append(out, s)
		}
	}
	return out
}

// Locators returns any carrier record locator elements.
func (m *Message) Locators() []*Locator {
	var out []*Locator
	for _, e := range m.Elements {
		if l, ok := e.(*Locator); ok {
			out = append(out, l)
		}
	}
	return out
}

// Unknowns returns the lines no recognizer claimed. A link that produces these
// steadily is speaking a dialect the profile does not cover yet.
func (m *Message) Unknowns() []*Unknown {
	var out []*Unknown
	for _, e := range m.Elements {
		if u, ok := e.(*Unknown); ok {
			out = append(out, u)
		}
	}
	return out
}

// Intent is what the message asks the receiver to do, derived from the segment
// action codes rather than from the message identifier, because the identifier
// is frequently absent.
type Intent string

const (
	IntentSell     Intent = "sell"      // segments carry request codes
	IntentReply    Intent = "reply"     // segments carry reply codes
	IntentCancel   Intent = "cancel"    // segments carry cancellation codes
	IntentAdvise   Intent = "advise"    // schedule change and similar
	IntentInfoOnly Intent = "info_only" // SSR/OSI with no segment action
	IntentUnknown  Intent = "unknown"
)

// Intent classifies the message.
func (m *Message) Intent() Intent {
	counts := map[Category]int{}
	for _, s := range m.Segments() {
		counts[s.Action.Category()]++
	}
	switch {
	case counts[CatRequest] > 0:
		return IntentSell
	case counts[CatReply] > 0:
		return IntentReply
	case counts[CatCancel] > 0:
		return IntentCancel
	case counts[CatAdvice] > 0:
		return IntentAdvise
	case len(m.SSRs()) > 0 || len(m.Segments()) > 0:
		return IntentInfoOnly
	}
	return IntentUnknown
}

func (m *Message) diag(sev Severity, line int, code, format string, args ...any) {
	m.Diagnostics = append(m.Diagnostics, Diagnostic{sev, line, code, fmt.Sprintf(format, args...)})
}

// Recognizer turns one line of message text into an Element.
type Recognizer struct {
	Name  string
	Match func(line string) (Element, bool)
}

// Profile is an ordered set of recognizers, tried first to last. Carrier
// dialects differ enough that one global grammar does not survive contact with
// production; give each link its own profile and extend it as you learn.
type Profile struct {
	Name        string
	Recognizers []Recognizer
	// Identifiers are message identifiers this profile recognises on a line of
	// their own.
	Identifiers map[string]bool
}

// Clone returns a copy that can be extended without affecting the original.
func (p *Profile) Clone(name string) *Profile {
	c := &Profile{Name: name, Identifiers: map[string]bool{}}
	c.Recognizers = append(c.Recognizers, p.Recognizers...)
	for k, v := range p.Identifiers {
		c.Identifiers[k] = v
	}
	return c
}

// Prepend adds recognizers ahead of the existing ones, so a carrier-specific
// rule can take precedence over the generic grammar.
func (p *Profile) Prepend(rs ...Recognizer) *Profile {
	p.Recognizers = append(append([]Recognizer{}, rs...), p.Recognizers...)
	return p
}

var (
	// Optional whitespace is permitted between fields: carriers disagree on
	// whether the segment element is written solid or spaced.
	segmentRe = regexp.MustCompile(
		`^(?:-)?([A-Z0-9]{2}) ?(\d{1,4}[A-Z]?) ?([A-Z]) ?(\d{2}[A-Z]{3}) ?([A-Z]{3}) ?([A-Z]{3}) ?([A-Z]{2}) ?(\d{1,2})(.*)$`)

	nameRe = regexp.MustCompile(`^(?:-)?(\d{1,2})([A-Z][A-Z0-9 '.-]*)/(.+)$`)

	ssrRe = regexp.MustCompile(
		`^SSR +([A-Z]{4}) +([A-Z0-9]{2}) +([A-Z]{2})(\d{0,2})(?: +(.*))?$`)

	osiRe = regexp.MustCompile(`^OSI +([A-Z0-9]{2}) +(.+)$`)

	locatorRe = regexp.MustCompile(`^(?:RL|\.L) *([A-Z0-9]{2})?/?([A-Z0-9]{5,8})$`)

	prefixRe = regexp.MustCompile(`^(TK|AP|RF|RM) +(.+)$`)

	// An itinerary reference inside an SSR, e.g. LHRJFK0175Y15JUN.
	ssrItinRe = regexp.MustCompile(`^[A-Z]{3}[A-Z]{3}\d{1,4}[A-Z]?[A-Z]\d{2}[A-Z]{3}$`)
)

func matchSegment(line string) (Element, bool) {
	m := segmentRe.FindStringSubmatch(line)
	if m == nil {
		return nil, false
	}
	return &Segment{
		Carrier: m[1], FlightNum: m[2], Class: m[3], Date: m[4],
		Board: m[5], Off: m[6], Action: ActionCode(m[7]),
		Seats: atoiOr(m[8], 1), Trailer: m[9],
	}, true
}

func matchName(line string) (Element, bool) {
	m := nameRe.FindStringSubmatch(line)
	if m == nil {
		return nil, false
	}
	givens := strings.Split(m[3], "/")
	n := &Name{Count: atoiOr(m[1], 1), Surname: strings.TrimSpace(m[2]), Givens: givens}
	for _, g := range givens {
		if strings.Contains(g, "INF") {
			n.Infant = true
		}
	}
	return n, true
}

func matchSSR(line string) (Element, bool) {
	m := ssrRe.FindStringSubmatch(line)
	if m == nil {
		return nil, false
	}
	s := &SSR{Code: m[1], Carrier: m[2], Action: ActionCode(m[3]), Count: atoiOr(m[4], 1)}
	rest := strings.TrimSpace(m[5])
	// A trailing "/name" associates the request with one traveller. Split on the
	// last slash only: DOCS free text is itself slash-delimited.
	if i := strings.LastIndex(rest, "/-"); i >= 0 {
		s.NameRef = strings.TrimPrefix(rest[i+1:], "-")
		rest = strings.TrimSpace(rest[:i])
	}
	if fields := strings.Fields(rest); len(fields) > 0 && ssrItinRe.MatchString(fields[0]) {
		s.Itinerary = fields[0]
		rest = strings.TrimSpace(strings.TrimPrefix(rest, fields[0]))
	}
	s.FreeText = rest
	return s, true
}

func matchOSI(line string) (Element, bool) {
	m := osiRe.FindStringSubmatch(line)
	if m == nil {
		return nil, false
	}
	return &OSI{Carrier: m[1], Text: strings.TrimSpace(m[2])}, true
}

func matchLocator(line string) (Element, bool) {
	m := locatorRe.FindStringSubmatch(line)
	if m == nil {
		return nil, false
	}
	return &Locator{Carrier: m[1], Value: m[2]}, true
}

func matchPrefixed(line string) (Element, bool) {
	m := prefixRe.FindStringSubmatch(line)
	if m == nil {
		return nil, false
	}
	text := strings.TrimSpace(m[2])
	switch m[1] {
	case "TK":
		return &Ticketing{Text: text}, true
	case "AP":
		return &Contact{Text: text}, true
	case "RF":
		return &ReceivedFrom{Text: text}, true
	case "RM":
		return &Remark{Text: text}, true
	}
	return nil, false
}

// Default is the baseline interline profile.
//
// Recognizer order matters. Segment and name elements have no keyword prefix,
// so the keyword-led elements are tried first and the positional ones last;
// otherwise an "SSR ..." line can be mistaken for free text.
var Default = &Profile{
	Name: "airimp-default",
	Recognizers: []Recognizer{
		{Name: "ssr", Match: matchSSR},
		{Name: "osi", Match: matchOSI},
		{Name: "locator", Match: matchLocator},
		{Name: "prefixed", Match: matchPrefixed},
		{Name: "segment", Match: matchSegment},
		{Name: "name", Match: matchName},
	},
	Identifiers: map[string]bool{
		"SS": true, "SR": true, "CX": true, "RC": true, "AV": true,
		"KK": true, "UC": true, "US": true, "UU": true, "NO": true,
		"SC": true, "DIV": true, "NAM": true, "CHG": true, "CNL": true,
	},
}

// Parse decodes AIRIMP message text using the default profile.
func Parse(text string) *Message { return Default.Parse(text) }

// Parse decodes AIRIMP message text.
//
// Parsing never fails. Lines the profile does not recognise become Unknown
// elements, which keeps an unfamiliar dialect routable and replayable instead
// of turning it into a dropped message.
func (p *Profile) Parse(text string) *Message {
	m := &Message{Text: text, Profile: p.Name}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")

	start := 0
	// A message identifier occupies a line by itself.
	for start < len(lines) {
		t := strings.TrimSpace(lines[start])
		if t == "" {
			start++
			continue
		}
		if p.Identifiers[strings.ToUpper(t)] {
			m.Identifier = strings.ToUpper(t)
			start++
		}
		break
	}

	for i := start; i < len(lines); i++ {
		raw := lines[i]
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		// A lone "E" or ".E" terminates the text on some links.
		if line == "E" || line == ".E" {
			continue
		}
		matched := false
		for _, r := range p.Recognizers {
			if el, ok := r.Match(line); ok {
				m.Elements = append(m.Elements, el)
				matched = true
				break
			}
		}
		if !matched {
			m.Elements = append(m.Elements, &Unknown{Line: raw, LineNo: i + 1})
			m.diag(Warn, i+1, "unrecognised_element",
				"no recognizer in profile %q claimed %q", p.Name, line)
		}
	}

	for _, s := range m.Segments() {
		if _, known := s.Action.Info(); !known {
			m.diag(Info, 0, "private_action_code",
				"action code %q is not in the interline vocabulary", s.Action)
		}
	}
	return m
}

// Build renders elements as AIRIMP message text.
func Build(identifier string, elements ...Element) string {
	var b strings.Builder
	if identifier != "" {
		b.WriteString(identifier)
		b.WriteByte('\n')
	}
	for i, e := range elements {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(e.Wire())
	}
	return b.String()
}
