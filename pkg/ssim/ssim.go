// Package ssim implements the IATA schedule messages carried over Type B:
// SSM, the Standard Schedules Message, and ASM, the Ad hoc Schedule Message.
//
// # What this is, and what it is not
//
// SSM and ASM are defined in IATA's Standard Schedules Information Manual,
// which is a paid publication and was not bought. What is public, and what this
// package is built from, is the vocabulary and the shape: the action
// identifiers each message type uses, and the fact that a message is a message
// identifier, a time mode, an action, and then flight, period and leg
// information in that order.
//
// The exact field layout within those lines is inferred. So this is a profile,
// in the same sense as pkg/airimp: an ordered set of recognizers that a
// deployment extends per link, not a claim of conformance. Anything a profile
// does not recognise is kept verbatim as a fragment rather than dropped, which
// is what makes a dialect gap visible as data instead of as silence.
//
// # Why a gateway cares
//
// A schedule change is not interesting in itself; it is interesting when it
// touches a flight somebody is holding. Matching a change against held records
// is the point, and it is what turns an SSM into work on a queue rather than a
// message in a log.
package ssim

import (
	"fmt"
	"strings"
)

// Kind distinguishes the two message types.
type Kind string

const (
	// KindSSM is the Standard Schedules Message: changes to a repeating
	// schedule over a period.
	KindSSM Kind = "SSM"
	// KindASM is the Ad hoc Schedule Message: a deviation affecting single
	// flights, which is what most operational traffic is.
	KindASM Kind = "ASM"
)

// Action is the action identifier on the third line of a schedule message.
type Action string

// Action identifiers. The SSM and ASM sets overlap but are not identical, and
// the difference is real: RIN and RRT are ad hoc concepts, SKD and REV are
// period concepts.
const (
	ActionNew       Action = "NEW" // insert new flight information
	ActionCancel    Action = "CNL" // cancel a flight or a period
	ActionReplace   Action = "RPL" // replace existing flight information
	ActionTime      Action = "TIM" // times only
	ActionEquipment Action = "EQT" // aircraft or equipment change
	ActionAdmin     Action = "ADM" // administrative data only
	ActionConfig    Action = "CON" // configuration or version change
	ActionFlightNum Action = "FLT" // flight designator change
	ActionRevise    Action = "REV" // revise a period
	ActionSchedule  Action = "SKD" // schedule update
	ActionReinstate Action = "RIN" // reinstate a cancelled flight
	ActionReroute   Action = "RRT" // reroute
	ActionRestore   Action = "RST" // restore a period
)

// ssmActions and asmActions are the sets each message type uses, as documented
// publicly by operators of schedule feeds.
var (
	ssmActions = map[Action]bool{
		ActionNew: true, ActionCancel: true, ActionReplace: true, ActionSchedule: true,
		ActionAdmin: true, ActionConfig: true, ActionEquipment: true, ActionFlightNum: true,
		ActionRevise: true, ActionRestore: true, ActionTime: true,
	}
	asmActions = map[Action]bool{
		ActionNew: true, ActionCancel: true, ActionReinstate: true, ActionReplace: true,
		ActionAdmin: true, ActionConfig: true, ActionEquipment: true, ActionFlightNum: true,
		ActionReroute: true, ActionTime: true,
	}
)

// ActionMeaning explains an action identifier.
var ActionMeaning = map[Action]string{
	ActionNew:       "insertion of new flight information",
	ActionCancel:    "cancellation",
	ActionReplace:   "replacement of existing flight information",
	ActionTime:      "change of times",
	ActionEquipment: "change of aircraft or equipment",
	ActionAdmin:     "change of administrative data",
	ActionConfig:    "change of configuration or version",
	ActionFlightNum: "change of flight designator",
	ActionRevise:    "revision of a period",
	ActionSchedule:  "schedule update",
	ActionReinstate: "reinstatement of a cancelled flight",
	ActionReroute:   "reroute",
	ActionRestore:   "restoration of a period",
}

// Meaning returns the identifier's meaning, or "" when it is not one this
// package knows.
func (a Action) Meaning() string { return ActionMeaning[a] }

// Cancels reports whether the action removes a flight rather than changing one.
func (a Action) Cancels() bool { return a == ActionCancel }

// ValidFor reports whether the action belongs to the given message type.
func (a Action) ValidFor(k Kind) bool {
	switch k {
	case KindSSM:
		return ssmActions[a]
	case KindASM:
		return asmActions[a]
	}
	return false
}

// TimeMode says which clock the times in the message are on. Getting this wrong
// moves every flight in the message by the station's offset.
type TimeMode string

const (
	// UTC is the mode nearly all interline schedule traffic uses.
	UTC TimeMode = "UTC"
	// LocalTime is permitted and rare, and is why the mode is stated at all.
	LocalTime TimeMode = "LT"
)

// Flight identifies the flight a message is about.
type Flight struct {
	Carrier string // two-character designator, e.g. "BA"
	Number  string // flight number as written, without leading-zero normalisation
	Suffix  string // operational suffix, when one is present
}

// Key returns a stable identifier for matching a schedule change against a
// held segment. Flight numbers are compared without their leading zeros
// because carriers write the same flight both ways.
func (f Flight) Key() string {
	return f.Carrier + strings.TrimLeft(f.Number, "0")
}

func (f Flight) String() string {
	s := f.Carrier + f.Number
	if f.Suffix != "" {
		s += f.Suffix
	}
	return s
}

// Period is the date range and day pattern a schedule line applies to. For an
// ASM the range is usually a single date, and From equals To.
type Period struct {
	From string // DDMMM or DDMMMYY as written
	To   string
	// Days is the frequency pattern, digits 1 (Monday) to 7 (Sunday). Empty
	// means the message did not state one, which for an ad hoc message is
	// normal.
	Days string
}

// Single reports whether the period covers one date.
func (p Period) Single() bool { return p.To == "" || p.To == p.From }

// Leg is one board-to-off pair within the flight.
type Leg struct {
	Board string // departure station
	Off   string // arrival station
	// Depart and Arrive are HHMM in the message's time mode. Arrive may carry a
	// day offset suffix, which is kept as written.
	Depart string
	Arrive string
	// Equipment is the aircraft type, when the line carried one.
	Equipment string
}

func (l Leg) String() string {
	s := l.Board + "-" + l.Off
	if l.Depart != "" || l.Arrive != "" {
		s += " " + l.Depart + "/" + l.Arrive
	}
	if l.Equipment != "" {
		s += " " + l.Equipment
	}
	return s
}

// Severity classifies a parse Diagnostic.
type Severity int

const (
	Info Severity = iota
	Warn
	Error
)

func (s Severity) String() string {
	switch s {
	case Warn:
		return "warn"
	case Error:
		return "error"
	}
	return "info"
}

// Diagnostic is a non-fatal observation made while parsing.
type Diagnostic struct {
	Severity Severity
	Line     int
	Code     string
	Detail   string
}

func (d Diagnostic) String() string {
	return fmt.Sprintf("%s:%d:%s: %s", d.Severity, d.Line, d.Code, d.Detail)
}

// Message is a parsed schedule message.
type Message struct {
	Kind     Kind
	TimeMode TimeMode
	Action   Action
	Flight   Flight
	Period   Period
	Legs     []Leg
	// Equipment is aircraft information stated for the message rather than for
	// a particular leg.
	Equipment string
	// Fragments holds every line no recognizer claimed, in order and verbatim.
	// A dialect gap must be visible as data, not lost.
	Fragments []string

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
	m.Diagnostics = append(m.Diagnostics, Diagnostic{sev, line, code, fmt.Sprintf(format, args...)})
}

// Describe renders the message as one line of operator-facing text.
func (m *Message) Describe() string {
	s := string(m.Kind) + " " + string(m.Action) + " " + m.Flight.String()
	if m.Period.From != "" {
		s += " " + m.Period.From
		if !m.Period.Single() {
			s += "-" + m.Period.To
		}
	}
	if len(m.Legs) > 0 {
		parts := make([]string, len(m.Legs))
		for i, l := range m.Legs {
			parts[i] = l.String()
		}
		s += " " + strings.Join(parts, " ")
	}
	return s
}
