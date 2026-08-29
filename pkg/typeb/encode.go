package typeb

import (
	"fmt"
	"strings"
	"unicode"
)

// EncodeOptions controls Type B serialisation.
type EncodeOptions struct {
	// MaxLineLength wraps address lines and validates text lines. Zero uses
	// DefaultLineLength. Set to -1 to disable wrapping entirely.
	MaxLineLength int
	// MaxLines bounds the number of text lines. Zero uses DefaultMaxLines; set
	// to -1 to disable the check.
	MaxLines int
	// Frame wraps the output in ZCZC/NNNN network framing.
	Frame bool
	// Channel is emitted after ZCZC when Frame is set.
	Channel string
	// CRLF emits "\r\n" line endings, which most links expect.
	CRLF bool
	// BlankLineAfterOrigin inserts an empty line between the origin line and the
	// text. Some carriers require it; others reject it.
	BlankLineAfterOrigin bool
	// Charset validates the text. Nil skips validation.
	Charset *Charset
}

func (o EncodeOptions) maxLine() int {
	switch {
	case o.MaxLineLength < 0:
		return 1 << 30
	case o.MaxLineLength == 0:
		return DefaultLineLength
	default:
		return o.MaxLineLength
	}
}

// Encode renders the message in Type B wire form.
//
// Encode is not the inverse of Parse for arbitrary input: Parse is lenient and
// normalises whitespace, so round-tripping a non-conforming message produces a
// conforming one. Never use Encode to reproduce a received message for audit
// or replay; use Message.Raw for that.
func (m *Message) Encode(opts EncodeOptions) ([]byte, error) {
	if len(m.Destinations) == 0 {
		return nil, fmt.Errorf("typeb: encode: message has no destination addresses")
	}
	if m.Origin.IsZero() {
		return nil, fmt.Errorf("typeb: encode: message has no origin address")
	}
	prio := m.Priority
	if prio == "" {
		prio = "QU"
	}
	if !priorityRe.MatchString(prio) {
		return nil, fmt.Errorf("typeb: encode: invalid priority %q", prio)
	}

	var lines []string
	if opts.Frame {
		z := StartOfMessage
		if opts.Channel != "" {
			z += " " + opts.Channel
		}
		lines = append(lines, z)
	}

	// Address lines: priority prefixes the first line only; wrap on whole
	// addresses so an address is never split across lines.
	max := opts.maxLine()
	cur := prio
	for _, d := range m.Destinations {
		s := d.String()
		if len(cur)+1+len(s) > max && cur != prio {
			lines = append(lines, cur)
			cur = s
			continue
		}
		if len(cur)+1+len(s) > max && cur == prio {
			// Priority plus one address already exceeds the limit; emit anyway
			// rather than produce an unroutable header.
			lines = append(lines, cur+" "+s)
			cur = ""
			continue
		}
		if cur == "" {
			cur = s
		} else {
			cur += " " + s
		}
	}
	if cur != "" && cur != prio {
		lines = append(lines, cur)
	}

	origin := "." + m.Origin.String()
	if m.OriginTime.Present {
		origin += " " + m.OriginTime.String()
	}
	for _, e := range m.OriginExtra {
		origin += " " + e
	}
	lines = append(lines, origin)

	if opts.BlankLineAfterOrigin {
		lines = append(lines, "")
	}
	if m.SMI != "" {
		lines = append(lines, m.SMI)
	}

	text := strings.ReplaceAll(m.Text, "\r\n", "\n")
	if opts.Charset != nil {
		if bad, ok := opts.Charset.FirstInvalid(text); !ok {
			return nil, fmt.Errorf("typeb: encode: character %q is not permitted in charset %s", bad, opts.Charset.Name)
		}
	}
	if text != "" {
		textLines := strings.Split(text, "\n")
		maxLines := opts.MaxLines
		if maxLines == 0 {
			maxLines = DefaultMaxLines
		}
		// Refuse rather than truncate. A message over the limit needs splitting
		// across several Type B messages, which changes their meaning and is
		// the sender's decision, not this encoder's.
		if maxLines > 0 && len(textLines) > maxLines {
			return nil, fmt.Errorf("typeb: encode: text has %d lines, the limit is %d; the message must be split",
				len(textLines), maxLines)
		}
		for _, tl := range textLines {
			if len(tl) > max {
				return nil, fmt.Errorf("typeb: encode: text line exceeds %d characters: %q", max, tl)
			}
			lines = append(lines, tl)
		}
	}

	if opts.Frame {
		lines = append(lines, EndOfMessage)
	}

	sep := "\n"
	if opts.CRLF {
		sep = "\r\n"
	}
	return []byte(strings.Join(lines, sep) + sep), nil
}

// Charset describes the characters a link will accept.
type Charset struct {
	Name    string
	allowed map[rune]bool
}

// FirstInvalid returns the first disallowed rune in s.
func (c *Charset) FirstInvalid(s string) (rune, bool) {
	for _, r := range s {
		if r == '\n' {
			continue
		}
		if !c.allowed[r] {
			return r, false
		}
	}
	return 0, true
}

// Valid reports whether every rune in s is permitted.
func (c *Charset) Valid(s string) bool {
	_, ok := c.FirstInvalid(s)
	return ok
}

func newCharset(name, extra string) *Charset {
	c := &Charset{Name: name, allowed: map[rune]bool{}}
	for r := 'A'; r <= 'Z'; r++ {
		c.allowed[r] = true
	}
	for r := '0'; r <= '9'; r++ {
		c.allowed[r] = true
	}
	for _, r := range extra {
		c.allowed[r] = true
	}
	return c
}

// CharsetITA2 is the conservative teletype set: uppercase letters, digits and
// the punctuation reliably carried by five-bit Baudot-derived links. Use it when
// a carrier's ICD does not say otherwise.
var CharsetITA2 = newCharset("ITA2", " .,-/()':=+?")

// CharsetIA5 is the wider set most modern Type B links accept.
var CharsetIA5 = newCharset("IA5", " .,-/()':=+?*#%&@<>!\"$;_")

// SanitiseText uppercases s and replaces characters outside the charset with
// the replacement rune, returning the result and the number of substitutions.
// Gateways should prefer rejecting to sanitising on egress, but sanitising is
// the right call when relaying text that a partner already accepted.
func SanitiseText(s string, cs *Charset, replacement rune) (string, int) {
	var b strings.Builder
	n := 0
	for _, r := range strings.ToUpper(s) {
		if r == '\n' || cs.allowed[r] {
			b.WriteRune(r)
			continue
		}
		if unicode.IsSpace(r) {
			b.WriteRune(' ')
			continue
		}
		b.WriteRune(replacement)
		n++
	}
	return b.String(), n
}
