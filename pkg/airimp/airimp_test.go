package airimp

import (
	"strings"
	"testing"
)

func TestParseSellMessage(t *testing.T) {
	text := "SS\n" +
		"BA0175Y15JUNLHRJFKNN1\n" +
		"1SMITH/JOHNMR\n" +
		"SSR VGML BA NN1 LHRJFK0175Y15JUN /-1SMITH/JOHNMR\n" +
		"OSI BA CTCT LON 44-20-7777-7777\n" +
		"RL 1A/ABC123"
	m := Parse(text)

	if m.Identifier != "SS" {
		t.Errorf("Identifier = %q, want SS", m.Identifier)
	}
	if got := m.Intent(); got != IntentSell {
		t.Errorf("Intent = %q, want sell", got)
	}
	if n := len(m.Unknowns()); n != 0 {
		t.Errorf("%d unrecognised lines: %v", n, m.Unknowns())
	}

	segs := m.Segments()
	if len(segs) != 1 {
		t.Fatalf("segments = %d, want 1", len(segs))
	}
	s := segs[0]
	if s.Carrier != "BA" || s.FlightNum != "0175" || s.Class != "Y" ||
		s.Date != "15JUN" || s.Board != "LHR" || s.Off != "JFK" ||
		s.Action != "NN" || s.Seats != 1 {
		t.Errorf("segment decoded wrong: %+v", s)
	}
	if !s.Action.NeedsReply() {
		t.Error("NN must require a reply")
	}

	names := m.Names()
	if len(names) != 1 || names[0].Surname != "SMITH" || names[0].Givens[0] != "JOHNMR" {
		t.Errorf("name decoded wrong: %+v", names)
	}

	ssrs := m.SSRs()
	if len(ssrs) != 1 {
		t.Fatalf("ssrs = %d, want 1", len(ssrs))
	}
	if ssrs[0].Code != "VGML" || ssrs[0].Carrier != "BA" || ssrs[0].Action != "NN" || ssrs[0].Count != 1 {
		t.Errorf("ssr decoded wrong: %+v", ssrs[0])
	}
	if ssrs[0].Itinerary != "LHRJFK0175Y15JUN" {
		t.Errorf("ssr itinerary = %q", ssrs[0].Itinerary)
	}
	if ssrs[0].NameRef != "1SMITH/JOHNMR" {
		t.Errorf("ssr name ref = %q", ssrs[0].NameRef)
	}

	locs := m.Locators()
	if len(locs) != 1 || locs[0].Carrier != "1A" || locs[0].Value != "ABC123" {
		t.Errorf("locator decoded wrong: %+v", locs)
	}
}

func TestSegmentSpacedForm(t *testing.T) {
	m := Parse("BA 0175 Y 15JUN LHR JFK NN1")
	segs := m.Segments()
	if len(segs) != 1 {
		t.Fatalf("spaced segment form must parse: %v", m.Unknowns())
	}
	if segs[0].Off != "JFK" || segs[0].Action != "NN" {
		t.Errorf("spaced segment decoded wrong: %+v", segs[0])
	}
}

// A one-digit flight number followed by a class letter is the case where a
// naive split takes the class as a flight suffix.
func TestSegmentShortFlightNumber(t *testing.T) {
	m := Parse("AA1Y15JUNJFKLHRNN2")
	segs := m.Segments()
	if len(segs) != 1 {
		t.Fatalf("want 1 segment, unknowns: %v", m.Unknowns())
	}
	if segs[0].FlightNum != "1" || segs[0].Class != "Y" || segs[0].Seats != 2 {
		t.Errorf("decoded wrong: %+v", segs[0])
	}
}

func TestSegmentAlphanumericCarrier(t *testing.T) {
	m := Parse("9W0123Y15JUNBOMDELNN1")
	segs := m.Segments()
	if len(segs) != 1 || segs[0].Carrier != "9W" || segs[0].FlightNum != "0123" {
		t.Errorf("alphanumeric designator decoded wrong: %+v, unknowns %v", segs, m.Unknowns())
	}
}

func TestReplyIntentAndStatusMapping(t *testing.T) {
	m := Parse("BA0175Y15JUNLHRJFKKK1\nBA0176Y20JUNJFKLHRUC1")
	if got := m.Intent(); got != IntentReply {
		t.Errorf("Intent = %q, want reply", got)
	}
	segs := m.Segments()
	if !segs[0].Action.Confirmed() {
		t.Error("KK must be confirmed")
	}
	h, ok := ReplyTo(segs[0].Action)
	if !ok || h != "HK" {
		t.Errorf("ReplyTo(KK) = %q,%v want HK,true", h, ok)
	}
	h, ok = ReplyTo(segs[1].Action)
	if !ok || h != "" {
		t.Errorf("ReplyTo(UC) = %q,%v want \"\",true", h, ok)
	}
	if _, ok := ReplyTo("NN"); ok {
		t.Error("ReplyTo must reject a request code")
	}
}

func TestWaitlistReply(t *testing.T) {
	if !ActionCode("US").Waitlisted() {
		t.Error("US must be a waitlist holding")
	}
	h, _ := ReplyTo("US")
	if h != "HL" {
		t.Errorf("ReplyTo(US) = %q, want HL", h)
	}
}

func TestUnknownLinesPreserved(t *testing.T) {
	text := "BA0175Y15JUNLHRJFKNN1\nZZ SOMETHING PROPRIETARY 42\n1SMITH/JOHNMR"
	m := Parse(text)
	u := m.Unknowns()
	if len(u) != 1 {
		t.Fatalf("unknowns = %d, want 1", len(u))
	}
	if u[0].Line != "ZZ SOMETHING PROPRIETARY 42" {
		t.Errorf("unknown line not preserved verbatim: %q", u[0].Line)
	}
	if u[0].LineNo != 2 {
		t.Errorf("LineNo = %d, want 2", u[0].LineNo)
	}
	// The known elements around it must still parse.
	if len(m.Segments()) != 1 || len(m.Names()) != 1 {
		t.Error("an unrecognised line must not derail the rest of the message")
	}
}

func TestSensitiveSSRFlagged(t *testing.T) {
	m := Parse("SSR DOCS BA HK1 /P/GBR/123456789/GBR/01JAN80/M/01JAN30/SMITH/JOHN")
	ssrs := m.SSRs()
	if len(ssrs) != 1 {
		t.Fatalf("want 1 SSR, got %d, unknowns %v", len(ssrs), m.Unknowns())
	}
	if !ssrs[0].Sensitive() {
		t.Error("DOCS must be flagged as personal data")
	}
	if ActionCode("HK").Category() != CatHolding {
		t.Error("HK must classify as a holding")
	}
	m2 := Parse("SSR VGML BA HK1")
	if m2.SSRs()[0].Sensitive() {
		t.Error("VGML is not personal data")
	}
}

func TestElementRoundTrip(t *testing.T) {
	cases := []string{
		"BA0175Y15JUNLHRJFKNN1",
		"1SMITH/JOHNMR",
		"2BROWN/ANNMRS/PETERMSTR",
		"SSR VGML BA HK1 LHRJFK0175Y15JUN",
		"OSI BA CTCT LON 44-20-7777-7777",
		"RL 1A/ABC123",
		"TK OK15JUN/LONBA0100",
		"AP LON 44-20-7777-7777-H",
		"RF SMITH",
		"RM CHECK VISA",
	}
	for _, in := range cases {
		m := Parse(in)
		if len(m.Elements) != 1 {
			t.Errorf("%q: elements = %d", in, len(m.Elements))
			continue
		}
		if got := m.Elements[0].Wire(); got != in {
			t.Errorf("round trip: got %q, want %q", got, in)
		}
	}
}

func TestBuild(t *testing.T) {
	out := Build("SS",
		&Segment{Carrier: "BA", FlightNum: "0175", Class: "Y", Date: "15JUN",
			Board: "LHR", Off: "JFK", Action: "NN", Seats: 1},
		&Name{Count: 1, Surname: "SMITH", Givens: []string{"JOHNMR"}},
	)
	want := "SS\nBA0175Y15JUNLHRJFKNN1\n1SMITH/JOHNMR"
	if out != want {
		t.Errorf("Build:\n got %q\nwant %q", out, want)
	}
	// What we build must parse back to what we meant.
	m := Parse(out)
	if len(m.Segments()) != 1 || len(m.Names()) != 1 || len(m.Unknowns()) != 0 {
		t.Errorf("built message did not reparse cleanly: %v", m.Diagnostics)
	}
}

func TestProfileExtension(t *testing.T) {
	p := Default.Clone("carrier-xx").Prepend(Recognizer{
		Name: "xx-proprietary",
		Match: func(line string) (Element, bool) {
			if strings.HasPrefix(line, "ZZ ") {
				return &Remark{Text: strings.TrimPrefix(line, "ZZ ")}, true
			}
			return nil, false
		},
	})
	m := p.Parse("ZZ SOMETHING PROPRIETARY")
	if len(m.Unknowns()) != 0 {
		t.Errorf("carrier recognizer did not take effect: %v", m.Unknowns())
	}
	// The base profile must be unaffected.
	if len(Parse("ZZ SOMETHING PROPRIETARY").Unknowns()) != 1 {
		t.Error("Clone must not mutate the default profile")
	}
}

func TestSegmentKeyIgnoresStatus(t *testing.T) {
	a := Parse("BA0175Y15JUNLHRJFKNN1").Segments()[0]
	b := Parse("BA0175Y15JUNLHRJFKKK1").Segments()[0]
	if a.Key() != b.Key() {
		t.Error("segment key must match a reply back to its request")
	}
}

func TestIntentOfInfoOnlyMessage(t *testing.T) {
	if got := Parse("OSI BA CTCT LON 123").Intent(); got != IntentUnknown {
		t.Errorf("Intent = %q, want unknown for OSI-only text", got)
	}
	if got := Parse("SSR VGML BA HK1").Intent(); got != IntentInfoOnly {
		t.Errorf("Intent = %q, want info_only", got)
	}
}
