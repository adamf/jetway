package api

import (
	"fmt"
	"strings"
	"time"

	"github.com/adamf/jetway/internal/store"
	"github.com/adamf/jetway/pkg/airimp"
	"github.com/adamf/jetway/pkg/avail"
	"github.com/adamf/jetway/pkg/avs"
	"github.com/adamf/jetway/pkg/edifact"
	"github.com/adamf/jetway/pkg/typeb"
)

// Field is one labelled value in a decoded view.
type Field struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Note  string `json:"note,omitempty"`
}

// Part is one decoded element or segment.
type Part struct {
	Kind   string  `json:"kind"`
	Wire   string  `json:"wire"`
	Fields []Field `json:"fields,omitempty"`
	// Note explains what the segment or element carries.
	Note string `json:"note,omitempty"`
	// Unrecognised marks content no grammar claimed, which is the signal that a
	// partner's dialect has drifted from the profile in use.
	Unrecognised bool `json:"unrecognised,omitempty"`
}

// Explained is a message rendered for a human: the envelope, the body broken
// into parts, and whatever the decoders complained about.
type Explained struct {
	Format      string             `json:"format"`
	Summary     string             `json:"summary"`
	Envelope    []Field            `json:"envelope"`
	Parts       []Part             `json:"parts"`
	Diagnostics []store.Diagnostic `json:"diagnostics,omitempty"`
	Raw         string             `json:"raw"`
}

// Explain decodes raw message bytes for display. It never fails: a message that
// cannot be decoded is exactly the one an operator most needs to look at.
func Explain(raw []byte) *Explained {
	e := &Explained{Raw: string(raw)}
	trimmed := strings.TrimLeft(string(raw), " \r\n\t")
	if strings.HasPrefix(trimmed, "UNB") || strings.HasPrefix(trimmed, "UNA") {
		return explainEDIFACT(raw, e)
	}
	return explainTypeB(raw, e)
}

func explainTypeB(raw []byte, e *Explained) *Explained {
	e.Format = "Type B / AIRIMP"
	tb, err := typeb.Parse(raw)
	if err != nil {
		e.Summary = "undecodable: " + err.Error()
		return e
	}
	dests := make([]string, 0, len(tb.Destinations))
	for _, d := range tb.Destinations {
		dests = append(dests, d.String())
	}
	e.Envelope = []Field{
		{Name: "Priority", Value: tb.Priority,
			// The band, not just the meaning: it is what decides the order a
			// backlog goes out in when a link comes back.
			Note: priorityNote(tb.Priority)},
		{Name: "Destinations", Value: strings.Join(dests, " ")},
		{Name: "Origin", Value: tb.Origin.String()},
		{Name: "Origin time", Value: tb.OriginTime.String(), Note: "DDHHMM, UTC"},
	}
	for _, d := range tb.Diagnostics {
		e.Diagnostics = append(e.Diagnostics, store.Diagnostic{
			Layer: "typeb", Severity: d.Severity.String(), Code: d.Code, Detail: d.Detail, Line: d.Line,
		})
	}

	if avs.IsAvailability(tb.Text) {
		return explainAVS(tb.Text, e)
	}

	m := airimp.Parse(tb.Text)
	e.Summary = "AIRIMP " + string(m.Intent())
	for _, d := range m.Diagnostics {
		e.Diagnostics = append(e.Diagnostics, store.Diagnostic{
			Layer: "airimp", Severity: d.Severity.String(), Code: d.Code, Detail: d.Detail, Line: d.Line,
		})
	}
	for _, el := range m.Elements {
		e.Parts = append(e.Parts, explainElement(el))
	}
	return e
}

// explainAVS renders an availability message as the beliefs it asserts, which
// is what an operator wants to see: what is sellable, on what, and how sure.
func explainAVS(text string, e *Explained) *Explained {
	e.Format = "Type B / AVS"
	m := avs.Parse(text, time.Now().UTC())
	e.Summary = fmt.Sprintf("availability status, %d entries", len(m.Entries))

	for _, d := range m.Diagnostics {
		e.Diagnostics = append(e.Diagnostics, store.Diagnostic{
			Layer: "avs", Severity: d.Severity.String(), Code: d.Code, Detail: d.Detail, Line: d.Line,
		})
	}

	// Group by segment, the way a carrier publishes it.
	type group struct {
		head    string
		entries []avail.Entry
	}
	var order []string
	byFlight := map[string]*group{}
	for _, en := range m.Entries {
		head := fmt.Sprintf("%s%s  %s-%s  %s",
			en.Key.Carrier, en.Key.FlightNum, en.Key.Board, en.Key.Off, en.Key.Date)
		g, ok := byFlight[head]
		if !ok {
			g = &group{head: head}
			byFlight[head] = g
			order = append(order, head)
		}
		g.entries = append(g.entries, en)
	}
	for _, head := range order {
		g := byFlight[head]
		p := Part{Kind: "flight", Wire: g.head}
		for _, en := range g.entries {
			note := string(en.Status)
			if en.SeatsKnown {
				note = fmt.Sprintf("%s, %d seats offered", en.Status, en.Seats)
			}
			if en.Status == avail.Open {
				note += " — sellable without asking"
			}
			p.Fields = append(p.Fields, Field{Name: "Class " + en.Key.Class,
				Value: string(en.Status), Note: note})
		}
		p.Note = "availability granted in advance; a booking against an open class needs no round trip"
		e.Parts = append(e.Parts, p)
	}
	return e
}

func explainElement(el airimp.Element) Part {
	p := Part{Kind: string(el.Kind()), Wire: el.Wire()}
	switch v := el.(type) {
	case *airimp.Segment:
		note := ""
		if info, ok := v.Action.Info(); ok {
			note = info.Meaning
		} else {
			note = "not in the interline vocabulary"
		}
		p.Fields = []Field{
			{Name: "Carrier", Value: v.Carrier},
			{Name: "Flight", Value: v.FlightNum},
			{Name: "Class", Value: v.Class},
			{Name: "Date", Value: v.Date, Note: "DDMMM, no year on the wire"},
			{Name: "Board", Value: v.Board},
			{Name: "Off", Value: v.Off},
			{Name: "Action", Value: string(v.Action), Note: note},
			{Name: "Seats", Value: fmt.Sprint(v.Seats)},
		}
	case *airimp.Name:
		p.Fields = []Field{
			{Name: "Count", Value: fmt.Sprint(v.Count)},
			{Name: "Surname", Value: v.Surname},
			{Name: "Given", Value: strings.Join(v.Givens, ", ")},
		}
	case *airimp.SSR:
		note := ""
		if v.Sensitive() {
			note = "carries personal data"
		}
		p.Fields = []Field{
			{Name: "Code", Value: v.Code, Note: note},
			{Name: "Carrier", Value: v.Carrier},
			{Name: "Status", Value: string(v.Action)},
			{Name: "Count", Value: fmt.Sprint(v.Count)},
		}
		if v.Itinerary != "" {
			p.Fields = append(p.Fields, Field{Name: "Itinerary", Value: v.Itinerary})
		}
		if v.FreeText != "" {
			p.Fields = append(p.Fields, Field{Name: "Text", Value: v.FreeText})
		}
	case *airimp.OSI:
		p.Fields = []Field{{Name: "Carrier", Value: v.Carrier}, {Name: "Text", Value: v.Text}}
	case *airimp.Locator:
		p.Fields = []Field{{Name: "Owner", Value: v.Carrier}, {Name: "Locator", Value: v.Value}}
	case *airimp.Unknown:
		p.Unrecognised = true
		p.Fields = []Field{{Name: "Line", Value: fmt.Sprint(v.LineNo),
			Note: "no recognizer in the active profile claimed this line; kept verbatim"}}
	}
	return p
}

func explainEDIFACT(raw []byte, e *Explained) *Explained {
	e.Format = "UN/EDIFACT / PADIS"
	ic, err := edifact.Parse(raw, edifact.ParseOptions{})
	if err != nil {
		e.Summary = "undecodable: " + err.Error()
		return e
	}
	date, tm := ic.PreparedDate()
	charset, version := ic.SyntaxIdentifier()
	e.Envelope = []Field{
		{Name: "Sender", Value: ic.Sender().String()},
		{Name: "Recipient", Value: ic.Recipient().String()},
		{Name: "Control ref", Value: ic.ControlRef(), Note: "UNB 0020; the sender's idempotency key"},
		{Name: "Prepared", Value: date + " " + tm},
		{Name: "Syntax", Value: fmt.Sprintf("%s v%d", charset, version)},
	}
	if ic.TestIndicator() {
		e.Envelope = append(e.Envelope, Field{Name: "Test", Value: "yes",
			Note: "test traffic; must not be applied to production state"})
	}
	for _, d := range ic.Diagnostics {
		e.Diagnostics = append(e.Diagnostics, store.Diagnostic{
			Layer: "edifact", Severity: d.Severity.String(), Code: d.Code, Detail: d.Detail,
		})
	}
	if len(ic.Messages) == 0 {
		e.Summary = "interchange with no messages"
		return e
	}
	m := ic.Messages[0]
	e.Summary = m.ID().String()
	e.Parts = append(e.Parts, Part{Kind: "UNH", Wire: m.Header.String(), Fields: []Field{
		{Name: "Reference", Value: m.Reference()},
		{Name: "Message", Value: m.ID().String(), Note: "type:version:release:agency"},
	}})
	for _, seg := range m.Segments {
		p := Part{Kind: seg.Tag, Wire: seg.String()}
		for i, el := range seg.Elements {
			for r, comp := range el {
				name := fmt.Sprintf("%d", i+1)
				if len(el) > 1 {
					name = fmt.Sprintf("%d[%d]", i+1, r+1)
				}
				p.Fields = append(p.Fields, Field{Name: name, Value: strings.Join(comp, " : ")})
			}
		}
		p.Note = segmentNote(seg.Tag)
		e.Parts = append(e.Parts, p)
	}
	if m.Trailer.Tag != "" {
		e.Parts = append(e.Parts, Part{Kind: "UNT", Wire: m.Trailer.String(), Fields: []Field{
			{Name: "Segments", Value: m.Trailer.Value(0), Note: "counts UNH and UNT"},
			{Name: "Reference", Value: m.Trailer.Value(1), Note: "must match UNH"},
		}})
	}
	return e
}

// segmentNote explains what the segments in the shipped PADIS profile carry.
func segmentNote(tag string) string {
	switch tag {
	case "MSG":
		return "message function: what this message is for"
	case "ORG":
		return "originator: system, office and agent"
	case "TIF":
		return "traveller information: surname and given names"
	case "TVL":
		return "travel product: date, board and off points, carrier, flight and class"
	case "RPI":
		return "related product information: seat count and status for the preceding TVL"
	case "SSR":
		return "special service request"
	case "RCI":
		return "reservation control information: the record locators each party holds"
	case "IFT":
		return "interactive free text: contact details, remarks and other service information"
	}
	return ""
}

// priorityNote explains a priority code and the service band it falls in.
func priorityNote(code string) string {
	if code == "" {
		return ""
	}
	meaning := typeb.KnownPriorities[code]
	band := typeb.ClassOf(code).String()
	if meaning == "" {
		return "not in the known set; serviced as " + band
	}
	return meaning + "; serviced as " + band
}
