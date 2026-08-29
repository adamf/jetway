// Package avs decodes and builds Availability Status messages.
//
// AVS is how a carrier grants free sale: it broadcasts, over Type B, which
// booking classes may be sold without asking. A distribution system caches
// that and sells against it, which is what removes a round trip from every
// booking.
//
// # What is guessed and what is not
//
// The normative source is AIRIMP Chapter 4, which is a paid IATA publication.
// Its published contents page names the shape -- segment status code families
// C, AS, L and LA, then Numeric Availability Status Messages as Options 1, 2
// and 3, each marked bilateral, plus selective query and enhanced data
// messages. Three numbered options that are explicitly bilateral is the
// standard saying the numeric form is agreed per partner rather than fixed.
//
// So this package makes the grammar a Profile and the status-code meanings
// configuration. That is not a workaround for the paywall; it is what the
// standard describes.
//
// The default status map contains only codes whose meaning is not in doubt.
// Anything else decodes to a diagnostic naming the unmapped code rather than a
// guess, because a wrong guess here does not fail loudly -- it sells seats that
// were never offered.
package avs

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/adamf/jetway/pkg/avail"
	"github.com/adamf/jetway/pkg/pnr"
)

// MessageIdentifier is the identifier an AVS message carries.
const MessageIdentifier = "AVS"

// IsAvailability reports whether Type B text carries availability rather than a
// booking.
//
// Exported because two places need the same answer -- the pipeline, to route
// the message, and the console, to explain it. When they disagreed, the console
// ran availability through the reservation grammar and reported every line as
// unrecognised.
func IsAvailability(text string) bool {
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		l := strings.TrimSpace(line)
		if l == "" {
			continue
		}
		return l == MessageIdentifier || strings.HasPrefix(l, MessageIdentifier+" ")
	}
	return false
}

// StatusMap translates a carrier's status codes into distribution statuses.
type StatusMap map[string]avail.Status

// DefaultStatusMap holds only the codes whose meaning is unambiguous across
// links. It is deliberately short.
//
// AIRIMP names further families -- AS, LA, and the numeric A and L codes --
// whose meanings are in the paid manual and, for the numeric ones, agreed
// bilaterally. They are absent here on purpose: an unmapped code produces a
// diagnostic an operator must resolve, whereas a guessed one quietly grants
// free sale on a class the carrier may have closed.
var DefaultStatusMap = StatusMap{
	"O": avail.Open,
	"C": avail.Closed,
	"L": avail.Waitlist,
	"R": avail.Request,
}

// Clone copies the map so a link profile can extend it without side effects.
func (m StatusMap) Clone() StatusMap {
	out := make(StatusMap, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Severity and Diagnostic mirror the other codec packages.
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

// Diagnostic is a non-fatal observation.
type Diagnostic struct {
	Severity Severity
	Line     int
	Code     string
	Detail   string
}

func (d Diagnostic) String() string {
	return fmt.Sprintf("%s:%d:%s: %s", d.Severity, d.Line, d.Code, d.Detail)
}

// Message is a decoded availability status message.
type Message struct {
	// Entries are the availability beliefs the message asserts.
	Entries []avail.Entry
	// Text is the message text as received.
	Text        string
	Profile     string
	Diagnostics []Diagnostic
}

// HasErrors reports whether anything was lost.
func (m *Message) HasErrors() bool {
	for _, d := range m.Diagnostics {
		if d.Severity == Error {
			return true
		}
	}
	return false
}

func (m *Message) diag(sev Severity, line int, code, format string, args ...any) {
	m.Diagnostics = append(m.Diagnostics, Diagnostic{sev, line, code, fmt.Sprintf(format, args...)})
}

// Profile is a per-link AVS grammar.
type Profile struct {
	Name   string
	Status StatusMap
	// Recognizers are tried first to last on each line.
	Recognizers []Recognizer
}

// context carries the flight a bare class/status line applies to.
type context struct {
	Carrier, FlightNum, WireDate, Board, Off string
	Depart                                   time.Time
	valid                                    bool
}

// Recognizer interprets one line, updating the flight context or emitting
// entries against it.
type Recognizer struct {
	Name  string
	Match func(line string, ctx *context, p *Profile, now time.Time) ([]avail.Entry, bool)
}

var (
	// A flight line names the segment that following status lines apply to:
	//   BA0175/27SEP/LHRJFK
	flightRe = regexp.MustCompile(
		`^(?:AVS +)?([A-Z0-9]{2}) ?(\d{1,4}[A-Z]?)[/ ]+(\d{2}[A-Z]{3})[/ ]+([A-Z]{3})[/ ]?([A-Z]{3})$`)

	// A status line carries one or more class/status pairs, each optionally
	// with a seat count for the numeric form:
	//   Y/O J/C M/L4
	statusTokenRe = regexp.MustCompile(`^([A-Z])[/ ]?([A-Z]{1,2})(\d{1,3})?$`)

	// A self-contained line naming the flight and one class:
	//   BA0175 27SEP LHRJFK Y O4
	singleRe = regexp.MustCompile(
		`^(?:AVS +)?([A-Z0-9]{2}) ?(\d{1,4}[A-Z]?)[/ ]+(\d{2}[A-Z]{3})[/ ]+([A-Z]{3})[/ ]?([A-Z]{3})[/ ]+([A-Z])[/ ]?([A-Z]{1,2})(\d{1,3})?$`)
)

func matchSingle(line string, ctx *context, p *Profile, now time.Time) ([]avail.Entry, bool) {
	m := singleRe.FindStringSubmatch(line)
	if m == nil {
		return nil, false
	}
	depart, err := pnr.ResolveDate(m[3], now)
	if err != nil {
		return nil, false
	}
	e, ok := p.entry(m[1], m[2], depart, m[4], m[5], m[6], m[7], m[8], now)
	if !ok {
		return nil, false
	}
	return []avail.Entry{e}, true
}

func matchFlight(line string, ctx *context, p *Profile, now time.Time) ([]avail.Entry, bool) {
	m := flightRe.FindStringSubmatch(line)
	if m == nil {
		return nil, false
	}
	depart, err := pnr.ResolveDate(m[3], now)
	if err != nil {
		return nil, false
	}
	*ctx = context{
		Carrier: m[1], FlightNum: m[2], WireDate: m[3],
		Board: m[4], Off: m[5], Depart: depart, valid: true,
	}
	return nil, true
}

func matchStatuses(line string, ctx *context, p *Profile, now time.Time) ([]avail.Entry, bool) {
	if !ctx.valid {
		return nil, false
	}
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return nil, false
	}
	var out []avail.Entry
	for _, f := range fields {
		m := statusTokenRe.FindStringSubmatch(f)
		if m == nil {
			return nil, false // all-or-nothing: a partial match is not this line
		}
		e, ok := p.entry(ctx.Carrier, ctx.FlightNum, ctx.Depart, ctx.Board, ctx.Off,
			m[1], m[2], m[3], now)
		if !ok {
			continue
		}
		out = append(out, e)
	}
	return out, true
}

// entry builds an availability entry, returning false when the status code is
// not mapped.
func (p *Profile) entry(carrier, flight string, depart time.Time,
	board, off, class, code, count string, now time.Time) (avail.Entry, bool) {

	status, known := p.Status[code]
	if !known {
		return avail.Entry{}, false
	}
	e := avail.Entry{
		Key:    avail.NewKey(carrier, flight, depart, board, off, class),
		Status: status, Source: avail.SourceAVS, AsOf: now,
	}
	if count != "" {
		if n, err := strconv.Atoi(count); err == nil {
			e.Seats, e.SeatsKnown = n, true
			// A count of zero is the carrier saying none are left, whatever the
			// letter alongside it says.
			if n == 0 {
				e.Status = avail.Closed
			}
		}
	}
	return e, true
}

// Default is the baseline profile.
//
// Recognizer order matters: the self-contained form is tried before the flight
// line, because a flight line is a prefix of it.
var Default = &Profile{
	Name:   "avs-default",
	Status: DefaultStatusMap,
	Recognizers: []Recognizer{
		{Name: "single", Match: matchSingle},
		{Name: "flight", Match: matchFlight},
		{Name: "statuses", Match: matchStatuses},
	},
}

// Clone returns a copy that can be adjusted for one link.
func (p *Profile) Clone(name string) *Profile {
	c := &Profile{Name: name, Status: p.Status.Clone()}
	c.Recognizers = append(c.Recognizers, p.Recognizers...)
	return c
}

// Parse decodes AVS message text using the default profile.
func Parse(text string, now time.Time) *Message { return Default.Parse(text, now) }

// Parse decodes AVS message text.
//
// now anchors the resolution of bare DDMMM dates and stamps the resulting
// beliefs. Pass the time the message was received, so replay reproduces the
// original reading.
func (p *Profile) Parse(text string, now time.Time) *Message {
	m := &Message{Text: text, Profile: p.Name}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	var ctx context

	for i, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || line == MessageIdentifier {
			continue
		}
		// Unmapped codes are reported independently of whether a recognizer
		// claimed the line. A line carrying one mapped code and one unmapped
		// one is otherwise silently half-applied, which is the worst outcome:
		// availability that looks complete and is not.
		for _, code := range unmappedCodes(line, p) {
			m.diag(Error, i+1, "unmapped_status_code",
				"status code %q is not mapped in profile %q; availability for that class was dropped",
				code, p.Name)
		}

		matched := false
		for _, r := range p.Recognizers {
			entries, ok := r.Match(line, &ctx, p, now)
			if !ok {
				continue
			}
			m.Entries = append(m.Entries, entries...)
			matched = true
			break
		}
		if !matched {
			m.diag(Warn, i+1, "unrecognised_line",
				"no recognizer in profile %q claimed %q", p.Name, line)
		}
	}
	return m
}

// unmappedCodes returns every status code on a line that parsed structurally
// but has no meaning configured.
func unmappedCodes(line string, p *Profile) []string {
	var out []string
	seen := map[string]bool{}
	add := func(code string) {
		if _, ok := p.Status[code]; ok || seen[code] {
			return
		}
		seen[code] = true
		out = append(out, code)
	}
	if m := singleRe.FindStringSubmatch(line); m != nil {
		add(m[7])
		return out
	}
	for _, f := range strings.Fields(line) {
		if m := statusTokenRe.FindStringSubmatch(f); m != nil {
			add(m[2])
		}
	}
	return out
}

// Build renders availability entries as AVS message text, grouping classes by
// segment. codeFor inverts the profile's status map.
func (p *Profile) Build(entries []avail.Entry) string {
	inverse := map[avail.Status]string{}
	// Prefer the shortest code for a status, so a map with synonyms is stable.
	for code, st := range p.Status {
		if cur, ok := inverse[st]; !ok || len(code) < len(cur) || (len(code) == len(cur) && code < cur) {
			inverse[st] = code
		}
	}

	type group struct {
		head    string
		classes []string
	}
	var order []string
	byFlight := map[string]*group{}

	for _, e := range entries {
		code, ok := inverse[e.Status]
		if !ok {
			continue
		}
		date, err := time.Parse("2006-01-02", e.Key.Date)
		if err != nil {
			continue
		}
		head := fmt.Sprintf("%s%s/%s/%s%s",
			e.Key.Carrier, e.Key.FlightNum, pnr.FormatDate(date), e.Key.Board, e.Key.Off)
		g, seen := byFlight[head]
		if !seen {
			g = &group{head: head}
			byFlight[head] = g
			order = append(order, head)
		}
		tok := e.Key.Class + "/" + code
		if e.SeatsKnown {
			tok += strconv.Itoa(e.Seats)
		}
		g.classes = append(g.classes, tok)
	}

	var b strings.Builder
	b.WriteString(MessageIdentifier)
	for _, head := range order {
		g := byFlight[head]
		b.WriteString("\n" + g.head + "\n" + strings.Join(g.classes, " "))
	}
	return b.String()
}

// Build renders entries using the default profile.
func Build(entries []avail.Entry) string { return Default.Build(entries) }
