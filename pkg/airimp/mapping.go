package airimp

import (
	"fmt"
	"strings"
	"time"

	"github.com/adamf/jetway/pkg/pnr"
)

// Change describes one effect an applied message had on a record. The gateway
// stores these as the event trail, which is what makes "what did message X do
// to this booking" answerable months later.
type Change struct {
	Op     string `json:"op"`
	Detail string `json:"detail"`
}

// ApplyOptions parameterises applying a message to a record.
type ApplyOptions struct {
	// ReceivedAt anchors the resolution of bare DDMMM dates. Use the time the
	// message was received, not now, so replay reproduces the original reading.
	ReceivedAt time.Time
	// Party identifies the sender, used for provenance and for attributing
	// record locators.
	Party string
	// Inbound reports the direction. An inbound request records segments as
	// requested-of-us; an inbound reply settles segments we requested.
	Inbound bool
	// Self is this node's own designator. A locator element naming us is an
	// echo of the locator we already hold, not a partner's reference, and
	// filing it alongside the partners' would make the record claim an external
	// system holds a locator that is in fact ours.
	Self string
}

// Apply folds an AIRIMP message into a record, returning what changed.
//
// Apply is deliberately total: it never returns an error for content it cannot
// interpret. Anything unplaceable is attached to the record as an unparsed
// fragment, so a dialect gap shows up as a visible fragment on a real booking
// rather than as a rejected message.
func Apply(p *pnr.PNR, m *Message, opts ApplyOptions) []Change {
	if opts.ReceivedAt.IsZero() {
		opts.ReceivedAt = time.Now().UTC()
	}
	var changes []Change
	add := func(op, format string, args ...any) {
		changes = append(changes, Change{Op: op, Detail: fmt.Sprintf(format, args...)})
	}

	for _, e := range m.Elements {
		switch el := e.(type) {

		case *Name:
			for _, g := range el.Givens {
				given, title := pnr.SplitTitle(g)
				if findPassenger(p, el.Surname, given) != nil {
					continue
				}
				p.Passengers = append(p.Passengers, pnr.Passenger{
					Surname: el.Surname, Given: given, Title: title, Infant: el.Infant,
				})
				add("add_passenger", "%s/%s", el.Surname, g)
			}

		case *Segment:
			applySegment(p, el, opts, add)

		case *SSR:
			s := pnr.SSR{
				Code: el.Code, Carrier: el.Carrier, Status: string(el.Action),
				Count: el.Count, Text: el.FreeText, Sensitive: el.Sensitive(),
			}
			if seg := p.SegmentByKey(itineraryKey(el.Itinerary)); seg != nil {
				s.SegmentRef = seg.Ref
			}
			if i := updateSSR(p, s); i >= 0 {
				add("update_ssr", "%s %s", el.Code, el.Action)
			} else {
				p.SSRs = append(p.SSRs, s)
				add("add_ssr", "%s %s%d", el.Code, el.Action, el.Count)
			}

		case *OSI:
			p.OSIs = append(p.OSIs, pnr.OSI{Carrier: el.Carrier, Text: el.Text})
			add("add_osi", "%s %s", el.Carrier, el.Text)

		case *Locator:
			owner := el.Carrier
			if owner == "" {
				owner = opts.Party
			}
			if opts.Self != "" && owner == opts.Self {
				continue
			}
			if prev, ok := p.LocatorFor(owner); !ok || prev != el.Value {
				p.SetLocator(owner, el.Value)
				add("set_locator", "%s=%s", owner, el.Value)
			}

		case *Ticketing:
			p.Ticketing = append(p.Ticketing, pnr.Ticketing{Text: el.Text})
			add("add_ticketing", "%s", el.Text)

		case *Contact:
			p.Contacts = append(p.Contacts, pnr.Contact{Type: contactType(el.Text), Text: el.Text})
			add("add_contact", "%s", el.Text)

		case *ReceivedFrom:
			p.ReceivedFrom = el.Text
			add("received_from", "%s", el.Text)

		case *Remark:
			p.Remarks = append(p.Remarks, pnr.Remark{Text: el.Text})
			add("add_remark", "%s", el.Text)

		case *Unknown:
			p.Unparsed = append(p.Unparsed, pnr.Fragment{
				Source: "airimp",
				Detail: fmt.Sprintf("line %d, profile %s", el.LineNo, m.Profile),
				Raw:    el.Line,
			})
			add("unparsed", "line %d retained verbatim", el.LineNo)
		}
	}

	p.Recompute()
	return changes
}

func applySegment(p *pnr.PNR, el *Segment, opts ApplyOptions, add func(string, string, ...any)) {
	depart, err := pnr.ResolveDate(el.Date, opts.ReceivedAt)
	if err != nil {
		// An unresolvable date must not be guessed at. Keep the segment with a
		// zero departure and record the raw value so the problem is visible.
		p.Unparsed = append(p.Unparsed, pnr.Fragment{
			Source: "airimp", Detail: "unresolvable segment date", Raw: el.Wire(),
		})
	}

	key := el.Key()
	existing := p.SegmentByKey(key)

	switch el.Action.Category() {
	case CatReply:
		holding, _ := ReplyTo(el.Action)
		if existing == nil {
			// A reply to a segment we have no record of. Keep it: it is
			// evidence of a state divergence with the partner, and dropping it
			// would hide that.
			p.Unparsed = append(p.Unparsed, pnr.Fragment{
				Source: "airimp",
				Detail: fmt.Sprintf("reply %s for a segment not on this record", el.Action),
				Raw:    el.Wire(),
			})
			add("orphan_reply", "%s for %s", el.Action, key)
			return
		}
		if holding == "" {
			existing.Status = string(el.Action)
			add("segment_refused", "%s %s", existing.Describe(), el.Action)
			return
		}
		if ActionCode(existing.Status).Category() == CatCancel {
			// A confirmation for a segment already cancelled: the reply and
			// the cancellation crossed on the network, the way store-and-
			// forward retries reorder things. The cancellation stands -- a
			// dead segment must not be confirmed back to life -- and the
			// change is named so the layer above can tell the partner again.
			add("late_confirmation_ignored", "%s answered %s after cancellation",
				existing.Describe(), el.Action)
			return
		}
		existing.Status = string(holding)
		existing.Seats = el.Seats
		add("segment_confirmed", "%s -> %s", existing.Describe(), holding)

	case CatCancel:
		if existing == nil {
			add("cancel_unknown_segment", "%s", key)
			return
		}
		existing.Status = "XX"
		add("segment_cancelled", "%s", existing.Describe())

	default:
		// A request or a holding statement. Record the segment at the status the
		// sender asserted, or at HN when they are asking us for something.
		status := string(el.Action)
		if opts.Inbound && el.Action.NeedsReply() {
			status = "HN"
		}
		if existing != nil {
			existing.Status = status
			existing.Seats = el.Seats
			add("segment_updated", "%s", existing.Describe())
			return
		}
		seg := pnr.Segment{
			Type: pnr.SegmentAir, Carrier: el.Carrier, FlightNum: el.FlightNum,
			Class: el.Class, Depart: depart, WireDate: el.Date,
			Board: el.Board, Off: el.Off, Status: status, Seats: el.Seats,
		}
		p.Segments = append(p.Segments, seg)
		p.Recompute()
		add("add_segment", "%s", p.Segments[len(p.Segments)-1].Describe())
	}
}

// BuildSell renders a sell request for the segments of p that belong to carrier
// and are still awaiting action.
func BuildSell(p *pnr.PNR, carrier string, action ActionCode) string {
	var els []Element
	for _, s := range p.Segments {
		if s.Carrier != carrier || s.Type != pnr.SegmentAir {
			continue
		}
		els = append(els, &Segment{
			Carrier: s.Carrier, FlightNum: s.FlightNum, Class: s.Class,
			Date: s.WireDate, Board: s.Board, Off: s.Off,
			Action: action, Seats: s.Seats,
		})
	}
	if len(els) == 0 {
		return ""
	}
	for _, grp := range groupPassengers(p) {
		els = append(els, grp)
	}
	for _, s := range p.SSRs {
		if s.Carrier != "" && s.Carrier != carrier {
			continue
		}
		els = append(els, &SSR{
			Code: s.Code, Carrier: carrier, Action: ActionCode(s.Status),
			Count: s.Count, FreeText: s.Text,
		})
	}
	for _, o := range p.OSIs {
		if o.Carrier != "" && o.Carrier != carrier {
			continue
		}
		els = append(els, &OSI{Carrier: carrier, Text: o.Text})
	}
	if p.RecordLocator != "" {
		els = append(els, &Locator{Carrier: p.Origin.Party, Value: p.RecordLocator})
	}
	return Build("SS", els...)
}

// BuildReply renders a reply message answering the segments in req with the
// decisions in outcomes, keyed by segment Key.
//
// The reply carries every record locator known for the booking, ours first and
// then the requester's. Echoing the requester's locator is what lets them match
// the reply to the booking they made: their locator means nothing to us, and
// ours means nothing to them, so a reply carrying only one of the two is a
// reply the other side cannot file.
func BuildReply(req *Message, outcomes map[string]ActionCode, rec *pnr.PNR, self string) string {
	var els []Element
	for _, s := range req.Segments() {
		code, ok := outcomes[s.Key()]
		if !ok {
			code = "NO"
		}
		els = append(els, &Segment{
			Carrier: s.Carrier, FlightNum: s.FlightNum, Class: s.Class,
			Date: s.Date, Board: s.Board, Off: s.Off, Action: code, Seats: s.Seats,
		})
	}
	for _, n := range req.Names() {
		els = append(els, n)
	}
	if rec != nil {
		if rec.RecordLocator != "" {
			els = append(els, &Locator{Carrier: self, Value: rec.RecordLocator})
		}
		for _, l := range rec.Locators {
			if l.Owner != self && l.Value != "" {
				els = append(els, &Locator{Carrier: l.Owner, Value: l.Value})
			}
		}
	}
	return Build("", els...)
}

// groupPassengers rebuilds name elements, grouping travellers by surname the
// way the wire format expects.
func groupPassengers(p *pnr.PNR) []Element {
	var order []string
	byName := map[string][]string{}
	for _, pax := range p.Passengers {
		g := pax.Given
		if pax.Title != "" {
			g += pax.Title
		}
		if _, seen := byName[pax.Surname]; !seen {
			order = append(order, pax.Surname)
		}
		byName[pax.Surname] = append(byName[pax.Surname], g)
	}
	var out []Element
	for _, sn := range order {
		out = append(out, &Name{Count: len(byName[sn]), Surname: sn, Givens: byName[sn]})
	}
	return out
}

func findPassenger(p *pnr.PNR, surname, given string) *pnr.Passenger {
	for i := range p.Passengers {
		if p.Passengers[i].Surname == surname && p.Passengers[i].Given == given {
			return &p.Passengers[i]
		}
	}
	return nil
}

func updateSSR(p *pnr.PNR, s pnr.SSR) int {
	for i := range p.SSRs {
		if p.SSRs[i].Code == s.Code && p.SSRs[i].Carrier == s.Carrier && p.SSRs[i].Text == s.Text {
			p.SSRs[i].Status = s.Status
			p.SSRs[i].Count = s.Count
			return i
		}
	}
	return -1
}

// itineraryKey converts an SSR itinerary reference (LHRJFK0175Y15JUN) into the
// segment key form, so an SSR can be tied to the segment it applies to.
func itineraryKey(itin string) string {
	if len(itin) < 12 {
		return ""
	}
	board, off, rest := itin[0:3], itin[3:6], itin[6:]
	if len(rest) < 7 {
		return ""
	}
	date := rest[len(rest)-5:]
	class := rest[len(rest)-6 : len(rest)-5]
	flight := rest[:len(rest)-6]
	return strings.Join([]string{"", flight, class, date, board, off}, "|")
}

func contactType(text string) string {
	switch {
	case strings.Contains(text, "@"):
		return "email"
	case strings.ContainsAny(text, "0123456789"):
		return "phone"
	}
	return "other"
}

// BuildCancel renders a cancellation for a carrier's segments.
//
// Cancelling is the message this package went longest without, and its absence
// reached further than it looks: with no way to tell a carrier a booking is
// off, nothing could safely cancel one at all. A record marked cancelled while
// the carrier still holds the seats is worse than a record left alone, because
// it looks settled to everybody on this side and is not.
//
// refs names the segments to cancel by their record position. Nil cancels every
// segment this carrier holds, which is what cancelling a booking means.
func BuildCancel(p *pnr.PNR, carrier string, refs []int) string {
	want := map[int]bool{}
	for _, r := range refs {
		want[r] = true
	}

	var els []Element
	for _, s := range p.Segments {
		if s.Carrier != carrier || s.Type != pnr.SegmentAir {
			continue
		}
		// Already-cancelled segments are skipped by default -- a partial
		// cancel must not re-cancel the rest -- but a caller naming refs
		// explicitly is re-issuing a cancellation a partner shows no sign of
		// having applied, and gets exactly what it asked for.
		if len(want) > 0 {
			if !want[s.Ref] {
				continue
			}
		} else if s.Status == "XX" {
			continue
		}
		els = append(els, &Segment{
			Carrier: s.Carrier, FlightNum: s.FlightNum, Class: s.Class,
			Date: s.WireDate, Board: s.Board, Off: s.Off,
			Action: ActionCancel, Seats: s.Seats,
		})
	}
	if len(els) == 0 {
		return ""
	}
	for _, grp := range groupPassengers(p) {
		els = append(els, grp)
	}
	// Both locators, ours and theirs. A cancellation the carrier cannot file
	// against a booking is a cancellation that does not happen.
	if p.RecordLocator != "" {
		els = append(els, &Locator{Carrier: p.Origin.Party, Value: p.RecordLocator})
	}
	for _, l := range p.Locators {
		if l.Owner == carrier && l.Value != "" {
			els = append(els, &Locator{Carrier: l.Owner, Value: l.Value})
		}
	}
	return Build(string(ActionCancel), els...)
}
