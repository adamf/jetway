package edifact

import (
	"fmt"
	"strconv"
)

// CONTRL is the UN/EDIFACT syntax and service report message: the receipt a
// partner is owed for an interchange, and the only standard way to tell them
// their syntax was wrong.
//
// Everything here follows the UNSM definition of CONTRL (version D, release 3,
// controlling agency UN), which the UN publishes and which is quoted for the
// code lists below. That matters: this is the rare layer where conformance can
// be checked rather than inferred.
//
// The message reports on a *subject interchange* at up to four nested levels --
// interchange (UCI), functional group (UCF), message (UCM), segment (UCS) and
// data element (UCD) -- and each reporting level carries an action code saying
// what was done with it.
const (
	// TagUCI identifies the subject interchange and the action taken on it.
	TagUCI = "UCI"
	// TagUCF identifies a functional group within the subject interchange.
	TagUCF = "UCF"
	// TagUCM identifies a message within the subject interchange.
	TagUCM = "UCM"
	// TagUCS identifies a segment in error, by position.
	TagUCS = "UCS"
	// TagUCD identifies a data element in error, within a UCS.
	TagUCD = "UCD"

	// MsgCONTRL is the CONTRL message type.
	MsgCONTRL = "CONTRL"
)

// Action is data element 0083, the action taken on a reporting level.
//
// The code list is short by design: CONTRL says only received, acknowledged or
// rejected, and everything else is expressed by which levels are reported.
type Action string

// Action codes from data element 0083.
const (
	// ActionRejected (4) rejects this level and every level below it.
	ActionRejected Action = "4"
	// ActionAcknowledged (7) acknowledges this level, and every level below it
	// that is not explicitly rejected elsewhere in the same CONTRL.
	ActionAcknowledged Action = "7"
	// ActionReceived (8) reports receipt of the interchange only. It says
	// nothing about syntax, and is what an immediate receipt looks like when
	// the syntax check has not happened yet.
	ActionReceived Action = "8"
)

// ActionMeaning explains an action code.
var ActionMeaning = map[Action]string{
	ActionRejected:     "this level and all lower levels rejected",
	ActionAcknowledged: "this level acknowledged, next lower level acknowledged if not explicitly rejected",
	ActionReceived:     "interchange received",
}

// Meaning returns the code's meaning, or "" when it is not in the standard list.
func (a Action) Meaning() string { return ActionMeaning[a] }

// Rejects reports whether the action rejects what it refers to.
func (a Action) Rejects() bool { return a == ActionRejected }

// SyntaxError is data element 0085, the nature of a detected syntax error.
type SyntaxError string

// Syntax error codes from data element 0085.
const (
	SyntaxVersionNotSupported SyntaxError = "2"
	RecipientNotActual        SyntaxError = "7"
	InvalidValue              SyntaxError = "12"
	MissingRequired           SyntaxError = "13"
	ValueNotSupportedHere     SyntaxError = "14"
	NotSupportedHere          SyntaxError = "15"
	TooManyConstituents       SyntaxError = "16"
	NoAgreement               SyntaxError = "17"
	UnspecifiedError          SyntaxError = "18"
	InvalidDecimalNotation    SyntaxError = "19"
	InvalidServiceCharAdvice  SyntaxError = "20"
	InvalidCharacters         SyntaxError = "21"
	InvalidServiceCharacters  SyntaxError = "22"
	UnknownSender             SyntaxError = "23"
	TooOld                    SyntaxError = "24"
	TestIndicatorNotSupported SyntaxError = "25"
	DuplicateDetected         SyntaxError = "26"
	SecurityNotSupported      SyntaxError = "27"
	ReferencesDoNotMatch      SyntaxError = "28"
	ControlCountMismatch      SyntaxError = "29"
	GroupsAndMessagesMixed    SyntaxError = "30"
	MultipleMessageTypes      SyntaxError = "31"
	LowerLevelEmpty           SyntaxError = "32"
	InvalidOccurrence         SyntaxError = "33"
	NestingNotAllowed         SyntaxError = "34"
	TooManySegmentRepeats     SyntaxError = "35"
	TooManyGroupRepeats       SyntaxError = "36"
	InvalidCharacterType      SyntaxError = "37"
	MissingDigitBeforeDecimal SyntaxError = "38"
	DataElementTooLong        SyntaxError = "39"
)

// SyntaxErrorMeaning explains a syntax error code.
var SyntaxErrorMeaning = map[SyntaxError]string{
	SyntaxVersionNotSupported: "syntax version or level not supported",
	RecipientNotActual:        "interchange recipient is not the actual recipient",
	InvalidValue:              "invalid value",
	MissingRequired:           "missing",
	ValueNotSupportedHere:     "value not supported in this position",
	NotSupportedHere:          "not supported in this position",
	TooManyConstituents:       "too many constituents",
	NoAgreement:               "no agreement",
	UnspecifiedError:          "unspecified error",
	InvalidDecimalNotation:    "invalid decimal notation",
	InvalidServiceCharAdvice:  "character invalid as service character",
	InvalidCharacters:         "invalid character(s)",
	InvalidServiceCharacters:  "invalid service character(s)",
	UnknownSender:             "unknown interchange sender",
	TooOld:                    "too old",
	TestIndicatorNotSupported: "test indicator not supported",
	DuplicateDetected:         "duplicate detected",
	SecurityNotSupported:      "security function not supported",
	ReferencesDoNotMatch:      "references do not match",
	ControlCountMismatch:      "control count does not match number of instances received",
	GroupsAndMessagesMixed:    "functional groups and messages mixed",
	MultipleMessageTypes:      "more than one message type in group",
	LowerLevelEmpty:           "lower level empty",
	InvalidOccurrence:         "invalid occurrence outside message or functional group",
	NestingNotAllowed:         "nesting indicator not allowed",
	TooManySegmentRepeats:     "too many segment repetitions",
	TooManyGroupRepeats:       "too many segment group repetitions",
	InvalidCharacterType:      "invalid type of character(s)",
	MissingDigitBeforeDecimal: "missing digit in front of decimal sign",
	DataElementTooLong:        "data element too long",
}

// Meaning returns the code's meaning, or "" when it is not in the standard list.
func (e SyntaxError) Meaning() string { return SyntaxErrorMeaning[e] }

// diagToSyntaxError maps this package's own diagnostic codes onto the standard
// 0085 vocabulary.
//
// Only mappings that are actually faithful are listed. Anything else becomes
// "unspecified error", which the standard provides for exactly this: reporting
// that something is wrong without claiming to know which of its categories it
// falls into.
var diagToSyntaxError = map[string]SyntaxError{
	"missing_unb":             MissingRequired,
	"missing_unz":             MissingRequired,
	"missing_une":             MissingRequired,
	"missing_unt":             MissingRequired,
	"unclosed_ung":            MissingRequired,
	"unclosed_unh":            MissingRequired,
	"control_ref_mismatch":    ReferencesDoNotMatch,
	"group_ref_mismatch":      ReferencesDoNotMatch,
	"message_ref_mismatch":    ReferencesDoNotMatch,
	"unz_count_mismatch":      ControlCountMismatch,
	"une_count_mismatch":      ControlCountMismatch,
	"unt_count_mismatch":      ControlCountMismatch,
	"bad_unz_count":           InvalidValue,
	"bad_unt_count":           InvalidValue,
	"bad_syntax_version":      InvalidValue,
	"unknown_charset":         InvalidValue,
	"charset_violation":       InvalidCharacters,
	"trailing_release":        InvalidServiceCharacters,
	"duplicate_unb":           InvalidOccurrence,
	"nested_ung":              InvalidOccurrence,
	"orphan_une":              InvalidOccurrence,
	"orphan_unt":              InvalidOccurrence,
	"segment_outside_message": InvalidOccurrence,
}

// serviceSegmentTag maps a diagnostic to the service segment it concerns, for
// data element 0013.
var diagServiceTag = map[string]string{
	"missing_unb":          TagUNB,
	"missing_unz":          TagUNZ,
	"missing_une":          TagUNE,
	"missing_unt":          TagUNT,
	"unclosed_ung":         TagUNE,
	"unclosed_unh":         TagUNT,
	"control_ref_mismatch": TagUNZ,
	"unz_count_mismatch":   TagUNZ,
	"bad_unz_count":        TagUNZ,
	"duplicate_unb":        TagUNB,
	"nested_ung":           TagUNG,
	"orphan_une":           TagUNE,
	"orphan_unt":           TagUNT,
}

// ElementReport identifies one erroneous data element within a segment.
type ElementReport struct {
	Error SyntaxError
	// Position is the 1-based data element position in the segment (0098).
	Position int
	// Component is the 1-based component position within a composite (0104).
	// Zero means the error is at the data element rather than a component.
	Component int
}

// SegmentReport identifies one erroneous segment within a message.
type SegmentReport struct {
	// Position is the 1-based segment position in the message (0096).
	Position int
	Error    SyntaxError
	Elements []ElementReport
}

// MessageReport is the action taken on one message of the subject interchange.
type MessageReport struct {
	// Reference is the subject message's UNH 0062.
	Reference string
	ID        MessageID
	Action    Action
	Error     SyntaxError
	// ServiceTag names the service segment at fault (0013), when one is.
	ServiceTag string
	Element    ElementReport
	Segments   []SegmentReport
}

// Report is what a CONTRL says about one subject interchange.
type Report struct {
	// ControlRef is the subject interchange's UNB 0020, which is what ties the
	// report to the thing it reports on.
	ControlRef string
	Sender     Party
	Recipient  Party
	Action     Action
	Error      SyntaxError
	ServiceTag string
	Element    ElementReport
	Messages   []MessageReport
}

// Rejected reports whether the subject interchange was refused outright.
func (r *Report) Rejected() bool { return r.Action.Rejects() }

// Check derives a report from an interchange this node has just decoded.
//
// The action is acknowledgement when nothing failed and rejection when
// something did. Rejection is at interchange level because the diagnostics this
// package raises are envelope-level: a broken UNZ count is not a property of
// any one message.
func Check(ic *Interchange) *Report {
	r := &Report{
		ControlRef: ic.ControlRef(),
		Sender:     ic.Sender(),
		Recipient:  ic.Recipient(),
		Action:     ActionAcknowledged,
	}
	for _, d := range ic.Diagnostics {
		if d.Severity != Error {
			continue
		}
		r.Action = ActionRejected
		// The first error is the one reported: each reporting level in CONTRL
		// carries exactly one 0085, and the standard is explicit about that.
		if r.Error == "" {
			code, ok := diagToSyntaxError[d.Code]
			if !ok {
				code = UnspecifiedError
			}
			r.Error = code
			r.ServiceTag = diagServiceTag[d.Code]
		}
	}
	if r.Action == ActionAcknowledged {
		for _, m := range ic.Messages {
			r.Messages = append(r.Messages, MessageReport{
				Reference: m.Reference(), ID: m.ID(), Action: ActionAcknowledged,
			})
		}
	}
	return r
}

// Receipt derives a receipt-only report: the interchange arrived, and nothing
// is being said about its syntax.
func Receipt(ic *Interchange) *Report {
	return &Report{
		ControlRef: ic.ControlRef(),
		Sender:     ic.Sender(),
		Recipient:  ic.Recipient(),
		Action:     ActionReceived,
	}
}

// CONTRLOptions controls how a report is rendered as an interchange.
type CONTRLOptions struct {
	// Sender and Recipient are this node and the partner: a CONTRL travels the
	// opposite way to the interchange it reports on, so these are the reverse
	// of the subject's.
	Sender    Party
	Recipient Party

	ControlRef string // the CONTRL interchange's own UNB 0020
	MessageRef string // UNH 0062; defaults to "1"
	Date       string // UNB S004 date
	Time       string // UNB S004 time

	// SyntaxVersion of the CONTRL interchange. Zero follows the subject.
	SyntaxVersion int
	CharsetID     string

	// Version and Release of the CONTRL message type. Empty derives them from
	// the syntax version, which is what the standard asks for: the CONTRL
	// follows the ISO 9735 version of the interchange carrying it.
	Version string
	Release string
}

func (o CONTRLOptions) messageID(syntaxVersion int) MessageID {
	v, rel := o.Version, o.Release
	if v == "" {
		v = "D"
	}
	if rel == "" {
		rel = "3"
		if syntaxVersion >= 4 {
			rel = "4"
		}
	}
	return MessageID{Type: MsgCONTRL, Version: v, Release: rel, ControllingAgency: "UN"}
}

// Build renders the report as a CONTRL interchange, ready to encode.
func (r *Report) Build(o CONTRLOptions) (*Interchange, error) {
	if r.ControlRef == "" {
		return nil, fmt.Errorf("edifact: CONTRL needs the subject interchange control reference")
	}
	if r.Action == "" {
		return nil, fmt.Errorf("edifact: CONTRL needs an action code")
	}
	v := o.SyntaxVersion
	if v == 0 {
		v = 3
	}
	ref := o.ControlRef
	if ref == "" {
		ref = r.ControlRef
	}
	msgRef := o.MessageRef
	if msgRef == "" {
		msgRef = "1"
	}

	ic := NewInterchange(UNBParams{
		CharsetID: o.CharsetID, SyntaxVersion: v,
		Sender: o.Sender, Recipient: o.Recipient,
		Date: o.Date, Time: o.Time, ControlRef: ref,
	})

	body := []Segment{uciSegment(r)}
	for _, m := range r.Messages {
		body = append(body, ucmSegment(m))
		for _, s := range m.Segments {
			body = append(body, Seg(TagUCS, simple(strconv.Itoa(s.Position)), simple(string(s.Error))))
			for _, e := range s.Elements {
				body = append(body, Seg(TagUCD, simple(string(e.Error)), elementIDComposite(e)))
			}
		}
	}
	ic.AddMessage(msgRef, o.messageID(v), body...)
	ic.Finalize()
	return ic, nil
}

func uciSegment(r *Report) Segment {
	return Seg(TagUCI,
		simple(r.ControlRef),
		Comp(r.Sender.ID, r.Sender.Qualifier, r.Sender.RoutingAddr),
		Comp(r.Recipient.ID, r.Recipient.Qualifier, r.Recipient.RoutingAddr),
		simple(string(r.Action)),
		simple(string(r.Error)),
		simple(r.ServiceTag),
		elementIDComposite(r.Element),
	)
}

func ucmSegment(m MessageReport) Segment {
	return Seg(TagUCM,
		simple(m.Reference),
		Comp(m.ID.Type, m.ID.Version, m.ID.Release, m.ID.ControllingAgency, m.ID.AssociationCode),
		simple(string(m.Action)),
		simple(string(m.Error)),
		simple(m.ServiceTag),
		elementIDComposite(m.Element),
	)
}

// elementIDComposite renders S011. A zero position means the composite is
// absent, which the encoder drops along with any empty tail.
func elementIDComposite(e ElementReport) Element {
	if e.Position == 0 {
		return Comp("")
	}
	comp := ""
	if e.Component > 0 {
		comp = strconv.Itoa(e.Component)
	}
	return Comp(strconv.Itoa(e.Position), comp)
}

// IsCONTRL reports whether a decoded message is a syntax and service report.
func IsCONTRL(m Message) bool { return m.ID().Type == MsgCONTRL }

// ParseCONTRL reads a CONTRL message into a report.
//
// This is the half that matters for an outbound link: it says whether the
// partner accepted what was sent, and if not, which part of it they refused.
func ParseCONTRL(m Message) (*Report, error) {
	if !IsCONTRL(m) {
		return nil, fmt.Errorf("edifact: message %s is not a CONTRL", m.ID())
	}
	uci, ok := m.First(TagUCI)
	if !ok {
		return nil, fmt.Errorf("edifact: CONTRL has no UCI segment")
	}
	r := &Report{
		ControlRef: uci.Value(0),
		Sender:     partyFrom(uci.Elem(1).First()),
		Recipient:  partyFrom(uci.Elem(2).First()),
		Action:     Action(uci.Value(3)),
		Error:      SyntaxError(uci.Value(4)),
		ServiceTag: uci.Value(5),
		Element:    elementReportFrom("", uci.Elem(6).First()),
	}

	// UCS and UCD belong to the UCM that precedes them, and UCD to the UCS
	// before it, so the flat segment list is walked in order rather than
	// gathered by tag.
	for _, s := range m.Segments {
		switch s.Tag {
		case TagUCM:
			r.Messages = append(r.Messages, MessageReport{
				Reference: s.Value(0),
				ID: MessageID{
					Type: s.Get(1, 0), Version: s.Get(1, 1), Release: s.Get(1, 2),
					ControllingAgency: s.Get(1, 3), AssociationCode: s.Get(1, 4),
				},
				Action:     Action(s.Value(2)),
				Error:      SyntaxError(s.Value(3)),
				ServiceTag: s.Value(4),
				Element:    elementReportFrom("", s.Elem(5).First()),
			})
		case TagUCS:
			if len(r.Messages) == 0 {
				continue
			}
			pos, _ := strconv.Atoi(s.Value(0))
			last := &r.Messages[len(r.Messages)-1]
			last.Segments = append(last.Segments, SegmentReport{
				Position: pos, Error: SyntaxError(s.Value(1)),
			})
		case TagUCD:
			if len(r.Messages) == 0 {
				continue
			}
			last := &r.Messages[len(r.Messages)-1]
			if len(last.Segments) == 0 {
				continue
			}
			seg := &last.Segments[len(last.Segments)-1]
			seg.Elements = append(seg.Elements, elementReportFrom(s.Value(0), s.Elem(1).First()))
		}
	}
	return r, nil
}

func elementReportFrom(code string, c Composite) ElementReport {
	pos, _ := strconv.Atoi(c.Get(0))
	comp, _ := strconv.Atoi(c.Get(1))
	return ElementReport{Error: SyntaxError(code), Position: pos, Component: comp}
}

// Describe renders a report as one line of operator-facing text.
func (r *Report) Describe() string {
	s := "interchange " + r.ControlRef + ": " + string(r.Action)
	if m := r.Action.Meaning(); m != "" {
		s += " (" + m + ")"
	}
	if r.Error != "" {
		s += "; error " + string(r.Error)
		if m := r.Error.Meaning(); m != "" {
			s += " (" + m + ")"
		}
		if r.ServiceTag != "" {
			s += " in " + r.ServiceTag
		}
	}
	return s
}
