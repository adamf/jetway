package edifact

import (
	"errors"
	"fmt"
	"strings"
)

// Severity classifies a Diagnostic.
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

// Diagnostic is a non-fatal observation made while decoding.
type Diagnostic struct {
	Severity Severity
	Offset   int
	Segment  int    // 0-based segment index, -1 when not segment-specific
	Code     string // stable machine-readable code
	Detail   string
}

func (d Diagnostic) String() string {
	return fmt.Sprintf("%s:%s @seg %d off %d: %s", d.Severity, d.Code, d.Segment, d.Offset, d.Detail)
}

// ErrNoSegments is returned when the input contains no decodable segment.
var ErrNoSegments = errors.New("edifact: input contains no segments")

// ScanOptions controls low-level decoding.
type ScanOptions struct {
	// Syntax overrides the service characters. When nil, UNA is honoured if
	// present and ISO 9735 defaults are used otherwise.
	Syntax *Syntax
	// PreserveLineBreaks keeps CR and LF that occur inside a segment as data.
	// The default drops them, which is right for the common case of a file that
	// has been line-wrapped for readability.
	PreserveLineBreaks bool
	// MaxSegments caps decoding to bound memory on hostile input. Zero means
	// DefaultMaxSegments.
	MaxSegments int
}

// DefaultMaxSegments bounds a single interchange. Real PNRGOV batches run to
// tens of thousands of segments; this leaves headroom while refusing to
// allocate without limit for a malformed stream that never terminates.
const DefaultMaxSegments = 500_000

// Scan decodes raw bytes into a flat segment list. It performs no envelope
// validation; use Parse for that.
//
// Scan is the tolerant reader at the bottom of the stack: it never fails on
// content it does not understand, only on input with no segment structure at
// all. Everything questionable becomes a Diagnostic.
func Scan(raw []byte, opts ScanOptions) ([]Segment, Syntax, []Diagnostic, error) {
	var diags []Diagnostic
	adddiag := func(sev Severity, off, seg int, code, format string, args ...any) {
		diags = append(diags, Diagnostic{sev, off, seg, code, fmt.Sprintf(format, args...)})
	}

	syn := DefaultSyntax(3)
	pos := 0

	// UNA service string advice, when present, must be the very first thing in
	// the interchange. Leading whitespace before it is a deviation seen in the
	// wild, so skip and note it.
	lead := 0
	for lead < len(raw) && (raw[lead] == ' ' || raw[lead] == '\r' || raw[lead] == '\n' || raw[lead] == '\t') {
		lead++
	}
	if lead > 0 {
		adddiag(Info, 0, -1, "leading_whitespace", "%d whitespace bytes before the interchange", lead)
	}
	pos = lead

	if len(raw) >= pos+9 && string(raw[pos:pos+3]) == "UNA" {
		syn.ComponentSep = raw[pos+3]
		syn.ElementSep = raw[pos+4]
		syn.DecimalMark = raw[pos+5]
		syn.ReleaseChar = raw[pos+6]
		rep := raw[pos+7]
		syn.SegmentTerm = raw[pos+8]
		if rep == ' ' {
			syn.RepetitionSep = DefaultRepetitionSep
			syn.RepetitionEnabled = false
		} else {
			syn.RepetitionSep = rep
			syn.RepetitionEnabled = true
		}
		pos += 9
		// A segment terminator may be followed by CR/LF, which are not data.
		for pos < len(raw) && (raw[pos] == '\r' || raw[pos] == '\n') {
			pos++
		}
	}

	if opts.Syntax != nil {
		syn = *opts.Syntax
	}
	if err := syn.Validate(); err != nil {
		return nil, syn, diags, err
	}

	maxSeg := opts.MaxSegments
	if maxSeg <= 0 {
		maxSeg = DefaultMaxSegments
	}

	var (
		segments  []Segment
		cur       Segment
		comps     Composite
		reps      Element
		elems     []Element
		buf       strings.Builder
		released  bool
		segStart  = pos
		started   bool
		segIndex  int
		truncated bool
	)

	flushComponent := func() {
		comps = append(comps, buf.String())
		buf.Reset()
	}
	flushRepetition := func() {
		flushComponent()
		reps = append(reps, comps)
		comps = nil
	}
	flushElement := func() {
		flushRepetition()
		elems = append(elems, reps)
		reps = nil
	}

	for ; pos < len(raw); pos++ {
		b := raw[pos]

		// Line breaks are framing, not data. This check precedes release
		// handling on purpose: a release character cannot promote a line break
		// to data, and pretending it can yields a value that cannot survive a
		// re-encode, since the next reader would strip it again.
		if (b == '\r' || b == '\n') && !opts.PreserveLineBreaks {
			if released {
				adddiag(Warn, pos, segIndex, "stray_release",
					"release character before a line break; both dropped")
				released = false
			} else if started {
				adddiag(Info, pos, segIndex, "linebreak_in_segment",
					"line break inside a segment was dropped")
			}
			continue
		}

		if released {
			// The standard restricts what may follow a release character to the
			// service characters themselves. Anything else is a sender bug;
			// take the byte literally rather than lose it.
			if !syn.isService(b) {
				adddiag(Warn, pos, segIndex, "stray_release",
					"release character before non-service byte %q; treated as data", string(b))
			}
			buf.WriteByte(b)
			released = false
			started = true
			continue
		}

		switch {
		case b == syn.ReleaseChar:
			released = true
			started = true

		case b == syn.ComponentSep:
			flushComponent()
			started = true

		case b == syn.ElementSep:
			flushElement()
			started = true

		case syn.RepetitionEnabled && b == syn.RepetitionSep:
			flushRepetition()
			started = true

		case b == syn.SegmentTerm:
			flushElement()
			// Element 0 holds the segment tag. A segment with no tag is
			// unusable; drop it with a diagnostic rather than emit a nameless
			// segment that downstream code will mishandle.
			tag := ""
			if len(elems) > 0 {
				tag = elems[0].Value()
			}
			if !validTag(tag) {
				adddiag(Error, segStart, segIndex, "invalid_tag",
					"segment tag %q is not a usable tag; segment dropped", tag)
			} else {
				cur = Segment{Tag: tag, Elements: elems[1:], Pos: Position{Offset: segStart, Index: segIndex}}
				segments = append(segments, cur)
				segIndex++
			}
			elems = nil
			started = false
			if len(segments) >= maxSeg {
				adddiag(Error, pos, segIndex, "segment_limit",
					"stopped after %d segments; input may be malformed or unterminated", maxSeg)
				truncated = true
			}
			// Skip line breaks that separate segments; they are not data.
			for pos+1 < len(raw) && (raw[pos+1] == '\r' || raw[pos+1] == '\n') {
				pos++
			}
			segStart = pos + 1

		default:
			buf.WriteByte(b)
			started = true
		}
		if truncated {
			break
		}
	}

	if released {
		adddiag(Error, len(raw), segIndex, "trailing_release", "input ends with a dangling release character")
	}
	// Content after the last terminator means the interchange was cut short --
	// a truncated file or a framing bug on the link. Keep what we have and say
	// so loudly; silently dropping a partial segment hides data loss.
	if started && !truncated {
		flushElement()
		tag := ""
		if len(elems) > 0 {
			tag = elems[0].Value()
		}
		if len(segments) == 0 {
			// Nothing in the input was ever terminated. This is not an
			// interchange; synthesising a segment from the whole buffer would
			// hand downstream code convincing garbage instead of a clean
			// "not EDIFACT" signal.
			return nil, syn, diags, ErrNoSegments
		}
		adddiag(Error, segStart, segIndex, "unterminated_segment",
			"input ends without a segment terminator; partial segment %q retained", tag)
		if validTag(tag) {
			segments = append(segments, Segment{Tag: tag, Elements: elems[1:], Pos: Position{Offset: segStart, Index: segIndex}})
		}
	}

	if len(segments) == 0 {
		return nil, syn, diags, ErrNoSegments
	}
	return segments, syn, diags, nil
}

// validTag reports whether t can be an EDIFACT segment tag. The standard fixes
// tags at three uppercase alphanumeric characters; this check is slightly wider
// because a few carriers ship non-conforming tags, but it still refuses
// whitespace and punctuation. Refusing matters: a segment whose tag is not a
// tag cannot be routed, cannot be re-encoded to the same bytes, and would give
// downstream code convincing garbage.
func validTag(t string) bool {
	// ISO 9735 fixes segment tags at exactly three uppercase alphanumeric
	// characters. Lowercase is tolerated because senders emit it, but the
	// length is not: a longer "tag" is the signature of a malformed stream, and
	// a six-character one starting "UNA" would be indistinguishable from a
	// service string advice when re-encoded.
	if len(t) != 3 {
		return false
	}
	for i := 0; i < len(t); i++ {
		c := t[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return false
		}
	}
	// UNA is the service string advice, never a data segment. Allowing it
	// through would let a re-encoded interchange redefine its own separators.
	return t != "UNA" && t != "una"
}
