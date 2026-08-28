// Package padis maps IATA PADIS EDIFACT messages onto the canonical PNR.
//
// # Provenance
//
// The PADIS message directories and implementation guides are IATA
// publications and are the normative source for segment composition. This
// package ships a community profile: the segments a reservation gateway must
// act on -- MSG, ORG, TIF, TVL, RPI, SSR, RCI, IFT -- in the composition most
// widely used for PAOREQ and PAORES. It is not a substitute for a carrier's
// implementation guide, and it is structured on the assumption that you will
// need to adjust it per link.
//
// Two properties make that assumption survivable. Segment handling is a
// registry, so a carrier profile can override any segment without forking this
// package. And every segment that no handler claims is attached to the record
// as an unparsed fragment, so a composition difference shows up as visible data
// on a live booking instead of as silence.
package padis

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/adamf/jetway/pkg/edifact"
	"github.com/adamf/jetway/pkg/pnr"
	"github.com/adamf/jetway/pkg/rescode"
)

// PADIS message types this package understands. Others decode at the syntax
// layer and route, but do not map onto a record.
const (
	MsgPAOREQ = "PAOREQ" // reservation request
	MsgPAORES = "PAORES" // reservation response
	MsgPNRGOV = "PNRGOV" // PNR data to a government authority
	MsgPAXLST = "PAXLST" // passenger manifest (APIS)
	MsgTKCREQ = "TKCREQ" // ticketing request
	MsgTKCRES = "TKCRES" // ticketing response
	MsgDCQCKI = "DCQCKI" // check-in query
	MsgDCRCKI = "DCRCKI" // check-in response
)

// Change mirrors airimp.Change so the gateway records one shape of event trail.
type Change struct {
	Op     string `json:"op"`
	Detail string `json:"detail"`
}

// ApplyOptions parameterises applying a message to a record.
type ApplyOptions struct {
	ReceivedAt time.Time
	Party      string
	Inbound    bool
	// Self is this node's own designator; see airimp.ApplyOptions.Self.
	Self string
}

// Handler maps one segment onto the record. It returns the changes it made.
// Returning ok=false leaves the segment for the fallback, which records it as
// an unparsed fragment.
type Handler func(p *pnr.PNR, seg edifact.Segment, st *State, opts ApplyOptions) (changes []Change, ok bool)

// State carries context across segments within one message. EDIFACT is
// positional: an RPI qualifies the TVL that preceded it, and a TIF's traveller
// references are needed by later SSRs.
//
// It is exported because Profile.Handlers is the extension point for carrier
// dialects, and a handler defined in another package must be able to name its
// own parameter types.
type State struct {
	// LastSegment is the itinerary segment most recently seen in this message,
	// which is what a following RPI or SSR qualifies.
	LastSegment *pnr.Segment
}

// Profile is a set of segment handlers.
type Profile struct {
	Name     string
	Handlers map[string]Handler
}

// Clone returns a copy that can be modified per link.
func (p *Profile) Clone(name string) *Profile {
	c := &Profile{Name: name, Handlers: map[string]Handler{}}
	for k, v := range p.Handlers {
		c.Handlers[k] = v
	}
	return c
}

// Default is the baseline PADIS reservation profile.
var Default = &Profile{
	Name: "padis-default",
	Handlers: map[string]Handler{
		"MSG": handleMSG,
		"ORG": handleORG,
		"TIF": handleTIF,
		"TVL": handleTVL,
		"RPI": handleRPI,
		"SSR": handleSSR,
		"RCI": handleRCI,
		"IFT": handleIFT,
	},
}

// Apply folds an EDIFACT message into a record.
func Apply(p *pnr.PNR, m edifact.Message, opts ApplyOptions) []Change {
	return Default.Apply(p, m, opts)
}

// Apply folds an EDIFACT message into a record using this profile.
func (pr *Profile) Apply(p *pnr.PNR, m edifact.Message, opts ApplyOptions) []Change {
	if opts.ReceivedAt.IsZero() {
		opts.ReceivedAt = time.Now().UTC()
	}
	st := &State{}
	var changes []Change
	for _, seg := range m.Segments {
		h, known := pr.Handlers[seg.Tag]
		if known {
			if cs, ok := h(p, seg, st, opts); ok {
				changes = append(changes, cs...)
				continue
			}
		}
		p.Unparsed = append(p.Unparsed, pnr.Fragment{
			Source: "edifact",
			Detail: fmt.Sprintf("segment %s in %s (profile %s)", seg.Tag, m.ID(), pr.Name),
			Raw:    seg.String(),
		})
		changes = append(changes, Change{Op: "unparsed", Detail: "segment " + seg.Tag + " retained verbatim"})
	}
	p.Recompute()
	return changes
}

func handleMSG(p *pnr.PNR, seg edifact.Segment, _ *State, _ ApplyOptions) ([]Change, bool) {
	fn := seg.Get(0, 1)
	return []Change{{Op: "message_function", Detail: fn}}, true
}

func handleORG(p *pnr.PNR, seg edifact.Segment, _ *State, opts ApplyOptions) ([]Change, bool) {
	sys := seg.Get(0, 0)
	loc := seg.Get(0, 1)
	agent := seg.Value(1)
	if p.Origin.Party == "" {
		p.Origin.Party = sys
	}
	if agent != "" {
		p.Origin.Agent = agent
	}
	return []Change{{Op: "origin", Detail: strings.TrimRight(sys+" "+loc+" "+agent, " ")}}, true
}

// handleTIF reads traveller names.
//
// Profile: TIF+<surname>+<given>:<title>:<ref>+<given>:<title>:<ref>...
func handleTIF(p *pnr.PNR, seg edifact.Segment, _ *State, _ ApplyOptions) ([]Change, bool) {
	surname := strings.TrimSpace(seg.Value(0))
	if surname == "" {
		return nil, false
	}
	var changes []Change
	if len(seg.Elements) == 1 {
		p.Passengers = append(p.Passengers, pnr.Passenger{Surname: surname})
		return []Change{{Op: "add_passenger", Detail: surname}}, true
	}
	for i := 1; i < len(seg.Elements); i++ {
		c := seg.Elem(i).First()
		given := strings.TrimSpace(c.Get(0))
		if given == "" {
			continue
		}
		title := strings.TrimSpace(c.Get(1))
		if hasPassenger(p, surname, given) {
			continue
		}
		p.Passengers = append(p.Passengers, pnr.Passenger{
			Surname: surname, Given: given, Title: title,
			Infant: strings.EqualFold(c.Get(3), "INF"),
		})
		changes = append(changes, Change{Op: "add_passenger", Detail: surname + "/" + given})
	}
	return changes, true
}

// handleTVL reads a flight segment.
//
// Profile: TVL+<depDate>:<depTime>:<arrDate>:<arrTime>+<board>+<off>+<carrier>+<flight>:<class>
// Dates are DDMMYY on the wire in this profile; DDMMM is also accepted because
// some carriers send the AIRIMP form inside EDIFACT.
func handleTVL(p *pnr.PNR, seg edifact.Segment, st *State, opts ApplyOptions) ([]Change, bool) {
	board, off := seg.Value(1), seg.Value(2)
	carrier := seg.Value(3)
	flight := seg.Get(4, 0)
	class := seg.Get(4, 1)
	if board == "" || off == "" || carrier == "" {
		return nil, false
	}
	depart, wire, err := parseTVLDate(seg.Get(0, 0), opts.ReceivedAt)
	if err != nil {
		p.Unparsed = append(p.Unparsed, pnr.Fragment{
			Source: "edifact", Detail: "unresolvable TVL date", Raw: seg.String(),
		})
	}
	s := pnr.Segment{
		Type: pnr.SegmentAir, Carrier: carrier, FlightNum: flight, Class: class,
		Depart: depart, WireDate: wire, DepartTime: seg.Get(0, 1), ArriveTime: seg.Get(0, 3),
		Board: board, Off: off, Status: "HN", Seats: 1,
	}
	if existing := p.SegmentByKey(s.Key()); existing != nil {
		st.LastSegment = existing
		return []Change{{Op: "segment_seen", Detail: existing.Describe()}}, true
	}
	p.Segments = append(p.Segments, s)
	p.Recompute()
	st.LastSegment = &p.Segments[len(p.Segments)-1]
	return []Change{{Op: "add_segment", Detail: st.LastSegment.Describe()}}, true
}

// handleRPI applies a status and quantity to the segment that preceded it.
//
// Profile: RPI+<quantity>+<status>
func handleRPI(p *pnr.PNR, seg edifact.Segment, st *State, opts ApplyOptions) ([]Change, bool) {
	if st.LastSegment == nil {
		return nil, false
	}
	if q, err := strconv.Atoi(seg.Value(0)); err == nil && q > 0 {
		st.LastSegment.Seats = q
	}
	code := rescode.ActionCode(strings.ToUpper(seg.Value(1)))
	if code == "" {
		return nil, false
	}
	switch {
	case opts.Inbound && code.NeedsReply():
		// Somebody is asking us for these seats. Record it as outstanding, not
		// as the request code itself, so the decision step can find it.
		st.LastSegment.Status = "HN"
		return []Change{{Op: "segment_requested", Detail: st.LastSegment.Describe()}}, true
	default:
		if h, isReply := rescode.ReplyTo(code); isReply {
			if h == "" {
				st.LastSegment.Status = string(code)
				return []Change{{Op: "segment_refused", Detail: st.LastSegment.Describe()}}, true
			}
			st.LastSegment.Status = string(h)
		} else {
			st.LastSegment.Status = string(code)
		}
	}
	return []Change{{Op: "segment_status", Detail: st.LastSegment.Describe()}}, true
}

// handleSSR reads a special service request.
//
// Profile: SSR+<code>:<status>:<quantity>:<company>:<board>:<off>:<freetext>
func handleSSR(p *pnr.PNR, seg edifact.Segment, st *State, _ ApplyOptions) ([]Change, bool) {
	c := seg.Elem(0).First()
	code := strings.ToUpper(c.Get(0))
	if code == "" {
		return nil, false
	}
	s := pnr.SSR{
		Code: code, Status: c.Get(1), Count: atoiOr(c.Get(2), 1),
		Carrier: c.Get(3), Text: strings.TrimSpace(c.Get(6)),
		Sensitive: sensitiveSSR(code),
	}
	if s.Text == "" && len(seg.Elements) > 1 {
		s.Text = strings.TrimSpace(seg.Value(1))
	}
	if st.LastSegment != nil {
		s.SegmentRef = st.LastSegment.Ref
	}
	p.SSRs = append(p.SSRs, s)
	return []Change{{Op: "add_ssr", Detail: code + " " + s.Status}}, true
}

// handleRCI reads reservation control information: the record locators each
// party holds for this booking.
//
// Profile: RCI+<company>:<locator>:<qualifier>[+<company>:<locator>...]
func handleRCI(p *pnr.PNR, seg edifact.Segment, _ *State, opts ApplyOptions) ([]Change, bool) {
	var changes []Change
	for i := range seg.Elements {
		for _, c := range seg.Elem(i) {
			owner, loc := c.Get(0), c.Get(1)
			if loc == "" {
				continue
			}
			if owner == "" {
				owner = opts.Party
			}
			if opts.Self != "" && owner == opts.Self {
				continue
			}
			if prev, ok := p.LocatorFor(owner); !ok || prev != loc {
				p.SetLocator(owner, loc)
				changes = append(changes, Change{Op: "set_locator", Detail: owner + "=" + loc})
			}
		}
	}
	return changes, len(changes) > 0 || len(seg.Elements) > 0
}

// handleIFT reads free text. The qualifier decides where it lands.
//
// Profile: IFT+<qualifier>:<function>+<text>
func handleIFT(p *pnr.PNR, seg edifact.Segment, _ *State, _ ApplyOptions) ([]Change, bool) {
	text := strings.TrimSpace(seg.Value(1))
	if text == "" {
		return nil, false
	}
	switch seg.Get(0, 0) {
	case "3": // contact
		p.Contacts = append(p.Contacts, pnr.Contact{Type: contactType(text), Text: text})
		return []Change{{Op: "add_contact", Detail: text}}, true
	case "4": // other service information
		p.OSIs = append(p.OSIs, pnr.OSI{Text: text})
		return []Change{{Op: "add_osi", Detail: text}}, true
	default:
		p.Remarks = append(p.Remarks, pnr.Remark{Text: text})
		return []Change{{Op: "add_remark", Detail: text}}, true
	}
}

func sensitiveSSR(code string) bool {
	switch code {
	case "DOCS", "DOCA", "DOCO", "FOID":
		return true
	}
	return false
}

func hasPassenger(p *pnr.PNR, surname, given string) bool {
	for _, x := range p.Passengers {
		if x.Surname == surname && x.Given == given {
			return true
		}
	}
	return false
}

// parseTVLDate accepts the DDMMYY form used in PADIS travel segments and the
// DDMMM form some carriers carry over from teletype. It returns the resolved
// date and the DDMMM spelling used as the canonical wire key.
func parseTVLDate(s string, ref time.Time) (time.Time, string, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if len(s) == 6 {
		if _, err := strconv.Atoi(s); err == nil {
			t, err := time.Parse("020106", s)
			if err != nil {
				return time.Time{}, "", err
			}
			return t, pnr.FormatDate(t), nil
		}
	}
	t, err := pnr.ResolveDate(s, ref)
	if err != nil {
		return time.Time{}, s, err
	}
	return t, pnr.FormatDate(t), nil
}

// FormatTVLDate renders a date in the DDMMYY form this profile sends.
func FormatTVLDate(t time.Time) string { return t.Format("020106") }

func atoiOr(s string, def int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return def
	}
	return n
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
