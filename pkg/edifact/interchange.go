package edifact

import (
	"fmt"
	"strconv"
	"strings"
)

// Service segment tags defined by ISO 9735.
const (
	TagUNA = "UNA"
	TagUNB = "UNB"
	TagUNG = "UNG"
	TagUNE = "UNE"
	TagUNH = "UNH"
	TagUNT = "UNT"
	TagUNZ = "UNZ"
)

// Message is a single UNH..UNT message within an interchange.
type Message struct {
	Header Segment // UNH
	// Segments is the message body: everything between UNH and UNT exclusive.
	Segments []Segment
	Trailer  Segment // UNT; zero-valued when the message was unterminated
}

// MessageID identifies a message type from UNH composite S009.
type MessageID struct {
	Type              string // 0065, e.g. "PAORES"
	Version           string // 0052, e.g. "96"
	Release           string // 0054, e.g. "1"
	ControllingAgency string // 0051, e.g. "IA" for IATA
	AssociationCode   string // 0057
}

// String renders the identifier in the conventional colon-joined form used by
// implementation guides, e.g. "PAORES:96:1:IA".
func (m MessageID) String() string {
	parts := []string{m.Type, m.Version, m.Release, m.ControllingAgency}
	if m.AssociationCode != "" {
		parts = append(parts, m.AssociationCode)
	}
	for len(parts) > 1 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return strings.Join(parts, ":")
}

// ID returns the message identifier from the UNH header.
func (m Message) ID() MessageID {
	c := m.Header.Elem(1).First()
	return MessageID{
		Type:              c.Get(0),
		Version:           c.Get(1),
		Release:           c.Get(2),
		ControllingAgency: c.Get(3),
		AssociationCode:   c.Get(4),
	}
}

// Reference returns the message reference number (UNH 0062), which the UNT must
// echo.
func (m Message) Reference() string { return m.Header.Value(0) }

// Find returns every segment with the given tag, in order.
func (m Message) Find(tag string) []Segment {
	var out []Segment
	for _, s := range m.Segments {
		if s.Tag == tag {
			out = append(out, s)
		}
	}
	return out
}

// First returns the first segment with the given tag and whether it was found.
func (m Message) First(tag string) (Segment, bool) {
	for _, s := range m.Segments {
		if s.Tag == tag {
			return s, true
		}
	}
	return Segment{}, false
}

// Group is a UNG..UNE functional group. IATA traffic rarely uses groups, but
// they are legal and must not break the decoder.
type Group struct {
	Header   Segment // UNG
	Messages []Message
	Trailer  Segment // UNE
}

// Party identifies an interchange sender or recipient (UNB S002/S003).
type Party struct {
	ID          string // 0004 / 0010
	Qualifier   string // 0007
	RoutingAddr string // 0008 reverse routing / 0014 routing address
	InternalID  string // syntax v4 only
	InternalSub string // syntax v4 only
}

func partyFrom(c Composite) Party {
	return Party{
		ID:          c.Get(0),
		Qualifier:   c.Get(1),
		RoutingAddr: c.Get(2),
		InternalID:  c.Get(3),
		InternalSub: c.Get(4),
	}
}

// String renders the party for logging and routing keys.
func (p Party) String() string {
	if p.Qualifier == "" {
		return p.ID
	}
	return p.ID + ":" + p.Qualifier
}

// Interchange is a decoded UNB..UNZ interchange.
type Interchange struct {
	// HadUNA reports whether an explicit service string advice was present.
	HadUNA bool
	Syntax Syntax

	Header  Segment // UNB; zero-valued when absent
	Trailer Segment // UNZ; zero-valued when absent

	// Groups is non-empty only when UNG/UNE framing was used. Messages holds
	// every message in the interchange regardless of grouping, which is what
	// routing and persistence care about.
	Groups   []Group
	Messages []Message

	// Segments is the flat decoded segment list, retained so that unknown or
	// structurally invalid content is never lost.
	Segments []Segment

	Raw         []byte
	Diagnostics []Diagnostic
}

// HasErrors reports whether any diagnostic is at Error severity.
func (ic *Interchange) HasErrors() bool {
	for _, d := range ic.Diagnostics {
		if d.Severity == Error {
			return true
		}
	}
	return false
}

func (ic *Interchange) diag(sev Severity, off, seg int, code, format string, args ...any) {
	ic.Diagnostics = append(ic.Diagnostics, Diagnostic{sev, off, seg, code, fmt.Sprintf(format, args...)})
}

// Sender returns the interchange sender (UNB S002).
func (ic *Interchange) Sender() Party { return partyFrom(ic.Header.Elem(1).First()) }

// Recipient returns the interchange recipient (UNB S003).
func (ic *Interchange) Recipient() Party { return partyFrom(ic.Header.Elem(2).First()) }

// ControlRef returns the interchange control reference (UNB 0020), the value
// the UNZ must echo and the natural idempotency key for a link.
func (ic *Interchange) ControlRef() string { return ic.Header.Value(4) }

// PreparedDate returns the raw UNB S004 date and time components. They are
// returned unparsed because the date is YYMMDD in syntax versions below 4 and
// CCYYMMDD from version 4, and senders disagree.
func (ic *Interchange) PreparedDate() (date, time string) {
	c := ic.Header.Elem(3).First()
	return c.Get(0), c.Get(1)
}

// TestIndicator reports whether UNB 0035 marks this as test traffic. A gateway
// must never apply test interchanges to production state.
func (ic *Interchange) TestIndicator() bool { return ic.Header.Value(10) == "1" }

// SyntaxIdentifier returns UNB S001 components: the character set (e.g. UNOA)
// and syntax version number.
func (ic *Interchange) SyntaxIdentifier() (charset string, version int) {
	c := ic.Header.Elem(0).First()
	v, _ := strconv.Atoi(c.Get(1))
	return c.Get(0), v
}

// ParseOptions controls interchange decoding.
type ParseOptions struct {
	Scan ScanOptions
	// Strict turns structural diagnostics at Error severity into a returned
	// error. Leave it off in a gateway: capture, diagnose and route to review
	// beats rejecting at the socket.
	Strict bool
	// SkipCharsetCheck disables validation of the text against the repertoire
	// named in UNB S001.
	SkipCharsetCheck bool
}

// Parse decodes an interchange and validates its envelope.
func Parse(raw []byte, opts ParseOptions) (*Interchange, error) {
	ic := &Interchange{Raw: append([]byte(nil), raw...)}
	ic.HadUNA = len(raw) >= 3 && strings.HasPrefix(strings.TrimLeft(string(raw), " \r\n\t"), TagUNA)

	segs, syn, diags, err := Scan(raw, opts.Scan)
	if err != nil {
		return nil, err
	}

	// The syntax version lives in UNB, which we can only read after scanning.
	declared, rescanCredible := declaredSyntaxVersion(segs)
	switch {
	case declared >= 1 && declared <= MaxSyntaxVersion:
		syn.Version = declared
	case declared != 0:
		diags = append(diags, Diagnostic{Warn, 0, -1, "bad_syntax_version",
			fmt.Sprintf("UNB declares syntax version %d, outside 1..%d; decoding as version %d",
				declared, MaxSyntaxVersion, syn.Version)})
	}

	// From version 4 the repetition separator is active by default, so a v4
	// interchange with no UNA must be rescanned or every '*' is misread as data.
	// An explicit UNA is authoritative and suppresses this.
	if opts.Scan.Syntax == nil && !ic.HadUNA && !syn.RepetitionEnabled &&
		syn.Version >= 4 && rescanCredible {
		rsyn := DefaultSyntax(syn.Version)
		rsegs, _, rdiags, rerr := Scan(raw, ScanOptions{
			Syntax:             &rsyn,
			PreserveLineBreaks: opts.Scan.PreserveLineBreaks,
			MaxSegments:        opts.Scan.MaxSegments,
		})
		// Accept the rescan only if it still reads back the version that
		// motivated it. Rescanning changes how UNB itself splits, so on a
		// malformed header the second reading can disagree with the first --
		// and then decoding is no longer a fixed point, meaning the same
		// interchange decodes differently each time it is re-encoded. When the
		// two readings disagree, the conservative first one wins.
		if rerr == nil {
			if v, ok := declaredSyntaxVersion(rsegs); ok && v == syn.Version {
				segs, syn, diags = rsegs, rsyn, rdiags
			} else {
				diags = append(diags, Diagnostic{Warn, 0, -1, "unstable_syntax_inference",
					"UNB declares syntax version 4 but re-reading with version 4 rules " +
						"does not reproduce it; decoded with version 3 rules instead"})
			}
		}
	}

	ic.Syntax = syn
	ic.Segments = segs
	ic.Diagnostics = append(ic.Diagnostics, diags...)

	ic.assemble(segs)
	ic.validateEnvelope()
	if !opts.SkipCharsetCheck {
		ic.validateCharset()
	}

	if opts.Strict && ic.HasErrors() {
		return ic, fmt.Errorf("edifact: interchange failed strict validation: %v", firstError(ic.Diagnostics))
	}
	return ic, nil
}

// declaredSyntaxVersion reads UNB S001/0002, but only when S001 as a whole
// looks like a genuine syntax identifier: a UNOx repertoire name followed by a
// single-digit version.
//
// The credibility check is load-bearing. Honouring the version means rescanning
// the input with different service characters, which changes how UNB itself
// decodes. If S001 is malformed, that second reading can produce a UNB whose
// version is no longer recoverable, and the interchange then decodes differently
// every time it is re-encoded. Refusing to infer a syntax from an incoherent
// header keeps decoding a fixed point.
func declaredSyntaxVersion(segs []Segment) (version int, credible bool) {
	for _, s := range segs {
		if s.Tag != TagUNB {
			continue
		}
		e := s.Elem(0)
		c := e.First()
		repertoire, ver := c.Get(0), c.Get(1)
		if len(repertoire) != 4 || !strings.HasPrefix(repertoire, "UNO") ||
			repertoire[3] < 'A' || repertoire[3] > 'Z' {
			return 0, false
		}
		if len(ver) != 1 || ver[0] < '0' || ver[0] > '9' {
			return 0, false
		}
		v, _ := strconv.Atoi(ver)

		// The version is worth recording whenever S001 names a repertoire and a
		// version. Driving a rescan off it is a stronger claim and needs a
		// stronger check: S001 must not repeat and must have no third
		// component, either of which means the element boundaries under the
		// current service characters are wrong -- and rescanning on a wrong
		// reading is how decoding stops being a fixed point.
		return v, len(e) == 1 && len(c) <= 2
	}
	return 0, false
}

func firstError(ds []Diagnostic) Diagnostic {
	for _, d := range ds {
		if d.Severity == Error {
			return d
		}
	}
	return Diagnostic{}
}

// assemble groups the flat segment list into the UNB/UNG/UNH hierarchy.
func (ic *Interchange) assemble(segs []Segment) {
	var (
		curMsg   *Message
		curGroup *Group
	)
	closeMessage := func(trailer Segment) {
		if curMsg == nil {
			return
		}
		curMsg.Trailer = trailer
		if curGroup != nil {
			curGroup.Messages = append(curGroup.Messages, *curMsg)
		}
		ic.Messages = append(ic.Messages, *curMsg)
		curMsg = nil
	}

	for _, s := range segs {
		switch s.Tag {
		case TagUNB:
			if ic.Header.Tag != "" {
				ic.diag(Error, s.Pos.Offset, s.Pos.Index, "duplicate_unb", "second UNB in one interchange")
			}
			ic.Header = s
		case TagUNZ:
			if curMsg != nil {
				ic.diag(Error, s.Pos.Offset, s.Pos.Index, "missing_unt",
					"message %q is still open at UNZ", curMsg.Reference())
			}
			closeMessage(Segment{})
			if curGroup != nil {
				ic.diag(Error, s.Pos.Offset, s.Pos.Index, "unclosed_ung", "UNZ reached with functional group still open")
				ic.Groups = append(ic.Groups, *curGroup)
				curGroup = nil
			}
			ic.Trailer = s
		case TagUNG:
			if curGroup != nil {
				ic.diag(Error, s.Pos.Offset, s.Pos.Index, "nested_ung", "UNG inside an open functional group")
				ic.Groups = append(ic.Groups, *curGroup)
			}
			curGroup = &Group{Header: s}
		case TagUNE:
			if curMsg != nil {
				ic.diag(Error, s.Pos.Offset, s.Pos.Index, "missing_unt",
					"message %q is still open at UNE", curMsg.Reference())
			}
			closeMessage(Segment{})
			if curGroup == nil {
				ic.diag(Error, s.Pos.Offset, s.Pos.Index, "orphan_une", "UNE without a matching UNG")
				continue
			}
			curGroup.Trailer = s
			ic.Groups = append(ic.Groups, *curGroup)
			curGroup = nil
		case TagUNH:
			if curMsg != nil {
				ic.diag(Error, s.Pos.Offset, s.Pos.Index, "unclosed_unh",
					"UNH for message %q while message %q is still open", s.Value(0), curMsg.Reference())
				closeMessage(Segment{})
			}
			curMsg = &Message{Header: s}
		case TagUNT:
			if curMsg == nil {
				ic.diag(Error, s.Pos.Offset, s.Pos.Index, "orphan_unt", "UNT without a matching UNH")
				continue
			}
			closeMessage(s)
		default:
			if curMsg == nil {
				ic.diag(Error, s.Pos.Offset, s.Pos.Index, "segment_outside_message",
					"segment %q appears outside any UNH..UNT message", s.Tag)
				continue
			}
			curMsg.Segments = append(curMsg.Segments, s)
		}
	}
	if curMsg != nil {
		ic.diag(Error, curMsg.Header.Pos.Offset, curMsg.Header.Pos.Index, "missing_unt",
			"message %q has no UNT trailer", curMsg.Reference())
		closeMessage(Segment{})
	}
	if curGroup != nil {
		ic.diag(Error, curGroup.Header.Pos.Offset, curGroup.Header.Pos.Index, "missing_une",
			"functional group has no UNE trailer")
		ic.Groups = append(ic.Groups, *curGroup)
	}
}

// validateEnvelope checks the counts and control references that ISO 9735
// requires. These are the checks that catch a truncated or spliced interchange,
// which is exactly the failure a store-and-forward link produces.
func (ic *Interchange) validateEnvelope() {
	if ic.Header.Tag == "" {
		ic.diag(Error, 0, -1, "missing_unb", "interchange has no UNB header")
	}
	if ic.Trailer.Tag == "" {
		ic.diag(Error, 0, -1, "missing_unz", "interchange has no UNZ trailer")
	}

	if ic.Header.Tag != "" && ic.Trailer.Tag != "" {
		if got, want := ic.Trailer.Value(1), ic.ControlRef(); got != want {
			ic.diag(Error, ic.Trailer.Pos.Offset, ic.Trailer.Pos.Index, "control_ref_mismatch",
				"UNZ control reference %q does not match UNB %q", got, want)
		}
		// UNZ 0036 counts functional groups when groups are used, messages
		// otherwise.
		want := len(ic.Messages)
		what := "messages"
		if len(ic.Groups) > 0 {
			want = len(ic.Groups)
			what = "functional groups"
		}
		if n, err := strconv.Atoi(strings.TrimSpace(ic.Trailer.Value(0))); err != nil {
			ic.diag(Error, ic.Trailer.Pos.Offset, ic.Trailer.Pos.Index, "bad_unz_count",
				"UNZ count %q is not a number", ic.Trailer.Value(0))
		} else if n != want {
			ic.diag(Error, ic.Trailer.Pos.Offset, ic.Trailer.Pos.Index, "unz_count_mismatch",
				"UNZ declares %d %s, interchange contains %d", n, what, want)
		}
	}

	for _, g := range ic.Groups {
		if g.Trailer.Tag == "" {
			continue
		}
		if got, want := g.Trailer.Value(1), g.Header.Value(4); got != want {
			ic.diag(Error, g.Trailer.Pos.Offset, g.Trailer.Pos.Index, "group_ref_mismatch",
				"UNE reference %q does not match UNG %q", got, want)
		}
		if n, err := strconv.Atoi(strings.TrimSpace(g.Trailer.Value(0))); err == nil && n != len(g.Messages) {
			ic.diag(Error, g.Trailer.Pos.Offset, g.Trailer.Pos.Index, "une_count_mismatch",
				"UNE declares %d messages, group contains %d", n, len(g.Messages))
		}
	}

	for _, m := range ic.Messages {
		if m.Trailer.Tag == "" {
			continue
		}
		if got, want := m.Trailer.Value(1), m.Reference(); got != want {
			ic.diag(Error, m.Trailer.Pos.Offset, m.Trailer.Pos.Index, "message_ref_mismatch",
				"UNT reference %q does not match UNH %q", got, want)
		}
		// UNT 0074 counts every segment in the message including UNH and UNT.
		want := len(m.Segments) + 2
		if n, err := strconv.Atoi(strings.TrimSpace(m.Trailer.Value(0))); err != nil {
			ic.diag(Error, m.Trailer.Pos.Offset, m.Trailer.Pos.Index, "bad_unt_count",
				"UNT segment count %q is not a number", m.Trailer.Value(0))
		} else if n != want {
			ic.diag(Error, m.Trailer.Pos.Offset, m.Trailer.Pos.Index, "unt_count_mismatch",
				"UNT declares %d segments, message %q contains %d", n, m.Reference(), want)
		}
	}
}

func (ic *Interchange) validateCharset() {
	name, _ := ic.SyntaxIdentifier()
	cs := CharsetByName(name)
	if cs == nil {
		if name != "" {
			ic.diag(Info, ic.Header.Pos.Offset, ic.Header.Pos.Index, "unknown_charset",
				"syntax identifier %q is not a repertoire this build validates", name)
		}
		return
	}
	for _, s := range ic.Segments {
		if s.Tag == TagUNB || s.Tag == TagUNA {
			continue // UNB itself names the repertoire and may precede it
		}
		for _, e := range s.Elements {
			for _, c := range e {
				for _, v := range c {
					if r, ok := cs.FirstInvalid(v); !ok {
						ic.diag(Warn, s.Pos.Offset, s.Pos.Index, "charset_violation",
							"segment %s contains %q which is outside repertoire %s", s.Tag, string(r), name)
					}
				}
			}
		}
	}
}
