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
