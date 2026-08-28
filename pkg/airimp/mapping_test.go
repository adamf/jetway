package airimp

import (
	"testing"
	"time"

	"github.com/adamf/jetway/pkg/pnr"
)

func at() time.Time { return time.Date(2026, time.June, 1, 12, 0, 0, 0, time.UTC) }

func inbound() ApplyOptions {
	return ApplyOptions{ReceivedAt: at(), Party: "BA", Inbound: true, Self: "1J"}
}

func TestApplySellCreatesRecord(t *testing.T) {
	m := Parse("SS\nBA0175Y15JUNLHRJFKNN2\n2SMITH/JOHNMR/ANNEMRS\n" +
		"SSR VGML BA NN1\nOSI BA CTCT LON 123\nAP LON 44-20-7777-7777\n" +
		"RF SMITH\nRM CHECK VISA\nRL BA/XYZ789")
	p := &pnr.PNR{}
	changes := Apply(p, m, inbound())
	if len(changes) == 0 {
		t.Fatal("Apply made no changes")
	}
	if len(p.Passengers) != 2 {
		t.Fatalf("passengers = %d: %+v", len(p.Passengers), p.Passengers)
	}
	if p.Passengers[0].Given != "JOHN" || p.Passengers[0].Title != "MR" {
		t.Errorf("title not split from the given name: %+v", p.Passengers[0])
	}
	if p.Passengers[1].Given != "ANNE" || p.Passengers[1].Title != "MRS" {
		t.Errorf("second traveller wrong: %+v", p.Passengers[1])
	}
	if len(p.Segments) != 1 {
		t.Fatalf("segments = %d", len(p.Segments))
	}
	s := p.Segments[0]
	// An inbound request is recorded as outstanding, not as the request code.
	if s.Status != "HN" {
		t.Errorf("status = %q, want HN for a request made of us", s.Status)
	}
	if s.Seats != 2 || s.Carrier != "BA" || s.Board != "LHR" {
		t.Errorf("segment = %+v", s)
	}
	if !s.Depart.Equal(time.Date(2026, time.June, 15, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("departure resolved to %s", s.Depart)
	}
	if len(p.SSRs) != 1 || len(p.OSIs) != 1 || len(p.Contacts) != 1 || len(p.Remarks) != 1 {
		t.Errorf("elements lost: ssr=%d osi=%d contact=%d remark=%d",
			len(p.SSRs), len(p.OSIs), len(p.Contacts), len(p.Remarks))
	}
	if p.ReceivedFrom != "SMITH" {
		t.Errorf("ReceivedFrom = %q", p.ReceivedFrom)
	}
	if loc, ok := p.LocatorFor("BA"); !ok || loc != "XYZ789" {
		t.Errorf("locator = %q,%v", loc, ok)
	}
}

// A locator naming us is an echo of what we already hold, not a partner's.
func TestApplySkipsOurOwnLocator(t *testing.T) {
	p := &pnr.PNR{RecordLocator: "ABC23D"}
	Apply(p, Parse("RL 1J/ABC23D\nRL BA/XYZ789"), inbound())
	if _, ok := p.LocatorFor("1J"); ok {
		t.Error("our own locator must not be filed as an external one")
	}
	if _, ok := p.LocatorFor("BA"); !ok {
		t.Error("the partner's locator must be recorded")
	}
}

func TestApplyReplySettlesSegment(t *testing.T) {
	p := &pnr.PNR{}
	Apply(p, Parse("BA0175Y15JUNLHRJFKNN1"), ApplyOptions{ReceivedAt: at(), Self: "BA"})
	if p.Segments[0].Status != "NN" {
		t.Fatalf("outbound-direction apply should keep the asserted code, got %q", p.Segments[0].Status)
	}

	// Now the same record, as a requester receiving an answer.
	q := &pnr.PNR{}
	Apply(q, Parse("BA0175Y15JUNLHRJFKNN1"), inbound())
	Apply(q, Parse("BA0175Y15JUNLHRJFKKK1"), ApplyOptions{ReceivedAt: at(), Party: "BA", Self: "1J"})
	if q.Segments[0].Status != "HK" {
		t.Errorf("status = %q, want HK", q.Segments[0].Status)
	}
	if len(q.Segments) != 1 {
		t.Errorf("the reply created a duplicate segment: %+v", q.Segments)
	}
}

// A reply for a segment we have no record of is evidence of a divergence with
// the partner. It must be kept, not dropped.
func TestOrphanReplyIsRetained(t *testing.T) {
	p := &pnr.PNR{}
	changes := Apply(p, Parse("BA0999Y15JUNLHRJFKKK1"), ApplyOptions{ReceivedAt: at(), Self: "1J"})
	if len(p.Unparsed) != 1 {
		t.Fatalf("unparsed = %d, want 1", len(p.Unparsed))
	}
	var sawOrphan bool
	for _, c := range changes {
		if c.Op == "orphan_reply" {
			sawOrphan = true
		}
	}
	if !sawOrphan {
		t.Errorf("expected an orphan_reply change: %+v", changes)
	}
	if len(p.Segments) != 0 {
		t.Error("an orphan reply must not create a segment")
	}
}

func TestApplyCancel(t *testing.T) {
	p := &pnr.PNR{}
	Apply(p, Parse("BA0175Y15JUNLHRJFKNN1"), inbound())
	Apply(p, Parse("BA0175Y15JUNLHRJFKXX1"), ApplyOptions{ReceivedAt: at(), Self: "1J"})
	if p.Segments[0].Status != "XX" {
		t.Errorf("status = %q, want XX", p.Segments[0].Status)
	}
	if p.Status != pnr.StatusCancelled {
		t.Errorf("record status = %q, want cancelled", p.Status)
	}
}

func TestApplyUnknownLineBecomesFragment(t *testing.T) {
	p := &pnr.PNR{}
	Apply(p, Parse("BA0175Y15JUNLHRJFKNN1\nZQ PRIVATE ELEMENT"), inbound())
	if len(p.Unparsed) != 1 {
		t.Fatalf("unparsed = %d", len(p.Unparsed))
	}
	if p.Unparsed[0].Raw != "ZQ PRIVATE ELEMENT" || p.Unparsed[0].Source != "airimp" {
		t.Errorf("fragment = %+v", p.Unparsed[0])
	}
	if len(p.Segments) != 1 {
		t.Error("the recognised segment must still be applied")
	}
}

func TestUnresolvableDateIsFlaggedNotGuessed(t *testing.T) {
	p := &pnr.PNR{}
	// 30FEB never exists.
	Apply(p, Parse("BA0175Y30FEBLHRJFKNN1"), inbound())
	if len(p.Unparsed) == 0 {
		t.Error("an unresolvable date must be recorded, not silently guessed")
	}
	if len(p.Segments) == 1 && !p.Segments[0].Depart.IsZero() {
		t.Errorf("departure was invented: %s", p.Segments[0].Depart)
	}
}

func TestSensitiveSSRIsMarked(t *testing.T) {
	p := &pnr.PNR{}
	Apply(p, Parse("SSR DOCS BA HK1 P/GBR/123456789"), inbound())
	if len(p.SSRs) != 1 || !p.SSRs[0].Sensitive {
		t.Errorf("DOCS must be flagged as personal data: %+v", p.SSRs)
	}
}

func TestBuildSellRoundTrips(t *testing.T) {
	depart, _ := pnr.ResolveDate("15JUN", at())
	p := &pnr.PNR{
		RecordLocator: "ABC23D",
		Origin:        pnr.Origin{Party: "1J"},
		Passengers: []pnr.Passenger{
			{Surname: "SMITH", Given: "JOHN", Title: "MR"},
			{Surname: "SMITH", Given: "ANNE", Title: "MRS"},
			{Surname: "BROWN", Given: "PAT"},
		},
		Segments: []pnr.Segment{
			{Type: pnr.SegmentAir, Carrier: "BA", FlightNum: "0175", Class: "Y",
				Depart: depart, WireDate: "15JUN", Board: "LHR", Off: "JFK", Status: "HN", Seats: 3},
			{Type: pnr.SegmentAir, Carrier: "AA", FlightNum: "0100", Class: "Y",
				Depart: depart, WireDate: "15JUN", Board: "JFK", Off: "DFW", Status: "HN", Seats: 3},
		},
		SSRs: []pnr.SSR{{Code: "VGML", Carrier: "BA", Status: "NN", Count: 1}},
	}
	text := BuildSell(p, "BA", "NN")
	if text == "" {
		t.Fatal("BuildSell produced nothing")
	}
	m := Parse(text)
	if len(m.Unknowns()) != 0 {
		t.Errorf("our own sell did not reparse: %v", m.Unknowns())
	}
	// Only the requested carrier's segments go to that carrier.
	segs := m.Segments()
	if len(segs) != 1 || segs[0].Carrier != "BA" {
		t.Errorf("segments = %+v; the AA leg must not be sent to BA", segs)
	}
	// Travellers are grouped by surname, as the wire format expects.
	names := m.Names()
	if len(names) != 2 {
		t.Fatalf("name elements = %d, want 2 (grouped by surname)", len(names))
	}
	if names[0].Surname != "SMITH" || names[0].Count != 2 {
		t.Errorf("first name element = %+v", names[0])
	}
	if names[1].Surname != "BROWN" || names[1].Count != 1 {
		t.Errorf("second name element = %+v", names[1])
	}
	if len(m.Locators()) != 1 || m.Locators()[0].Value != "ABC23D" {
		t.Errorf("our locator must be carried: %+v", m.Locators())
	}
	if _, err := pnr.ResolveDate(segs[0].Date, at()); err != nil {
		t.Errorf("built an unresolvable date %q", segs[0].Date)
	}
}

func TestBuildSellSkipsCarrierWithNoSegments(t *testing.T) {
	p := &pnr.PNR{Segments: []pnr.Segment{{Type: pnr.SegmentAir, Carrier: "BA"}}}
	if got := BuildSell(p, "LH", "NN"); got != "" {
		t.Errorf("BuildSell for an uninvolved carrier = %q, want empty", got)
	}
}

func TestBuildReplyCarriesBothLocators(t *testing.T) {
	req := Parse("BA0175Y15JUNLHRJFKNN1\n1SMITH/JOHNMR")
	rec := &pnr.PNR{RecordLocator: "CARR01"}
	rec.SetLocator("1J", "GDS001")
	out := BuildReply(req, map[string]ActionCode{
		req.Segments()[0].Key(): "KK",
	}, rec, "BA")

	m := Parse(out)
	if len(m.Segments()) != 1 || m.Segments()[0].Action != "KK" {
		t.Fatalf("reply segment = %+v", m.Segments())
	}
	locs := map[string]string{}
	for _, l := range m.Locators() {
		locs[l.Carrier] = l.Value
	}
	if locs["BA"] != "CARR01" {
		t.Errorf("our locator missing: %v", locs)
	}
	if locs["1J"] != "GDS001" {
		t.Errorf("the requester's locator must be echoed or they cannot file the reply: %v", locs)
	}
}

// A segment the responder did not decide gets NO, not silence.
func TestBuildReplyDefaultsToNoAction(t *testing.T) {
	req := Parse("BA0175Y15JUNLHRJFKNN1")
	out := BuildReply(req, map[string]ActionCode{}, &pnr.PNR{}, "BA")
	if got := Parse(out).Segments()[0].Action; got != "NO" {
		t.Errorf("action = %q, want NO", got)
	}
}
