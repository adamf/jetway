package padis

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/adamf/jetway/pkg/edifact"
	"github.com/adamf/jetway/pkg/pnr"
)

// guideExample1 is Appendix B example 1 of the IATA PADIS PNRGOV
// implementation guide (release 14.1), verbatim apart from the history LTS
// lines, which carry characters outside the UNOA repertoire the example
// declares. It is the external artefact this parser is checked against.
var guideExample1 = []string{
	"UNB+IATA:1+1A+KRC+130527:0649+0003'",
	"UNH+1+PNRGOV:11:1:IA+270513/0649/SQ/602'",
	"MSG+:22'",
	"ORG+1A:MUC'",
	"TVL+270513:1430:270513:2205+SIN+ICN+SQ+602'",
	"EQN+1'",
	"SRC'",
	"RCI+1A:3PGZOV::190313:1354'",
	"DAT+700:270513:0559'",
	"ORG+1A:MUC+32393340:SINSQ08AA+NCE+SQ:NCE+A+SG+ELPD+CFDE59+9C'",
	"TIF+BELT:I+ISABELLE MRS:A:2:1'",
	"FTI+SQ:8794285757'",
	"IFT+4:63::SQ'",
	"REF+:001C451486DFF0CC'",
	"SSR+DOCS:HK:1:SQ:::::/P/GBR/512731999/GBR/20SEP12/FI/25OCT17/BELT/SOPHY OLIVIA/'",
	"SSR+DOCS:HK:1:SQ:::::/P/GBR/509229987/GBR/01JUL78/F/12NOV22/BELT/ISABELLE RUTH/'",
	"TIF+BELT:I+SOPHY:IN:3'",
	"IFT+4:63::SQ'",
	"TVL+270513:1430:270513:2205+SIN+ICN+SQ+602:D'",
	"RPI+1+HK'",
	"APD+333'",
	"SSR+INFT:HK:1:SQ:::SIN:ICN:BELT/SOPHY 20SEP12+::2'",
	"SSR+DOCS:HK:1:SQ:::SIN:ICN:/P/GBR/512731999/GBR/20SEP12/FI/25OCT17/BELT/SOPHY OLIVIA/+::2'",
	"SSR+DOCS:HK:1:SQ:::SIN:ICN:/P/GBR/509229987/GBR/01JUL78/F/12NOV22/BELT/ISABELLE RUTH/+::2'",
	"RCI+1A:3PGZOV::190313:1354'",
	"DAT'",
	"ORG+SQ++++A'",
	"TRI++SIN-168:::2'",
	"TIF+BELT:I+ISABELLE MRS:A:2'",
	"SSD+011D++++J'",
	"TBD++3:33:700++HP:SIN-168+618:0123456789:2:ICN+618:0123456788:3:ICN+618:0123456787:722356:ICN'",
	"DAT'",
	"ORG+SQ++++A'",
	"TRI++SIN-169:::3'",
	"TIF+BELT:I+SOPHY:IN:3'",
	"SSD+011D++++J'",
	"LTS+0/O/NM/BELT/ISABELLE MRS(ADT)(INF/SOPHY/20SEP12)'",
}

func guideInterchange(t *testing.T) *edifact.Interchange {
	t.Helper()
	// UNT counts every segment of the message including itself and UNH.
	n := len(guideExample1) - 1 + 1
	raw := strings.Join(guideExample1, "") + "UNT+" + strconv.Itoa(n) + "+1'" + "UNZ+1+0003'"
	ic, err := edifact.Parse([]byte(raw), edifact.ParseOptions{Strict: true})
	if err != nil {
		t.Fatalf("parse guide example: %v", err)
	}
	if ic.HasErrors() {
		t.Fatalf("guide example has diagnostics: %+v", ic.Diagnostics)
	}
	if len(ic.Messages) != 1 {
		t.Fatalf("messages: %d", len(ic.Messages))
	}
	return ic
}

func TestParsePNRGOVGuideExample(t *testing.T) {
	ic := guideInterchange(t)
	p, err := ParsePNRGOV(ic.Messages[0])
	if err != nil {
		t.Fatal(err)
	}
	if p.Sender != "1A" || p.Station != "MUC" {
		t.Errorf("sender %q/%q", p.Sender, p.Station)
	}
	fl := p.Flight
	if fl.Carrier != "SQ" || fl.Number != "602" || fl.Board != "SIN" || fl.Off != "ICN" {
		t.Errorf("flight %+v", fl)
	}
	if want := time.Date(2013, 5, 27, 14, 30, 0, 0, time.UTC); !fl.Departs.Equal(want) {
		t.Errorf("departs %v", fl.Departs)
	}
	if want := time.Date(2013, 5, 27, 22, 5, 0, 0, time.UTC); !fl.Arrives.Equal(want) {
		t.Errorf("arrives %v", fl.Arrives)
	}
	if p.Count != 1 || len(p.Records) != 1 {
		t.Fatalf("count %d records %d", p.Count, len(p.Records))
	}
	r := p.Records[0]
	rec := r.PNR
	if rec.RecordLocator != "3PGZOV" || rec.Origin.Party != "1A" || rec.Origin.Agent != "MUC" {
		t.Errorf("record %q origin %+v", rec.RecordLocator, rec.Origin)
	}
	if want := time.Date(2013, 3, 19, 13, 54, 0, 0, time.UTC); !rec.CreatedAt.Equal(want) {
		t.Errorf("created %v", rec.CreatedAt)
	}
	if want := time.Date(2013, 5, 27, 5, 59, 0, 0, time.UTC); !rec.UpdatedAt.Equal(want) {
		t.Errorf("updated %v", rec.UpdatedAt)
	}
	if len(rec.Passengers) != 2 {
		t.Fatalf("passengers %+v", rec.Passengers)
	}
	adult, infant := rec.Passengers[0], rec.Passengers[1]
	if adult.Surname != "BELT" || !strings.HasPrefix(adult.Given, "ISABELLE") || adult.Type != pnr.PaxAdult || adult.Ref != 2 {
		t.Errorf("adult %+v", adult)
	}
	if len(adult.FrequentFlyer) != 1 || adult.FrequentFlyer[0] != "SQ:8794285757" {
		t.Errorf("frequent flyer %v", adult.FrequentFlyer)
	}
	if infant.Surname != "BELT" || infant.Given != "SOPHY" || infant.Type != pnr.PaxInfant || !infant.Infant || infant.Ref != 3 {
		t.Errorf("infant %+v", infant)
	}
	if len(rec.Segments) != 1 {
		t.Fatalf("segments %+v", rec.Segments)
	}
	seg := rec.Segments[0]
	if seg.Carrier != "SQ" || seg.FlightNum != "602" || seg.Class != "D" || seg.Board != "SIN" || seg.Off != "ICN" ||
		seg.DepartTime != "1430" || seg.ArriveTime != "2205" || seg.Status != "HK" || seg.Seats != 1 || seg.WireDate != "27MAY" {
		t.Errorf("segment %+v", seg)
	}
	if !seg.Depart.Equal(time.Date(2013, 5, 27, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("segment date %v", seg.Depart)
	}
	// Two name-level DOCS, then INFT and two DOCS under the flight.
	if len(rec.SSRs) != 5 {
		t.Fatalf("ssrs %d: %+v", len(rec.SSRs), rec.SSRs)
	}
	if s := rec.SSRs[0]; s.Code != "DOCS" || !s.Sensitive || s.PaxRef != 2 || s.SegmentRef != 0 ||
		!strings.HasPrefix(s.Text, "/P/GBR/512731999") {
		t.Errorf("name-level DOCS %+v", s)
	}
	if s := rec.SSRs[2]; s.Code != "INFT" || s.SegmentRef != 1 || s.PaxRef != 2 || s.Text != "BELT/SOPHY 20SEP12" || s.Carrier != "SQ" {
		t.Errorf("INFT %+v", s)
	}
	if len(r.CheckIn) != 2 {
		t.Fatalf("check-in %+v", r.CheckIn)
	}
	ci := r.CheckIn[0]
	if ci.PaxRef != 2 || ci.Station != "SIN" || ci.Sequence != 168 || ci.Seat != "011D" || ci.Cabin != "J" || ci.BagWeightKg != 33 {
		t.Errorf("adult check-in %+v", ci)
	}
	if len(ci.Bags) != 3 || ci.Bags[0].Tag != "0123456789" || ci.Bags[0].Piece != 2 || ci.Bags[0].Destination != "ICN" ||
		ci.Bags[2].Tag != "0123456787" || ci.Bags[2].Piece != 722356 {
		t.Errorf("bags %+v", ci.Bags)
	}
	ci = r.CheckIn[1]
	if ci.PaxRef != 3 || ci.Sequence != 169 || ci.Seat != "011D" || len(ci.Bags) != 0 {
		t.Errorf("infant check-in %+v", ci)
	}
}

// guidePush is the content of the guide's example as this package's types,
// for building it back.
func guidePush() *GovPush {
	docsInf := "/P/GBR/512731999/GBR/20SEP12/FI/25OCT17/BELT/SOPHY OLIVIA/"
	docsAdt := "/P/GBR/509229987/GBR/01JUL78/F/12NOV22/BELT/ISABELLE RUTH/"
	rec := &pnr.PNR{
		RecordLocator: "3PGZOV",
		CreatedAt:     time.Date(2013, 3, 19, 13, 54, 0, 0, time.UTC),
		UpdatedAt:     time.Date(2013, 5, 27, 5, 59, 0, 0, time.UTC),
		Origin:        pnr.Origin{Party: "1A", Agent: "MUC"},
		Passengers: []pnr.Passenger{
			{Ref: 2, Surname: "BELT", Given: "ISABELLE ", Title: "MRS", Type: pnr.PaxAdult, FrequentFlyer: []string{"SQ:8794285757"}},
			{Ref: 3, Surname: "BELT", Given: "SOPHY", Type: pnr.PaxInfant, Infant: true},
		},
		Segments: []pnr.Segment{{Ref: 1, Type: pnr.SegmentAir, Carrier: "SQ", FlightNum: "602", Class: "D",
			Depart: time.Date(2013, 5, 27, 0, 0, 0, 0, time.UTC), DepartTime: "1430", ArriveTime: "2205",
			Board: "SIN", Off: "ICN", Status: "HK", Seats: 1, WireDate: "27MAY"}},
		SSRs: []pnr.SSR{
			{Code: "DOCS", Status: "HK", Count: 1, Carrier: "SQ", PaxRef: 2, Text: docsInf, Sensitive: true},
			{Code: "DOCS", Status: "HK", Count: 1, Carrier: "SQ", PaxRef: 2, Text: docsAdt, Sensitive: true},
			{Code: "INFT", Status: "HK", Count: 1, Carrier: "SQ", SegmentRef: 1, PaxRef: 2, Text: "BELT/SOPHY 20SEP12"},
		},
	}
	return &GovPush{
		Sender: "1A", Station: "MUC",
		Flight: GovFlight{Carrier: "SQ", Number: "602", Board: "SIN", Off: "ICN",
			Departs: time.Date(2013, 5, 27, 14, 30, 0, 0, time.UTC), Arrives: time.Date(2013, 5, 27, 22, 5, 0, 0, time.UTC)},
		Records: []GovRecord{{PNR: rec, CheckIn: []GovCheckIn{
			{PaxRef: 2, Station: "SIN", Sequence: 168, Seat: "011D", Cabin: "J", BagWeightKg: 33, Bags: []GovBag{
				{Tag: "0123456789", Piece: 2, Destination: "ICN"}, {Tag: "0123456788", Piece: 3, Destination: "ICN"}, {Tag: "0123456787", Piece: 722356, Destination: "ICN"}}},
			{PaxRef: 3, Station: "SIN", Sequence: 169, Seat: "011D", Cabin: "J"},
		}}},
	}
}

func TestBuildPNRGOVMatchesGuideSegments(t *testing.T) {
	ic, err := BuildPNRGOV(guidePush(), BuildOptions{
		Sender: edifact.Party{ID: "1A"}, Recipient: edifact.Party{ID: "KRC"}, ControlRef: "0003",
		Now: time.Date(2013, 5, 27, 6, 49, 0, 0, time.UTC), MessageRef: "1",
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := ic.Encode(edifact.EncodeOptions{SegmentPerLine: true, Charset: edifact.CharsetUNOA})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, l := range strings.Split(string(out), "\n") {
		got[strings.TrimSpace(l)] = true
	}
	// Every one of these is a line of the guide's example. The envelope is
	// not compared: the guide's UNB declares the PADIS syntax identifier
	// IATA:1, and this node writes ISO 9735 UNOA:3, which the same states
	// accept and which the CONTRL layer here is built on.
	for _, want := range []string{
		"UNH+1+PNRGOV:11:1:IA'",
		"MSG+:22'",
		"ORG+1A:MUC'",
		"TVL+270513:1430:270513:2205+SIN+ICN+SQ+602'",
		"EQN+1'",
		"SRC'",
		"RCI+1A:3PGZOV::190313:1354'",
		"DAT+700:270513:0559'",
		"TIF+BELT+ISABELLE MRS:A:2'",
		"FTI+SQ:8794285757'",
		"SSR+DOCS:HK:1:SQ:::::" + "/P/GBR/512731999/GBR/20SEP12/FI/25OCT17/BELT/SOPHY OLIVIA/'",
		"TIF+BELT+SOPHY:IN:3'",
		"TVL+270513:1430:270513:2205+SIN+ICN+SQ+602:D'",
		"RPI+1+HK'",
		"SSR+INFT:HK:1:SQ:::SIN:ICN:BELT/SOPHY 20SEP12+::2'",
		"DAT'",
		"ORG+SQ++++A'",
		"TRI++SIN-168:::2'",
		"SSD+011D++++J'",
		"TBD++3:33:700++HP:SIN-168+618:0123456789:2:ICN+618:0123456788:3:ICN+618:0123456787:722356:ICN'",
		"TRI++SIN-169:::3'",
	} {
		if !got[want] {
			t.Errorf("missing %q\nin:\n%s", want, out)
		}
	}
	if !strings.Contains(string(out), "UNZ+1+0003'") {
		t.Errorf("trailer:\n%s", out)
	}
}

func TestPNRGOVRoundTrip(t *testing.T) {
	want := guidePush()
	ic, err := BuildPNRGOV(want, BuildOptions{Sender: edifact.Party{ID: "1A"}, Recipient: edifact.Party{ID: "KRC"}, ControlRef: "1"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := ic.Encode(edifact.EncodeOptions{Charset: edifact.CharsetUNOA})
	if err != nil {
		t.Fatal(err)
	}
	back, err := edifact.Parse(out, edifact.ParseOptions{Strict: true})
	if err != nil || back.HasErrors() {
		t.Fatalf("reparse: %v %+v", err, back.Diagnostics)
	}
	got, err := ParsePNRGOV(back.Messages[0])
	if err != nil {
		t.Fatal(err)
	}
	if got.Count != 1 || len(got.Records) != 1 {
		t.Fatalf("records %d/%d", got.Count, len(got.Records))
	}
	g, w := got.Records[0], want.Records[0]
	if g.PNR.RecordLocator != w.PNR.RecordLocator || len(g.PNR.Passengers) != 2 || len(g.PNR.Segments) != 1 || len(g.PNR.SSRs) != 3 {
		t.Errorf("record %+v", g.PNR)
	}
	if g.PNR.Passengers[1].Type != pnr.PaxInfant || g.PNR.Passengers[0].Title != "MRS" {
		t.Errorf("passengers %+v", g.PNR.Passengers)
	}
	if len(g.CheckIn) != 2 || g.CheckIn[0].Seat != "011D" || g.CheckIn[0].Sequence != 168 || len(g.CheckIn[0].Bags) != 3 || g.CheckIn[1].PaxRef != 3 {
		t.Errorf("check-in %+v", g.CheckIn)
	}
	if !got.Flight.Departs.Equal(want.Flight.Departs) {
		t.Errorf("departs %v", got.Flight.Departs)
	}
}

func TestBuildPNRGOVSkipsRecordsWithoutTravellersOnAFlight(t *testing.T) {
	p := guidePush()
	p.Records = append(p.Records,
		GovRecord{PNR: &pnr.PNR{RecordLocator: "EMPTY1", Segments: p.Records[0].PNR.Segments}},
		GovRecord{PNR: &pnr.PNR{RecordLocator: "NOAIR1", Passengers: p.Records[0].PNR.Passengers}},
	)
	ic, err := BuildPNRGOV(p, BuildOptions{Sender: edifact.Party{ID: "1A"}, Recipient: edifact.Party{ID: "KRC"}, ControlRef: "1"})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := ic.Encode(edifact.EncodeOptions{SegmentPerLine: true, Charset: edifact.CharsetUNOA})
	if !strings.Contains(string(out), "EQN+1'") || strings.Contains(string(out), "EMPTY1") || strings.Contains(string(out), "NOAIR1") {
		t.Errorf("empty records were pushed:\n%s", out)
	}
}

func TestParsePNRGOVRejectsOtherMessages(t *testing.T) {
	ic, _ := BuildPAOREQ(samplePNR(), "BA", BuildOptions{Sender: edifact.Party{ID: "X"}, Recipient: edifact.Party{ID: "BA"}, ControlRef: "1"})
	if _, err := ParsePNRGOV(ic.Messages[0]); err == nil {
		t.Error("a PAOREQ parsed as a PNRGOV")
	}
}

func FuzzPNRGOVRoundTrip(f *testing.F) {
	f.Add("3PGZOV", "BELT", "ISABELLE", "011D", "0123456789")
	f.Add("ABC123", "O BRIEN", "SEAN MR", "12A", "")
	f.Fuzz(func(t *testing.T, locator, surname, given, seat, tag string) {
		p := guidePush()
		rec := p.Records[0].PNR
		rec.RecordLocator = locator
		rec.Passengers[0].Surname, rec.Passengers[0].Given, rec.Passengers[0].Title = surname, given, ""
		p.Records[0].CheckIn[0].Seat = seat
		p.Records[0].CheckIn[0].Bags = []GovBag{{Tag: tag, Piece: 1, Destination: "ICN"}}
		ic, err := BuildPNRGOV(p, BuildOptions{Sender: edifact.Party{ID: "1A"}, Recipient: edifact.Party{ID: "KRC"}, ControlRef: "1"})
		if err != nil {
			return
		}
		out, err := ic.Encode(edifact.EncodeOptions{Charset: edifact.CharsetUNOA})
		if err != nil {
			return
		}
		back, err := edifact.Parse(out, edifact.ParseOptions{})
		if err != nil {
			return
		}
		got, err := ParsePNRGOV(back.Messages[0])
		if err != nil {
			t.Fatalf("own output does not parse: %v", err)
		}
		if len(got.Records) != 1 {
			t.Fatalf("records %d", len(got.Records))
		}
		clean := func(s string) bool {
			return s == strings.TrimSpace(s) && strings.IndexFunc(s, func(r rune) bool {
				return !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == ' ')
			}) < 0 && !strings.Contains(s, "  ")
		}
		if clean(locator) && got.Records[0].PNR.RecordLocator != locator {
			t.Errorf("locator %q became %q", locator, got.Records[0].PNR.RecordLocator)
		}
		if clean(seat) && got.Records[0].CheckIn[0].Seat != seat {
			t.Errorf("seat %q became %q", seat, got.Records[0].CheckIn[0].Seat)
		}
	})
}
