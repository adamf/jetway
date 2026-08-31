package padis

import (
	"strings"
	"testing"
	"time"

	"github.com/adamf/jetway/pkg/edifact"
	"github.com/adamf/jetway/pkg/pnr"
)

func ref() time.Time { return time.Date(2026, time.June, 1, 12, 0, 0, 0, time.UTC) }

func samplePNR() *pnr.PNR {
	d, _ := pnr.ResolveDate("15JUN", ref())
	return &pnr.PNR{
		RecordLocator: "ABC23D",
		Origin:        pnr.Origin{Party: "1A", Agent: "MUC12345"},
		Passengers: []pnr.Passenger{
			{Ref: 1, Surname: "SMITH", Given: "JOHN", Title: "MR"},
			{Ref: 2, Surname: "SMITH", Given: "ANNE", Title: "MRS"},
		},
		Segments: []pnr.Segment{{
			Ref: 1, Type: pnr.SegmentAir, Carrier: "BA", FlightNum: "0175", Class: "Y",
			Depart: d, WireDate: "15JUN", DepartTime: "0800", ArriveTime: "1100",
			Board: "LHR", Off: "JFK", Status: "HN", Seats: 2,
		}},
		SSRs:     []pnr.SSR{{Code: "VGML", Status: "NN", Count: 1, Carrier: "BA"}},
		Contacts: []pnr.Contact{{Type: "phone", Text: "LON 44 20 7777 7777"}},
	}
}

func buildOpts() BuildOptions {
	return BuildOptions{
		Sender:     edifact.Party{ID: "1A", Qualifier: "ZZ"},
		Recipient:  edifact.Party{ID: "BA", Qualifier: "ZZ"},
		ControlRef: "000000001", MessageRef: "1", Now: ref(),
	}
}

func TestBuildPAOREQIsWellFormed(t *testing.T) {
	ic, err := BuildPAOREQ(samplePNR(), "BA", buildOpts())
	if err != nil {
		t.Fatalf("BuildPAOREQ: %v", err)
	}
	out, err := ic.Encode(edifact.EncodeOptions{Charset: edifact.CharsetUNOA})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	// The generated interchange must survive strict validation: this is what
	// stops us shipping a miscounted UNT to a partner.
	back, err := edifact.Parse(out, edifact.ParseOptions{Strict: true})
	if err != nil {
		t.Fatalf("generated PAOREQ failed strict parse: %v\n%s", err, out)
	}
	if len(back.Diagnostics) != 0 {
		t.Errorf("diagnostics on generated message: %v\n%s", back.Diagnostics, out)
	}
	if got := back.Messages[0].ID().Type; got != MsgPAOREQ {
		t.Errorf("message type = %q", got)
	}
	if !strings.Contains(string(out), "TVL+150626:0800:150626:1100+LHR+JFK+BA+0175:Y'") {
		t.Errorf("TVL segment not as expected:\n%s", out)
	}
}

func TestPAOREQRoundTripsToEquivalentRecord(t *testing.T) {
	orig := samplePNR()
	ic, err := BuildPAOREQ(orig, "BA", buildOpts())
	if err != nil {
		t.Fatalf("BuildPAOREQ: %v", err)
	}
	out, _ := ic.Encode(edifact.EncodeOptions{})
	back, err := edifact.Parse(out, edifact.ParseOptions{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	got := &pnr.PNR{}
	changes := Apply(got, back.Messages[0], ApplyOptions{ReceivedAt: ref(), Party: "1A", Inbound: true})
	if len(changes) == 0 {
		t.Fatal("Apply made no changes")
	}
	for _, f := range got.Unparsed {
		t.Errorf("unparsed fragment on our own generated message: %+v", f)
	}

	if len(got.Passengers) != 2 {
		t.Fatalf("passengers = %d, want 2: %+v", len(got.Passengers), got.Passengers)
	}
	if got.Passengers[0].Surname != "SMITH" || got.Passengers[0].Given != "JOHN" || got.Passengers[0].Title != "MR" {
		t.Errorf("passenger 1 = %+v", got.Passengers[0])
	}
	if got.Passengers[1].Given != "ANNE" {
		t.Errorf("passenger 2 = %+v", got.Passengers[1])
	}
	if len(got.Segments) != 1 {
		t.Fatalf("segments = %d, want 1", len(got.Segments))
	}
	s := got.Segments[0]
	if s.Carrier != "BA" || s.FlightNum != "0175" || s.Class != "Y" ||
		s.Board != "LHR" || s.Off != "JFK" || s.Seats != 2 {
		t.Errorf("segment = %+v", s)
	}
	if !s.Depart.Equal(orig.Segments[0].Depart) {
		t.Errorf("departure date drifted: %s vs %s", s.Depart, orig.Segments[0].Depart)
	}
	if s.Key() != orig.Segments[0].Key() {
		t.Errorf("segment key drifted: %q vs %q", s.Key(), orig.Segments[0].Key())
	}
	if len(got.SSRs) != 1 || got.SSRs[0].Code != "VGML" {
		t.Errorf("ssrs = %+v", got.SSRs)
	}
	if len(got.Contacts) != 1 {
		t.Errorf("contacts = %+v", got.Contacts)
	}
	if loc, ok := got.LocatorFor("1A"); !ok || loc != "ABC23D" {
		t.Errorf("locator = %q,%v", loc, ok)
	}
}

func TestPAORESConfirmationSettlesSegment(t *testing.T) {
	rec := samplePNR()
	ic, err := BuildPAORES(edifact.Message{}, rec,
		map[string]string{rec.Segments[0].Key(): "KK"}, "BA1234", "BA", buildOpts())
	if err != nil {
		t.Fatalf("BuildPAORES: %v", err)
	}
	out, _ := ic.Encode(edifact.EncodeOptions{})
	back, _ := edifact.Parse(out, edifact.ParseOptions{Strict: true})

	// Apply the response onto the requester's copy of the record.
	req := samplePNR()
	Apply(req, back.Messages[0], ApplyOptions{ReceivedAt: ref(), Party: "BA"})
	if req.Segments[0].Status != "HK" {
		t.Errorf("status = %q, want HK after a KK response", req.Segments[0].Status)
	}
	if loc, ok := req.LocatorFor("BA"); !ok || loc != "BA1234" {
		t.Errorf("carrier locator = %q,%v want BA1234", loc, ok)
	}
	if req.AwaitingReply() {
		t.Error("record should no longer be awaiting a reply")
	}
}

func TestPAORESRefusalMarksSegment(t *testing.T) {
	rec := samplePNR()
	ic, _ := BuildPAORES(edifact.Message{}, rec,
		map[string]string{rec.Segments[0].Key(): "UC"}, "", "BA", buildOpts())
	out, _ := ic.Encode(edifact.EncodeOptions{})
	back, _ := edifact.Parse(out, edifact.ParseOptions{})

	req := samplePNR()
	Apply(req, back.Messages[0], ApplyOptions{ReceivedAt: ref(), Party: "BA"})
	if req.Segments[0].Status != "UC" {
		t.Errorf("status = %q, want UC", req.Segments[0].Status)
	}
	if req.Status != pnr.StatusCancelled {
		t.Errorf("record status = %q, want cancelled once the only segment is refused", req.Status)
	}
}

func TestWaitlistResponse(t *testing.T) {
	rec := samplePNR()
	ic, _ := BuildPAORES(edifact.Message{}, rec,
		map[string]string{rec.Segments[0].Key(): "US"}, "BA9999", "BA", buildOpts())
	out, _ := ic.Encode(edifact.EncodeOptions{})
	back, _ := edifact.Parse(out, edifact.ParseOptions{})
	req := samplePNR()
	Apply(req, back.Messages[0], ApplyOptions{ReceivedAt: ref(), Party: "BA"})
	if req.Segments[0].Status != "HL" {
		t.Errorf("status = %q, want HL after US", req.Segments[0].Status)
	}
}

// An unknown segment must land on the record as a fragment, never be dropped.
func TestUnknownSegmentBecomesFragment(t *testing.T) {
	in := "UNB+UNOA:3+BA:ZZ+1A:ZZ+260601:1200+1'UNH+1+PAORES:96:1:IA'" +
		"MSG+:22'ZZZ+PROPRIETARY:PAYLOAD'UNT+3+1'UNZ+1+1'"
	ic, err := edifact.Parse([]byte(in), edifact.ParseOptions{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	p := &pnr.PNR{}
	Apply(p, ic.Messages[0], ApplyOptions{ReceivedAt: ref()})
	if len(p.Unparsed) != 1 {
		t.Fatalf("unparsed = %d, want 1: %+v", len(p.Unparsed), p.Unparsed)
	}
	if !strings.Contains(p.Unparsed[0].Raw, "PROPRIETARY") {
		t.Errorf("fragment did not keep the raw segment: %+v", p.Unparsed[0])
	}
}

func TestTVLAcceptsTeletypeDateForm(t *testing.T) {
	in := "UNB+UNOA:3+BA:ZZ+1A:ZZ+260601:1200+1'UNH+1+PAOREQ:96:1:IA'" +
		"TVL+15JUN::+LHR+JFK+BA+0175:Y'UNT+3+1'UNZ+1+1'"
	ic, _ := edifact.Parse([]byte(in), edifact.ParseOptions{})
	p := &pnr.PNR{}
	Apply(p, ic.Messages[0], ApplyOptions{ReceivedAt: ref()})
	if len(p.Segments) != 1 {
		t.Fatalf("segments = %d, unparsed %+v", len(p.Segments), p.Unparsed)
	}
	if p.Segments[0].WireDate != "15JUN" {
		t.Errorf("wire date = %q", p.Segments[0].WireDate)
	}
}

// The cases below check segment composition against the forms documented in
// IATA's publicly published PNRGOV EDIFACT Implementation Guide. The data is
// substituted -- what is being asserted is the shape, not the document.

func applyTVL(t *testing.T, wire string) *pnr.PNR {
	t.Helper()
	in := "UNB+UNOA:3+BA:ZZ+1J:ZZ+260601:1200+1'UNH+1+PAORES:96:1:IA'" + wire + "UNT+3+1'UNZ+1+1'"
	ic, err := edifact.Parse([]byte(in), edifact.ParseOptions{})
	if err != nil {
		t.Fatalf("Parse %q: %v", wire, err)
	}
	p := &pnr.PNR{}
	Apply(p, ic.Messages[0], ApplyOptions{ReceivedAt: ref()})
	return p
}

// Element 3 is a composite: marketing carrier, then operating carrier. Reading
// only the first loses who actually holds the inventory, and interline
// messages are addressed to the operating carrier.
func TestTVLCarriesOperatingCarrier(t *testing.T) {
	p := applyTVL(t, "TVL+010410:2235:020410:1200+ATL+LHR+DL:KL+10:K'")
	if len(p.Segments) != 1 {
		t.Fatalf("segments = %d, unparsed %+v", len(p.Segments), p.Unparsed)
	}
	s := p.Segments[0]
	if s.Carrier != "DL" {
		t.Errorf("marketing carrier = %q, want DL", s.Carrier)
	}
	if s.OperatingCarrier != "KL" {
		t.Errorf("operating carrier = %q, want KL", s.OperatingCarrier)
	}
	if s.FlightNum != "10" || s.Class != "K" {
		t.Errorf("flight/class = %q/%q", s.FlightNum, s.Class)
	}
	if s.DepartTime != "2235" || s.ArriveTime != "1200" {
		t.Errorf("times = %q/%q", s.DepartTime, s.ArriveTime)
	}
	// DDMMYY: 010410 is 1 April 2010, not 4 January.
	if s.Depart.Day() != 1 || s.Depart.Month() != time.April {
		t.Errorf("departure = %s, want 1 April", s.Depart.Format("2006-01-02"))
	}
}

func TestTVLWithoutOperatingCarrier(t *testing.T) {
	p := applyTVL(t, "TVL+121210:0915::1230+LHR+JFK+DL+324:B'")
	if len(p.Segments) != 1 {
		t.Fatalf("segments = %d, unparsed %+v", len(p.Segments), p.Unparsed)
	}
	s := p.Segments[0]
	if s.Carrier != "DL" || s.OperatingCarrier != "" {
		t.Errorf("carriers = %q/%q", s.Carrier, s.OperatingCarrier)
	}
	// An absent arrival date is legal; the arrival time still applies.
	if s.ArriveTime != "1230" {
		t.Errorf("arrival time = %q, want 1230", s.ArriveTime)
	}
}

// Date, board and off point are conditional for ARNK and OPEN. Requiring them
// turned every surface gap into an unparsed fragment and broke the itinerary's
// continuity.
func TestTVLSurfaceGap(t *testing.T) {
	p := applyTVL(t, "TVL+++++ARNK'")
	if len(p.Unparsed) != 0 {
		t.Errorf("ARNK must be understood, not retained as a fragment: %+v", p.Unparsed)
	}
	if len(p.Segments) != 1 {
		t.Fatalf("segments = %d", len(p.Segments))
	}
	if p.Segments[0].Type != pnr.SegmentSurface {
		t.Errorf("type = %q, want surface", p.Segments[0].Type)
	}
	if got := p.Segments[0].Describe(); got != "ARNK" {
		t.Errorf("Describe = %q", got)
	}
}

func TestTVLOpenDatedSegment(t *testing.T) {
	p := applyTVL(t, "TVL++LHR+ORD++OPEN'")
	if len(p.Unparsed) != 0 {
		t.Errorf("OPEN must be understood: %+v", p.Unparsed)
	}
	if len(p.Segments) != 1 {
		t.Fatalf("segments = %d", len(p.Segments))
	}
	s := p.Segments[0]
	if s.Board != "LHR" || s.Off != "ORD" {
		t.Errorf("points = %q-%q", s.Board, s.Off)
	}
	if !s.Depart.IsZero() {
		t.Errorf("an open segment has no departure date, got %s", s.Depart)
	}
}

func applyTIF(t *testing.T, wire string) *pnr.PNR {
	t.Helper()
	return applyTVL(t, wire)
}

// The second component is the traveller type, not a title. Reading it as a
// title turned every adult into someone called "A".
func TestTIFTravellerTypeIsNotATitle(t *testing.T) {
	p := applyTIF(t, "TIF+JONES+JOHNMR:A'")
	if len(p.Passengers) != 1 {
		t.Fatalf("passengers = %d, unparsed %+v", len(p.Passengers), p.Unparsed)
	}
	x := p.Passengers[0]
	if x.Surname != "JONES" || x.Given != "JOHN" || x.Title != "MR" {
		t.Errorf("name split wrong: %+v", x)
	}
	if x.Type != pnr.PaxAdult {
		t.Errorf("type = %q, want A", x.Type)
	}
	if x.Infant {
		t.Error("an adult must not be flagged as an infant")
	}
}

func TestTIFInfant(t *testing.T) {
	p := applyTIF(t, "TIF+RUITER+MISTY:IN'")
	if len(p.Passengers) != 1 {
		t.Fatalf("passengers = %d", len(p.Passengers))
	}
	x := p.Passengers[0]
	if x.Type != pnr.PaxInfant || !x.Infant {
		t.Errorf("infant not recognised: %+v", x)
	}
	// MISTY must not lose a "MS" from the end of a name that is not a title.
	if x.Given != "MISTY" {
		t.Errorf("given = %q, want MISTY", x.Given)
	}
}

func TestTIFTravellerReference(t *testing.T) {
	p := applyTIF(t, "TIF+SMITHJR+JOHNMR:A:1'")
	if len(p.Passengers) != 1 {
		t.Fatalf("passengers = %d", len(p.Passengers))
	}
	if p.Passengers[0].Surname != "SMITHJR" || p.Passengers[0].Given != "JOHN" {
		t.Errorf("%+v", p.Passengers[0])
	}
}

func TestTIFGroup(t *testing.T) {
	p := applyTIF(t, "TIF+SEETHE WORLD:G'")
	if len(p.Passengers) != 1 {
		t.Fatalf("passengers = %d", len(p.Passengers))
	}
	if p.Passengers[0].Type != pnr.PaxGroup {
		t.Errorf("type = %q, want G; on a group the type sits on element 0", p.Passengers[0].Type)
	}
}

// RCI repeats: each element is one party's locator.
func TestRCIRepeatingLocators(t *testing.T) {
	p := applyTVL(t, "RCI+SK:123EF+1G:345ABC+XX:7890:C'")
	want := map[string]string{"SK": "123EF", "1G": "345ABC", "XX": "7890"}
	for owner, loc := range want {
		if got, ok := p.LocatorFor(owner); !ok || got != loc {
			t.Errorf("locator %s = %q,%v; want %q", owner, got, ok, loc)
		}
	}
}

// What we build must decode back to what we meant, including the corrected
// composites.
func TestBuiltSegmentsRoundTripThroughTheCorrectedMapping(t *testing.T) {
	rec := samplePNR()
	rec.Segments[0].OperatingCarrier = "KL"
	rec.Passengers[0].Type = pnr.PaxAdult
	rec.Passengers[1].Type = pnr.PaxAdult
	rec.Segments = append(rec.Segments, pnr.Segment{
		Ref: 2, Type: pnr.SegmentSurface, Board: "JFK", Off: "BOS", Status: "HK",
	})

	ic, err := BuildPAOREQ(rec, "BA", buildOpts())
	if err != nil {
		t.Fatalf("BuildPAOREQ: %v", err)
	}
	out, _ := ic.Encode(edifact.EncodeOptions{})
	back, err := edifact.Parse(out, edifact.ParseOptions{Strict: true})
	if err != nil {
		t.Fatalf("re-parse: %v\n%s", err, out)
	}
	got := &pnr.PNR{}
	Apply(got, back.Messages[0], ApplyOptions{ReceivedAt: ref(), Party: "1A", Inbound: true})
	for _, f := range got.Unparsed {
		t.Errorf("unparsed fragment on our own message: %+v", f)
	}
	if len(got.Passengers) != 2 {
		t.Fatalf("passengers = %d: %+v", len(got.Passengers), got.Passengers)
	}
	if got.Passengers[0].Given != "JOHN" || got.Passengers[0].Title != "MR" {
		t.Errorf("name did not survive: %+v", got.Passengers[0])
	}
	if got.Segments[0].OperatingCarrier != "KL" {
		t.Errorf("operating carrier did not survive: %+v", got.Segments[0])
	}
}

// A cancellation is an advisory: the sender has already cancelled, and nothing
// in the message asks the recipient to decide anything. Stamping it with the
// request function code once made carriers answer cancels as if they were
// sells -- refusing the already-cancelled segments with NO.
func TestBuildCancelIsAnAdvisoryNotARequest(t *testing.T) {
	ic, err := BuildCancel(samplePNR(), "BA", nil, buildOpts())
	if err != nil {
		t.Fatalf("BuildCancel: %v", err)
	}
	if got := MessageFunction(ic.Messages[0]); got != FuncCancellation {
		t.Errorf("cancel message function = %q, want %q", got, FuncCancellation)
	}
}

func TestMessageFunctionReadsTheMSGSegment(t *testing.T) {
	ic, err := BuildPAOREQ(samplePNR(), "BA", buildOpts())
	if err != nil {
		t.Fatalf("BuildPAOREQ: %v", err)
	}
	if got := MessageFunction(ic.Messages[0]); got != FuncRequest {
		t.Errorf("request message function = %q, want %q", got, FuncRequest)
	}
}
