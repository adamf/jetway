package padis

import (
	"fmt"
	"strconv"
	"time"

	"github.com/adamf/jetway/pkg/edifact"
	"github.com/adamf/jetway/pkg/pnr"
)

// BuildOptions parameterises message construction.
type BuildOptions struct {
	Sender     edifact.Party
	Recipient  edifact.Party
	ControlRef string
	MessageRef string
	Now        time.Time
	// SyntaxVersion selects the ISO 9735 version to declare. 3 is the safe
	// default for IATA traffic.
	SyntaxVersion int
	Test          bool
}

func (o BuildOptions) now() time.Time {
	if o.Now.IsZero() {
		return time.Now().UTC()
	}
	return o.Now.UTC()
}

func (o BuildOptions) msgRef() string {
	if o.MessageRef == "" {
		return "1"
	}
	return o.MessageRef
}

// paoresID is the message identifier for the reservation response.
func messageID(t string) edifact.MessageID {
	return edifact.MessageID{Type: t, Version: "96", Release: "1", ControllingAgency: "IA"}
}

func newInterchange(t string, o BuildOptions) *edifact.Interchange {
	now := o.now()
	v := o.SyntaxVersion
	if v == 0 {
		v = 3
	}
	return edifact.NewInterchange(edifact.UNBParams{
		CharsetID: "UNOA", SyntaxVersion: v,
		Sender: o.Sender, Recipient: o.Recipient,
		Date: now.Format("060102"), Time: now.Format("1504"),
		ControlRef: o.ControlRef, AppRef: t, Test: o.Test,
	})
}

// BuildPAOREQ renders a reservation request for the segments of p operated by
// carrier.
func BuildPAOREQ(p *pnr.PNR, carrier string, o BuildOptions) (*edifact.Interchange, error) {
	body := []edifact.Segment{
		edifact.Seg("MSG", edifact.Comp("", "11")), // 11: request
		edifact.Seg("ORG", edifact.Comp(p.Origin.Party, p.Origin.Agent)),
	}
	if p.RecordLocator != "" {
		body = append(body, edifact.Seg("RCI", edifact.Comp(p.Origin.Party, p.RecordLocator, "")))
	}
	body = append(body, tifSegments(p)...)

	n := 0
	for _, s := range p.Segments {
		if s.Carrier != carrier || s.Type != pnr.SegmentAir {
			continue
		}
		body = append(body, tvlSegment(s), edifact.Seg("RPI",
			edifact.Simple(strconv.Itoa(s.Seats)), edifact.Simple("NN")))
		n++
	}
	if n == 0 {
		return nil, fmt.Errorf("padis: record has no %s segments to request", carrier)
	}
	body = append(body, ssrSegments(p, carrier)...)
	for _, c := range p.Contacts {
		body = append(body, edifact.Seg("IFT", edifact.Comp("3", "1"), edifact.Simple(c.Text)))
	}
	for _, osi := range p.OSIs {
		body = append(body, edifact.Seg("IFT", edifact.Comp("4", "28"), edifact.Simple(osi.Text)))
	}

	ic := newInterchange(MsgPAOREQ, o)
	ic.AddMessage(o.msgRef(), messageID(MsgPAOREQ), body...)
	ic.Finalize()
	return ic, nil
}

// BuildPAORES renders a reservation response answering req, where outcomes maps
// a segment key to the status code decided for it.
func BuildPAORES(req edifact.Message, p *pnr.PNR, outcomes map[string]string, locator, carrier string, o BuildOptions) (*edifact.Interchange, error) {
	body := []edifact.Segment{
		edifact.Seg("MSG", edifact.Comp("", "22")), // 22: response
	}
	// RCI carries every locator known for the booking. The requester files the
	// reply against their own locator, so omitting it leaves them unable to
	// match the answer to the request.
	var rci []edifact.Element
	if locator != "" {
		rci = append(rci, edifact.Comp(carrier, locator, ""))
	}
	for _, l := range p.Locators {
		if l.Owner != carrier && l.Value != "" {
			rci = append(rci, edifact.Comp(l.Owner, l.Value, ""))
		}
	}
	if len(rci) > 0 {
		body = append(body, edifact.Segment{Tag: "RCI", Elements: rci})
	}
	body = append(body, tifSegments(p)...)
	for _, s := range p.Segments {
		if s.Carrier != carrier {
			continue
		}
		status, ok := outcomes[s.Key()]
		if !ok {
			status = "NO"
		}
		body = append(body, tvlSegment(s), edifact.Seg("RPI",
			edifact.Simple(strconv.Itoa(s.Seats)), edifact.Simple(status)))
	}
	body = append(body, ssrSegments(p, carrier)...)

	ic := newInterchange(MsgPAORES, o)
	ic.AddMessage(o.msgRef(), messageID(MsgPAORES), body...)
	ic.Finalize()
	return ic, nil
}

func tvlSegment(s pnr.Segment) edifact.Segment {
	dep := FormatTVLDate(s.Depart)
	return edifact.Seg("TVL",
		edifact.Comp(dep, s.DepartTime, dep, s.ArriveTime),
		edifact.Simple(s.Board),
		edifact.Simple(s.Off),
		edifact.Simple(s.Carrier),
		edifact.Comp(s.FlightNum, s.Class),
	)
}

// tifSegments groups passengers by surname, one TIF per surname, which is how
// the format expects a family to be carried.
func tifSegments(p *pnr.PNR) []edifact.Segment {
	var order []string
	byName := map[string][]pnr.Passenger{}
	for _, pax := range p.Passengers {
		if _, seen := byName[pax.Surname]; !seen {
			order = append(order, pax.Surname)
		}
		byName[pax.Surname] = append(byName[pax.Surname], pax)
	}
	var out []edifact.Segment
	for _, sn := range order {
		els := []edifact.Element{edifact.Simple(sn)}
		for _, pax := range byName[sn] {
			inf := ""
			if pax.Infant {
				inf = "INF"
			}
			els = append(els, edifact.Comp(pax.Given, pax.Title, strconv.Itoa(pax.Ref), inf))
		}
		out = append(out, edifact.Segment{Tag: "TIF", Elements: els})
	}
	return out
}

func ssrSegments(p *pnr.PNR, carrier string) []edifact.Segment {
	var out []edifact.Segment
	for _, s := range p.SSRs {
		if s.Carrier != "" && s.Carrier != carrier {
			continue
		}
		out = append(out, edifact.Seg("SSR", edifact.Comp(
			s.Code, s.Status, strconv.Itoa(s.Count), carrier, "", "", s.Text)))
	}
	return out
}
