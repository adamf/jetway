// Package typeb implements the IATA/ATA "Type B" teletype message envelope used
// by the SITA and ARINC store-and-forward networks.
//
// A Type B message is a line-oriented, uppercase, 7-bit text frame:
//
//	ZCZC ABC1234                 <- optional network start-of-message + channel seq
//	QU LHRRMBA NYCRMAA           <- priority code + one or more 7-char TTY addresses
//	.LONXX1A 121430              <- origin line: '.' + originator address + DDHHMM
//	                             <- optional blank line(s)
//	SSR VGML BA HK1 ...          <- message text (AIRIMP, PNL/ADL, SSM, EDIFACT, ...)
//	NNNN                         <- optional network end-of-message
//
// The envelope carries no indication of what the text means; classification is
// the job of a higher layer (see pkg/airimp and internal/gateway/classify).
//
// Parsing is deliberately lenient. Real carrier traffic contains malformed
// headers, non-conforming addresses, stray control characters and unexpected
// line breaks; a gateway that rejects them loses messages. Parse records what it
// could not understand in Diagnostics and always preserves the original bytes so
// the message can be replayed after a parser fix. Use Strict() when you need
// conformance failures to be errors instead.
package typeb

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Control characters that appear around Type B frames on real links.
const (
	SOH = 0x01
	STX = 0x02
	ETX = 0x03
	EOT = 0x04
	SYN = 0x16
	NUL = 0x00
)

// StartOfMessage and EndOfMessage are the classic teletype framing sequences.
// Many SITA feeds still wrap each message in them.
const (
	StartOfMessage = "ZCZC"
	EndOfMessage   = "NNNN"
)

// DefaultLineLength is the maximum characters per line for Type B text.
//
// IATA's Type B Messaging whitepaper (v2.1, June 2024) states the format limit
// as 60 lines of 63 characters. Configurable because individual links differ,
// but 63 is the number the standard gives.
const DefaultLineLength = 63

// DefaultMaxLines is the maximum number of text lines in a Type B message,
// from the same source. Exceeding it means the message must be split, which is
// the sender's decision and not something this package does silently.
const DefaultMaxLines = 60

// Severity classifies a parse Diagnostic.
type Severity int

const (
	// Info records something unusual but harmless.
	Info Severity = iota
	// Warn records a conformance deviation that was recovered from.
	Warn
	// Error records a deviation that lost information.
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

// Diagnostic is a single non-fatal observation made while parsing.
type Diagnostic struct {
	Severity Severity
	Line     int    // 1-based line number in the normalised input, 0 if not line-specific
	Code     string // stable machine-readable code, e.g. "bad_address"
	Detail   string
}

func (d Diagnostic) String() string {
	if d.Line > 0 {
		return fmt.Sprintf("%s:%d:%s: %s", d.Severity, d.Line, d.Code, d.Detail)
	}
	return fmt.Sprintf("%s:%s: %s", d.Severity, d.Code, d.Detail)
}

// Address is a 7-character Type B teletype address, conventionally
// LLLDDCC: 3-character location, 2-character department, 2-character
// company/airline designator (which may contain a digit, e.g. 1A = Amadeus).
type Address struct {
	Location   string
	Department string
	Carrier    string
	// Extra holds any characters beyond the 7th. A few carriers use 8-character
	// addresses; keeping the tail avoids silently corrupting their routing.
	Extra string
}

var addrRe = regexp.MustCompile(`^([A-Z0-9]{3})([A-Z0-9]{2})([A-Z0-9]{2})([A-Z0-9]*)$`)

// ParseAddress parses a TTY address. It returns an error only when the input
// cannot plausibly be an address at all.
func ParseAddress(s string) (Address, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	m := addrRe.FindStringSubmatch(s)
	if m == nil {
		return Address{}, fmt.Errorf("typeb: %q is not a valid TTY address", s)
	}
	return Address{Location: m[1], Department: m[2], Carrier: m[3], Extra: m[4]}, nil
}

// String renders the address in wire form.
func (a Address) String() string { return a.Location + a.Department + a.Carrier + a.Extra }

// IsZero reports whether the address is unset.
func (a Address) IsZero() bool { return a.Location == "" && a.Department == "" && a.Carrier == "" }

// Conventional reports whether the address follows the LLLDDCC convention with
// an alphabetic location and department. Non-conventional addresses are legal on
// the wire but worth flagging when onboarding a new link.
func (a Address) Conventional() bool {
	if len(a.Extra) != 0 {
		return false
	}
	for _, r := range a.Location + a.Department {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

// OriginTime is the DDHHMM day-of-month and UTC time stamp on the origin line.
// It carries no month or year, which is why it must never be used as an absolute
// timestamp: resolve it against the receive time instead (see Resolve).
type OriginTime struct {
	Day    int
	Hour   int
	Minute int
	// Present is false when the origin line carried no time group.
	Present bool
}

func (o OriginTime) String() string {
	if !o.Present {
		return ""
	}
	return fmt.Sprintf("%02d%02d%02d", o.Day, o.Hour, o.Minute)
}

var originTimeRe = regexp.MustCompile(`^(\d{2})(\d{2})(\d{2})$`)

func parseOriginTime(s string) (OriginTime, bool) {
	m := originTimeRe.FindStringSubmatch(s)
	if m == nil {
		return OriginTime{}, false
	}
	d, _ := strconv.Atoi(m[1])
	h, _ := strconv.Atoi(m[2])
	mi, _ := strconv.Atoi(m[3])
	if d < 1 || d > 31 || h > 23 || mi > 59 {
		return OriginTime{}, false
	}
	return OriginTime{Day: d, Hour: h, Minute: mi, Present: true}, true
}

// Message is a parsed Type B envelope.
type Message struct {
	// Framed reports that the input was wrapped in ZCZC/NNNN.
	Framed bool
	// Channel is the optional channel/sequence token following ZCZC.
	Channel string

	Priority     string
	Destinations []Address

	Origin     Address
	OriginTime OriginTime
	// OriginExtra holds trailing tokens on the origin line beyond the time group,
	// such as relay signatures or carrier-specific sequence numbers.
	OriginExtra []string

	// SMI is the Standard Message Identifier line, when the message class uses
	// one. Most AIRIMP traffic does not.
	SMI string

	// Text is the message body with line endings normalised to "\n" and no
	// trailing newline.
	Text string

	// Raw is the exact input given to Parse. It is the source of truth for
	// replay and must never be regenerated from the parsed fields.
	Raw []byte

	Diagnostics []Diagnostic
}

// HasErrors reports whether any diagnostic is at Error severity.
func (m *Message) HasErrors() bool {
	for _, d := range m.Diagnostics {
		if d.Severity == Error {
			return true
		}
	}
	return false
}

func (m *Message) diag(sev Severity, line int, code, format string, args ...any) {
	m.Diagnostics = append(m.Diagnostics, Diagnostic{
		Severity: sev, Line: line, Code: code, Detail: fmt.Sprintf(format, args...),
	})
}

// ErrEmpty is returned when the input contains no message content.
var ErrEmpty = errors.New("typeb: empty message")

// Normalise strips transport control characters and converts CRLF/CR line
// endings to LF. It is exported because raw capture layers sometimes need to
// normalise before hashing for deduplication.
func Normalise(b []byte) []byte {
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch c {
		case NUL, SOH, STX, ETX, EOT, SYN:
			continue
		case '\r':
			// CRLF -> LF; bare CR -> LF.
			if i+1 < len(b) && b[i+1] == '\n' {
				continue
			}
			out = append(out, '\n')
		default:
			out = append(out, c)
		}
	}
	return out
}

var priorityRe = regexp.MustCompile(`^Q[A-Z]$`)

// KnownPriorities are the priority codes seen in general interline use. Others
// are accepted but flagged.
var KnownPriorities = map[string]string{
	"QU": "urgent",
	"QK": "normal",
	"QD": "deferred",
	"QN": "no priority / bulk",
	"QX": "multiple address",
	"QS": "service",
	"QP": "priority",
	"QC": "circular",
	"QM": "multiple",
	"QF": "flight movement",
	"QY": "administrative",
}

// Parse decodes a Type B envelope leniently. It returns an error only when the
// input has no usable content; every other problem becomes a Diagnostic.
func Parse(raw []byte) (*Message, error) {
	m := &Message{Raw: append([]byte(nil), raw...)}

	body := string(Normalise(raw))
	if strings.TrimSpace(body) == "" {
		return nil, ErrEmpty
	}

	lines := strings.Split(body, "\n")

	// Strip ZCZC ... NNNN network framing if present.
	start := 0
	for start < len(lines) && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	if start < len(lines) {
		if f := strings.Fields(strings.TrimSpace(lines[start])); len(f) > 0 && f[0] == StartOfMessage {
			m.Framed = true
			if len(f) > 1 {
				m.Channel = f[1]
			}
			start++
		}
	}
	end := len(lines)
	for end > start && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	if end > start && strings.TrimSpace(lines[end-1]) == EndOfMessage {
		m.Framed = true
		end--
	}
	lines = lines[start:end]
	lineOffset := start // for diagnostic line numbers

	if len(lines) == 0 {
		return nil, ErrEmpty
	}

	i := 0

	// --- Address block -----------------------------------------------------
	// The first non-blank line begins with the priority code. Address lines
	// continue until the origin line ('.') or until a line that holds no
	// addresses.
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	if i < len(lines) {
		fields := strings.Fields(strings.TrimSpace(lines[i]))
		if len(fields) > 0 && priorityRe.MatchString(fields[0]) {
			m.Priority = fields[0]
			if _, ok := KnownPriorities[m.Priority]; !ok {
				m.diag(Info, lineOffset+i+1, "unknown_priority", "priority code %q is not in the known set", m.Priority)
			}
			m.appendAddresses(fields[1:], lineOffset+i+1)
			i++
			// Continuation address lines: no priority, not the origin line.
			for i < len(lines) {
				t := strings.TrimSpace(lines[i])
				if t == "" || strings.HasPrefix(t, ".") {
					break
				}
				f := strings.Fields(t)
				if !allLookLikeAddresses(f) {
					break
				}
				m.appendAddresses(f, lineOffset+i+1)
				i++
			}
		} else {
			m.diag(Warn, lineOffset+i+1, "missing_priority",
				"no priority line; treating message as headerless")
		}
	}

	// --- Origin line -------------------------------------------------------
	for j := i; j < len(lines); j++ {
		t := strings.TrimSpace(lines[j])
		if t == "" {
			continue
		}
		if strings.HasPrefix(t, ".") {
			m.parseOriginLine(t, lineOffset+j+1)
			i = j + 1
			break
		}
		// First non-blank, non-origin line: there is no origin line.
		if m.Priority != "" {
			m.diag(Warn, lineOffset+j+1, "missing_origin", "no origin line found before message text")
		}
		i = j
		break
	}

	// --- Text --------------------------------------------------------------
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	m.Text = strings.TrimRight(strings.Join(lines[i:], "\n"), "\n")

	if m.Text == "" {
		m.diag(Error, 0, "empty_text", "message has an envelope but no text")
	}
	if len(m.Destinations) == 0 && m.Priority != "" {
		m.diag(Error, 0, "no_destinations", "priority line carried no parseable addresses")
	}
	return m, nil
}

func allLookLikeAddresses(fields []string) bool {
	if len(fields) == 0 {
		return false
	}
	for _, f := range fields {
		if _, err := ParseAddress(f); err != nil {
			return false
		}
	}
	return true
}

func (m *Message) appendAddresses(fields []string, line int) {
	for _, f := range fields {
		a, err := ParseAddress(f)
		if err != nil {
			m.diag(Error, line, "bad_address", "cannot parse destination address %q", f)
			continue
		}
		if !a.Conventional() {
			m.diag(Info, line, "unconventional_address", "address %q does not follow LLLDDCC", f)
		}
		m.Destinations = append(m.Destinations, a)
	}
}

func (m *Message) parseOriginLine(t string, line int) {
	fields := strings.Fields(strings.TrimPrefix(t, "."))
	if len(fields) == 0 {
		m.diag(Error, line, "empty_origin", "origin line has no originator address")
		return
	}
	a, err := ParseAddress(fields[0])
	if err != nil {
		m.diag(Error, line, "bad_origin_address", "cannot parse originator address %q", fields[0])
	} else {
		m.Origin = a
	}
	for _, f := range fields[1:] {
		if !m.OriginTime.Present {
			if ot, ok := parseOriginTime(f); ok {
				m.OriginTime = ot
				continue
			}
		}
		m.OriginExtra = append(m.OriginExtra, f)
	}
	if !m.OriginTime.Present {
		m.diag(Warn, line, "missing_origin_time", "origin line carried no DDHHMM time group")
	}
}
