// Package edifact implements the UN/EDIFACT interchange syntax (ISO 9735) used
// to carry IATA PADIS messages such as PAOREQ/PAORES, PNRGOV, PAXLST and the
// DCQCKI/DCRCKI check-in pair.
//
// This package deliberately stops at the syntax layer. It decodes an interchange
// into segments, elements and components, and validates the structural rules the
// standard defines (control reference matching, segment counts, release
// characters, character repertoire). It knows nothing about what a TVL or SSR
// segment means -- that is the job of pkg/padis, which drives message structure
// from data-driven dictionaries.
//
// The split matters for a gateway. Syntax rules are stable and universal;
// message structure varies by carrier, message version and bilateral agreement.
// Keeping them apart means an unknown message type still decodes losslessly and
// can be stored, routed and replayed rather than rejected at the door.
package edifact

import "fmt"

// Service character defaults from ISO 9735. These apply when no UNA segment is
// present, which is the common case for IATA traffic.
const (
	DefaultComponentSep  byte = ':'
	DefaultElementSep    byte = '+'
	DefaultDecimalMark   byte = '.'
	DefaultReleaseChar   byte = '?'
	DefaultRepetitionSep byte = '*'
	DefaultSegmentTerm   byte = '\''
)

// Syntax holds the service characters in force for an interchange.
type Syntax struct {
	ComponentSep  byte
	ElementSep    byte
	DecimalMark   byte
	ReleaseChar   byte
	RepetitionSep byte
	SegmentTerm   byte

	// Version is the syntax version from UNB S001/0002 (1..4). Repetition
	// separators exist only from version 4; RepetitionEnabled reflects that.
	Version int

	// RepetitionEnabled is set when the repetition separator is active. It is
	// false for syntax versions below 4 and when UNA reserved the position with
	// a space.
	RepetitionEnabled bool
}

// DefaultSyntax returns the ISO 9735 defaults at the given syntax version.
func DefaultSyntax(version int) Syntax {
	return Syntax{
		ComponentSep:      DefaultComponentSep,
		ElementSep:        DefaultElementSep,
		DecimalMark:       DefaultDecimalMark,
		ReleaseChar:       DefaultReleaseChar,
		RepetitionSep:     DefaultRepetitionSep,
		SegmentTerm:       DefaultSegmentTerm,
		Version:           version,
		RepetitionEnabled: version >= 4,
	}
}

// isService reports whether b is a service character under this syntax.
func (s Syntax) isService(b byte) bool {
	if b == s.ComponentSep || b == s.ElementSep || b == s.SegmentTerm || b == s.ReleaseChar {
		return true
	}
	return s.RepetitionEnabled && b == s.RepetitionSep
}

// validServiceChar reports whether b can serve as a service character.
//
// ISO 9735 does not enumerate the legal choices, but a service character must
// be distinguishable from data, so it cannot be a letter, a digit or a space.
// Restricting to printable ASCII additionally refuses a UNA whose bytes are a
// truncated multi-byte sequence -- corruption that would otherwise be accepted
// as an exotic but legal syntax and then split UTF-8 data mid-character.
func validServiceChar(b byte) bool {
	if b <= 0x20 || b > 0x7E {
		return false
	}
	switch {
	case b >= 'A' && b <= 'Z', b >= 'a' && b <= 'z', b >= '0' && b <= '9':
		return false
	}
	return true
}

// Validate checks that the service characters are distinct and usable.
func (s Syntax) Validate() error {
	if s.DecimalMark != '.' && s.DecimalMark != ',' {
		return fmt.Errorf("edifact: decimal mark must be '.' or ',', got %q", s.DecimalMark)
	}
	seen := map[byte]string{}
	for _, p := range []struct {
		b    byte
		name string
	}{
		{s.ComponentSep, "component separator"},
		{s.ElementSep, "element separator"},
		{s.ReleaseChar, "release character"},
		{s.SegmentTerm, "segment terminator"},
	} {
		if p.b == 0 {
			return fmt.Errorf("edifact: %s is unset", p.name)
		}
		if !validServiceChar(p.b) {
			return fmt.Errorf("edifact: %s %q is not a usable service character", p.name, p.b)
		}
		if prev, dup := seen[p.b]; dup {
			return fmt.Errorf("edifact: %s and %s are both %q", prev, p.name, p.b)
		}
		seen[p.b] = p.name
	}
	if s.RepetitionEnabled {
		if !validServiceChar(s.RepetitionSep) {
			return fmt.Errorf("edifact: repetition separator %q is not a usable service character", s.RepetitionSep)
		}
		if prev, dup := seen[s.RepetitionSep]; dup {
			return fmt.Errorf("edifact: %s and repetition separator are both %q", prev, s.RepetitionSep)
		}
	}
	return nil
}

// UNAString renders the UNA service string advice for this syntax.
func (s Syntax) UNAString() string {
	rep := s.RepetitionSep
	if !s.RepetitionEnabled {
		rep = ' ' // reserved for future use in syntax versions below 4
	}
	return string([]byte{'U', 'N', 'A', s.ComponentSep, s.ElementSep, s.DecimalMark, s.ReleaseChar, rep, s.SegmentTerm})
}

// MaxSyntaxVersion is the highest ISO 9735 syntax version number. Values
// outside 1..MaxSyntaxVersion in UNB S001/0002 are sender errors and must not
// be allowed to change how the interchange is decoded.
const MaxSyntaxVersion = 4

// IsDefault reports whether the syntax is fully implied by the ISO 9735
// defaults for its version, in which case a UNA segment may be omitted.
//
// The repetition rule is the subtle part. Whether the repetition separator is
// active follows from the syntax version -- off below version 4, on from
// version 4. An interchange that deviates from that implication can only say so
// with an explicit UNA, so it is not default even when every service character
// matches. Getting this wrong drops the UNA on re-encode and silently changes
// whether '*' is data or a separator.
func (s Syntax) IsDefault() bool {
	if s.ComponentSep != DefaultComponentSep ||
		s.ElementSep != DefaultElementSep ||
		s.DecimalMark != DefaultDecimalMark ||
		s.ReleaseChar != DefaultReleaseChar ||
		s.SegmentTerm != DefaultSegmentTerm {
		return false
	}
	if s.RepetitionEnabled && s.RepetitionSep != DefaultRepetitionSep {
		return false
	}
	return s.RepetitionEnabled == (s.Version >= 4)
}

// Composite is one occurrence of a data element: an ordered list of components.
// Empty components are preserved positionally because position carries meaning.
type Composite []string

// Get returns component i, or "" when absent. Bounds-safe by design: EDIFACT
// senders routinely truncate trailing components and callers should not have to
// guard every access.
func (c Composite) Get(i int) string {
	if i < 0 || i >= len(c) {
		return ""
	}
	return c[i]
}

// Element is a data element, held as its repetitions. Non-repeating elements
// have exactly one occurrence.
type Element []Composite

// First returns the first occurrence, or nil when the element is absent.
func (e Element) First() Composite {
	if len(e) == 0 {
		return nil
	}
	return e[0]
}

// Get returns component i of the first occurrence.
func (e Element) Get(i int) string { return e.First().Get(i) }

// Value returns the first component of the first occurrence, the most common
// access for a simple element.
func (e Element) Value() string { return e.Get(0) }

// Position locates a segment within the source bytes for diagnostics and audit.
type Position struct {
	Offset int // byte offset of the segment tag in the normalised input
	Index  int // 0-based segment ordinal within the interchange
}

// Segment is a decoded EDIFACT segment.
type Segment struct {
	Tag      string
	Elements []Element
	Pos      Position
}

// Elem returns element i, or nil when absent.
func (s Segment) Elem(i int) Element {
	if i < 0 || i >= len(s.Elements) {
		return nil
	}
	return s.Elements[i]
}

// Get returns component comp of element elem, or "" when absent. This is the
// workhorse accessor for mapping code.
func (s Segment) Get(elem, comp int) string { return s.Elem(elem).Get(comp) }

// Value returns the first component of element i.
func (s Segment) Value(i int) string { return s.Elem(i).Get(0) }
