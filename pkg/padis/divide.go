package padis

import (
	"fmt"
	"strconv"

	"github.com/adamf/jetway/pkg/edifact"
	"github.com/adamf/jetway/pkg/pnr"
)

// Divide advisories: telling a partner that one booking has become two.
//
// # What this is built from
//
// The interline divide procedure lives in AIRIMP, which is paid and was not
// bought. What is public, and what this uses, is how PADIS represents a split
// record: IATA's PNRGOV implementation guide is free and documents the group
// carrying it as an EQN giving the number of passengers split from or to a
// record, alongside the RCI segments naming the records involved. The RCI
// composite is specified there too.
//
// So the segment vocabulary here is grounded and the message shape is a
// profile, which is the same standing as the reservation messages beside it.
// One field is deliberately left empty: RCI's reservation control type (9958)
// takes a code from the PADIS codeset directory, which is paid, and guessing
// which value means "the other half of a division" would be worse than
// omitting a conditional element.
const (
	// TagEQN carries a number of units -- here, passengers moved by a split.
	TagEQN = "EQN"
)

// SplitRef names one record involved in a division.
type SplitRef struct {
	// Owner is the airline or system code that issued the locator.
	Owner   string
	Locator string
}

// Divide is a decoded advisory that a booking was divided.
type Divide struct {
	// Party is who divided it.
	Party string
	// Locator is the sender's own record, the one the advisory is about.
	Locator string
	// Split names the other half or halves.
	Split []SplitRef
	// Passengers is how many moved.
	Passengers int
}

// Describe renders the advisory as one line of operator-facing text.
func (d *Divide) Describe() string {
	s := fmt.Sprintf("divide: %d passenger(s) split from %s", d.Passengers, d.Locator)
	for _, r := range d.Split {
		s += " to " + r.Owner + "/" + r.Locator
	}
	return s
}

// BuildDivide renders an advisory that passengers have moved to another record.
//
// It carries both locators because that is the whole content of the message:
// the partner holds one record covering everybody, and this says which
// passengers are now somewhere else and where that is.
func BuildDivide(parent, child *pnr.PNR, carrier string, o BuildOptions) (*edifact.Interchange, error) {
	if parent == nil || child == nil {
		return nil, fmt.Errorf("padis: a divide advisory needs both records")
	}
	if len(child.Passengers) == 0 {
		return nil, fmt.Errorf("padis: %s moved no passengers", child.RecordLocator)
	}
	body := []edifact.Segment{
		edifact.Seg("MSG", edifact.Comp("", "11")),
		edifact.Seg("ORG", edifact.Comp(o.Sender.ID, parent.Origin.Agent)),
		edifact.Seg("RCI", edifact.Comp(o.Sender.ID, parent.RecordLocator, "")),
	}
	// The carrier's own reference for the booking, so they can find it.
	for _, l := range parent.Locators {
		if l.Owner == carrier && l.Value != "" {
			body = append(body, edifact.Seg("RCI", edifact.Comp(l.Owner, l.Value, "")))
		}
	}

	// The split group: how many moved, and where they moved to.
	body = append(body,
		edifact.Seg(TagEQN, edifact.Simple(strconv.Itoa(len(child.Passengers)))),
		edifact.Seg("RCI", edifact.Comp(o.Sender.ID, child.RecordLocator, "")),
	)

	// Who moved, and what they still hold with this carrier.
	body = append(body, tifSegments(child)...)
	for _, s := range child.Segments {
		if s.Carrier != carrier || s.Type != pnr.SegmentAir || s.Status == "XX" {
			continue
		}
		body = append(body, tvlSegment(s),
			edifact.Seg("RPI", edifact.Simple(strconv.Itoa(s.Seats)), edifact.Simple(s.Status)))
	}

	ic := newInterchange(MsgPAOREQ, o)
	ic.AddMessage(o.msgRef(), messageID(MsgPAOREQ), body...)
	ic.Finalize()
	return ic, nil
}

// IsDivide reports whether a message advises a division.
//
// The signal is the EQN segment. The guide places it in the group carrying the
// split record locators and nowhere else in a reservation message, so its
// presence beside more than one RCI is what distinguishes this from an ordinary
// request.
func IsDivide(m edifact.Message) bool {
	if _, ok := m.First(TagEQN); !ok {
		return false
	}
	return len(m.Find("RCI")) >= 2
}

// ParseDivide decodes a divide advisory.
func ParseDivide(m edifact.Message) (*Divide, error) {
	if !IsDivide(m) {
		return nil, fmt.Errorf("padis: message does not advise a division")
	}
	eqn, _ := m.First(TagEQN)
	n, err := strconv.Atoi(eqn.Value(0))
	if err != nil || n <= 0 {
		return nil, fmt.Errorf("padis: divide advisory has an unreadable passenger count %q", eqn.Value(0))
	}

	d := &Divide{Passengers: n}
	if org, ok := m.First("ORG"); ok {
		d.Party = org.Get(0, 0)
	}

	// RCI segments before the EQN describe the record being divided; those
	// after it name what the passengers moved to. Walking in order is the only
	// way to tell them apart, because the segment itself does not say.
	seenEQN := false
	for _, seg := range m.Segments {
		switch seg.Tag {
		case TagEQN:
			seenEQN = true
		case "RCI":
			for i := range seg.Elements {
				c := seg.Elem(i).First()
				owner, loc := c.Get(0), c.Get(1)
				if loc == "" {
					continue
				}
				if seenEQN {
					d.Split = append(d.Split, SplitRef{Owner: owner, Locator: loc})
				} else if d.Locator == "" {
					d.Locator = loc
				}
			}
		}
	}
	if d.Locator == "" || len(d.Split) == 0 {
		return nil, fmt.Errorf("padis: divide advisory names fewer than two records")
	}
	return d, nil
}
