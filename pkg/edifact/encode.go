package edifact

import (
	"fmt"
	"strconv"
	"strings"
)

// EncodeOptions controls interchange serialisation.
type EncodeOptions struct {
	// Syntax overrides the service characters. Zero value uses the syntax the
	// interchange was decoded with, or ISO 9735 defaults.
	Syntax *Syntax
	// EmitUNA forces a UNA service string advice. It is emitted automatically
	// whenever the syntax is non-default, because a receiver cannot decode
	// custom separators without it.
	EmitUNA bool
	// SegmentPerLine appends a line break after each segment terminator. Purely
	// cosmetic under the syntax, but many partners' tooling expects it and it
	// makes captured traffic readable.
	SegmentPerLine bool
	// CRLF uses "\r\n" instead of "\n" when SegmentPerLine is set.
	CRLF bool
	// Charset validates output. Nil skips validation.
	Charset *Charset
}

// escape prefixes every service character in s with the release character.
func escape(s string, syn Syntax) string {
	needs := false
	for i := 0; i < len(s); i++ {
		if syn.isService(s[i]) {
			needs = true
			break
		}
	}
	if !needs {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); i++ {
		if syn.isService(s[i]) {
			b.WriteByte(syn.ReleaseChar)
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// EncodeTo appends the segment's wire form, terminator included, to dst.
//
// Trailing empty components and elements are truncated, as ISO 9735 requires.
// Interior empties are preserved: their position is what carries the meaning.
func (s Segment) EncodeTo(dst *strings.Builder, syn Syntax) {
	dst.WriteString(escape(s.Tag, syn))

	// Render elements, then trim the empty tail so we do not emit "ABC+++".
	rendered := make([]string, len(s.Elements))
	for i, e := range s.Elements {
		reps := make([]string, 0, len(e))
		for _, c := range e {
			comps := make([]string, len(c))
			for j, v := range c {
				comps[j] = escape(v, syn)
			}
			for len(comps) > 0 && comps[len(comps)-1] == "" {
				comps = comps[:len(comps)-1]
			}
			reps = append(reps, strings.Join(comps, string(syn.ComponentSep)))
		}
		for len(reps) > 1 && reps[len(reps)-1] == "" {
			reps = reps[:len(reps)-1]
		}
		sep := string(syn.RepetitionSep)
		if !syn.RepetitionEnabled {
			sep = ""
			if len(reps) > 1 {
				// Repetitions cannot be represented below syntax version 4;
				// keeping only the first would silently drop data, so join with
				// the component separator, which at least preserves the values.
				sep = string(syn.ComponentSep)
			}
		}
		rendered[i] = strings.Join(reps, sep)
	}
	for len(rendered) > 0 && rendered[len(rendered)-1] == "" {
		rendered = rendered[:len(rendered)-1]
	}
	for _, r := range rendered {
		dst.WriteByte(syn.ElementSep)
		dst.WriteString(r)
	}
	dst.WriteByte(syn.SegmentTerm)
}

// String renders the segment using ISO 9735 defaults, for logs and tests.
func (s Segment) String() string {
	var b strings.Builder
	s.EncodeTo(&b, DefaultSyntax(3))
	return b.String()
}

// Encode serialises the interchange.
//
// Encode writes the segments held in ic.Segments when it is populated, so a
// decoded interchange re-encodes with its original segment order including any
// content the envelope validator complained about. Call Finalize first if you
// have mutated messages and need the counts recomputed.
func (ic *Interchange) Encode(opts EncodeOptions) ([]byte, error) {
	syn := ic.Syntax
	if syn.SegmentTerm == 0 {
		syn = DefaultSyntax(3)
	}
	if opts.Syntax != nil {
		syn = *opts.Syntax
	}
	if err := syn.Validate(); err != nil {
		return nil, err
	}

	segs := ic.Segments
	if len(segs) == 0 {
		segs = ic.flatten()
	}

	var b strings.Builder
	nl := ""
	if opts.SegmentPerLine {
		nl = "\n"
		if opts.CRLF {
			nl = "\r\n"
		}
	}
	// Emit the service string advice whenever the syntax is not fully implied by
	// the defaults, and also whenever the interchange arrived with one.
	//
	// The second condition matters more than it looks. Dropping a UNA is only
	// safe if a reader without it would infer exactly the same service
	// characters, and that inference runs through UNB -- which the service
	// characters themselves decide how to split. On a malformed UNB the two
	// readings can disagree, and the interchange then decodes differently every
	// time it is re-encoded. Keeping the UNA the sender gave us costs nine
	// bytes and removes the circularity.
	if opts.EmitUNA || !syn.IsDefault() || ic.HadUNA {
		b.WriteString(syn.UNAString())
		b.WriteString(nl)
	}
	for _, s := range segs {
		s.EncodeTo(&b, syn)
		b.WriteString(nl)
	}

	out := b.String()
	if opts.Charset != nil {
		if r, ok := opts.Charset.FirstInvalid(strings.NewReplacer("\r", "", "\n", "").Replace(out)); !ok {
			return nil, fmt.Errorf("edifact: encode: %q is outside repertoire %s", string(r), opts.Charset.Name)
		}
	}
	return []byte(out), nil
}

// flatten rebuilds a segment list from the envelope structure.
func (ic *Interchange) flatten() []Segment {
	var out []Segment
	if ic.Header.Tag != "" {
		out = append(out, ic.Header)
	}
	emitMsg := func(m Message) {
		out = append(out, m.Header)
		out = append(out, m.Segments...)
		if m.Trailer.Tag != "" {
			out = append(out, m.Trailer)
		}
	}
	if len(ic.Groups) > 0 {
		for _, g := range ic.Groups {
			out = append(out, g.Header)
			for _, m := range g.Messages {
				emitMsg(m)
			}
			if g.Trailer.Tag != "" {
				out = append(out, g.Trailer)
			}
		}
	} else {
		for _, m := range ic.Messages {
			emitMsg(m)
		}
	}
	if ic.Trailer.Tag != "" {
		out = append(out, ic.Trailer)
	}
	return out
}

// Finalize recomputes the UNT and UNZ counts and control references from the
// current message contents, then rebuilds the flat segment list.
//
// Every hand-built interchange must go through Finalize. Miscounted UNT
// segments are the single most common EDIFACT integration defect, and they are
// entirely mechanical to avoid.
func (ic *Interchange) Finalize() {
	ref := ic.ControlRef()
	for i := range ic.Messages {
		m := &ic.Messages[i]
		count := len(m.Segments) + 2
		m.Trailer = Segment{Tag: TagUNT, Elements: []Element{
			simple(strconv.Itoa(count)),
			simple(m.Reference()),
		}}
	}
	for gi := range ic.Groups {
		g := &ic.Groups[gi]
		for i := range g.Messages {
			m := &g.Messages[i]
			m.Trailer = Segment{Tag: TagUNT, Elements: []Element{
				simple(strconv.Itoa(len(m.Segments) + 2)),
				simple(m.Reference()),
			}}
		}
		g.Trailer = Segment{Tag: TagUNE, Elements: []Element{
			simple(strconv.Itoa(len(g.Messages))),
			simple(g.Header.Value(4)),
		}}
	}
	n := len(ic.Messages)
	if len(ic.Groups) > 0 {
		n = len(ic.Groups)
	}
	ic.Trailer = Segment{Tag: TagUNZ, Elements: []Element{
		simple(strconv.Itoa(n)),
		simple(ref),
	}}
	ic.Segments = ic.flatten()
}

// simple builds a single-component, non-repeating element.
func simple(v string) Element { return Element{Composite{v}} }

// Simple builds a single-component, non-repeating data element.
func Simple(v string) Element { return simple(v) }

// Comp builds a single-occurrence composite data element.
func Comp(components ...string) Element { return Element{Composite(components)} }

// Repeat builds a repeating data element from several composites.
func Repeat(occurrences ...Composite) Element { return Element(occurrences) }

// Seg builds a segment from a tag and elements.
func Seg(tag string, elems ...Element) Segment { return Segment{Tag: tag, Elements: elems} }

// UNBParams are the fields needed to construct an interchange header.
type UNBParams struct {
	CharsetID     string // S001/0001, e.g. "UNOA"
	SyntaxVersion int    // S001/0002, e.g. 4
	Sender        Party
	Recipient     Party
	Date          string // S004/0017, YYMMDD or CCYYMMDD per syntax version
	Time          string // S004/0019, HHMM
	ControlRef    string // 0020
	AppRef        string // 0026
	AckRequest    bool   // 0031
	Test          bool   // 0035
}

// NewInterchange builds an empty interchange with a populated UNB. Add messages
// then call Finalize before encoding.
func NewInterchange(p UNBParams) *Interchange {
	v := p.SyntaxVersion
	if v == 0 {
		v = 3
	}
	cs := p.CharsetID
	if cs == "" {
		cs = "UNOA"
	}
	ack, test := "", ""
	if p.AckRequest {
		ack = "1"
	}
	if p.Test {
		test = "1"
	}
	unb := Seg(TagUNB,
		Comp(cs, strconv.Itoa(v)),
		Comp(p.Sender.ID, p.Sender.Qualifier, p.Sender.RoutingAddr),
		Comp(p.Recipient.ID, p.Recipient.Qualifier, p.Recipient.RoutingAddr),
		Comp(p.Date, p.Time),
		Simple(p.ControlRef),
		Simple(""), // S005 recipient reference/password
		Simple(p.AppRef),
		Simple(""), // 0029 processing priority
		Simple(ack),
		Simple(""), // 0032 interchange agreement identifier
		Simple(test),
	)
	return &Interchange{Syntax: DefaultSyntax(v), Header: unb}
}

// AddMessage appends a message built from a UNH identifier and body segments.
func (ic *Interchange) AddMessage(ref string, id MessageID, body ...Segment) {
	unh := Seg(TagUNH,
		Simple(ref),
		Comp(id.Type, id.Version, id.Release, id.ControllingAgency, id.AssociationCode),
	)
	ic.Messages = append(ic.Messages, Message{Header: unh, Segments: body})
}
