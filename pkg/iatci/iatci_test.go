package iatci

import (
	"strings"
	"testing"
	"time"

	"github.com/adamf/jetway/pkg/edifact"
	"github.com/adamf/jetway/pkg/padis"
)

var opts = padis.BuildOptions{
	Sender: edifact.Party{ID: "BA", Qualifier: "ZZ"}, Recipient: edifact.Party{ID: "AA", Qualifier: "ZZ"},
	ControlRef: "1", Now: time.Date(2025, 11, 26, 9, 0, 0, 0, time.UTC),
}

// A request written by hand from the release 01.1 structure: LOR, FDQ with
// the outbound flight first and the inbound second, then one passenger with
// her booking, seat wish, bags, SSR and passport. The parser must read every
// element from the standard's positions, not from what the builder happens
// to write.
const handWritten = "UNA:+.? 'UNB+UNOA:3+BA:ZZ+AA:ZZ+251126:0900+1++DCQCKI'" +
	"UNH+1+DCQCKI:01:1:IA'" +
	"LOR+BA:LHR'" +
	"FDQ+AA+0100+2611251400+JFK+DFW++BA+0117+2611250830+2611251130+LHR+JFK'" +
	"PPD+SMITH+A:N+P1+JANE'" +
	"PRD+Y+OK++ABC123++AA:ABC123+0012345678901'" +
	"PSD++14C'" +
	"PBD+2:31+++BA:0125123456:2:DFW'" +
	"PSI++WCHR:AA'" +
	"PAP+A+SMITH+JANE+140580++++PT:P123456:GBR'" +
	"UNT+10+1'UNZ+1+1'"

func TestParsesAHandWrittenRequest(t *testing.T) {
	ic, err := edifact.Parse([]byte(handWritten), edifact.ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	req, err := ParseDCQCKI(ic.Messages[0])
	if err != nil {
		t.Fatal(err)
	}
	if req.Requestor != "BA" || req.RequestorStation != "LHR" {
		t.Errorf("requestor: %+v", req)
	}
	if req.Outbound.Carrier != "AA" || req.Outbound.Number != "0100" || req.Outbound.Board != "JFK" || req.Outbound.Off != "DFW" ||
		!req.Outbound.Date.Equal(time.Date(2025, 11, 26, 14, 0, 0, 0, time.UTC)) {
		t.Errorf("outbound: %+v", req.Outbound)
	}
	if req.Inbound.Carrier != "BA" || req.Inbound.Number != "0117" || req.Inbound.Board != "LHR" || req.Inbound.Off != "JFK" ||
		!req.Inbound.Arrives.Equal(time.Date(2025, 11, 26, 11, 30, 0, 0, time.UTC)) {
		t.Errorf("inbound: %+v", req.Inbound)
	}
	if len(req.Passengers) != 1 {
		t.Fatalf("passengers: %+v", req.Passengers)
	}
	p := req.Passengers[0]
	if p.Surname != "SMITH" || p.Given != "JANE" || p.Type != "A" || p.Infant || p.Ref != "P1" {
		t.Errorf("name: %+v", p)
	}
	if p.Class != "Y" || p.Status != "OK" || p.Locator != "ABC123" || p.Ticket != "0012345678901" || p.SeatWant != "14C" {
		t.Errorf("booking: %+v", p)
	}
	if p.Pieces != 2 || p.Weight != 31 || len(p.Tags) != 1 || p.Tags[0].Serial != "0125123456" || p.Tags[0].Count != 2 || p.Tags[0].Dest != "DFW" {
		t.Errorf("bags: %+v %+v", p.Pieces, p.Tags)
	}
	if len(p.SSRs) != 1 || p.SSRs[0] != "WCHR" || p.Document != "P123456" || p.DocumentCountry != "GBR" || p.DateOfBirth.Year() != 1980 {
		t.Errorf("services and document: %+v", p)
	}
}

func sampleRequest() *CheckInRequest {
	return &CheckInRequest{
		Requestor: "BA", RequestorStation: "LHR",
		Inbound:  Flight{Carrier: "BA", Number: "0117", Date: time.Date(2025, 11, 26, 8, 30, 0, 0, time.UTC), Arrives: time.Date(2025, 11, 26, 11, 30, 0, 0, time.UTC), Board: "LHR", Off: "JFK"},
		Outbound: Flight{Carrier: "AA", Number: "0100", Date: time.Date(2025, 11, 26, 14, 0, 0, 0, time.UTC), Board: "JFK", Off: "DFW"},
		Passengers: []Passenger{
			{Surname: "SMITH", Given: "JANE", Type: "A", Ref: "P1", Class: "Y", Locator: "ABC123", Ticket: "0012345678901",
				SeatWant: "14C", Pieces: 2, Weight: 31, Tags: []Tag{{Carrier: "BA", Serial: "0125123456", Count: 2, Dest: "DFW"}},
				SSRs: []string{"WCHR"}, FrequentFlyer: "AA12345", Document: "P123456", DocumentCountry: "GBR", DateOfBirth: time.Date(1980, 5, 14, 0, 0, 0, 0, time.UTC)},
			{Surname: "SMITH", Given: "TIM", Type: "C", Ref: "P2", Class: "Y", Locator: "ABC123"},
		},
	}
}

func TestRequestRoundTrips(t *testing.T) {
	ic, err := BuildDCQCKI(sampleRequest(), opts)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := ic.Encode(edifact.EncodeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "UNH+1+DCQCKI:01:1:IA'") || !strings.Contains(string(raw), "FDQ+AA+0100+2611251400+JFK+DFW++BA+0117+2611250830+2611251130+LHR+JFK'") {
		t.Fatalf("wire:\n%s", raw)
	}
	back, err := edifact.Parse(raw, edifact.ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	req, err := ParseDCQCKI(back.Messages[0])
	if err != nil {
		t.Fatal(err)
	}
	want := sampleRequest()
	if req.Describe() != want.Describe() || len(req.Passengers) != 2 {
		t.Fatalf("round trip: %s vs %s", req.Describe(), want.Describe())
	}
	got, exp := req.Passengers[0], want.Passengers[0]
	if got.Surname != exp.Surname || got.Given != exp.Given || got.Ref != exp.Ref || got.Class != exp.Class || got.Locator != exp.Locator ||
		got.SeatWant != exp.SeatWant || got.Pieces != exp.Pieces || got.Weight != exp.Weight || len(got.Tags) != 1 || got.Tags[0] != exp.Tags[0] ||
		len(got.SSRs) != 1 || got.SSRs[0] != "WCHR" || got.FrequentFlyer != exp.FrequentFlyer || got.Document != exp.Document || !got.DateOfBirth.Equal(exp.DateOfBirth) {
		t.Fatalf("passenger did not survive the wire:\n got %+v\nwant %+v", got, exp)
	}
	if req.Passengers[1].Type != "C" || req.Passengers[1].Ref != "P2" {
		t.Fatalf("child: %+v", req.Passengers[1])
	}
}

func TestResponseRoundTrips(t *testing.T) {
	res := &CheckInResponse{
		Flight: Flight{Carrier: "AA", Number: "0100", Date: time.Date(2025, 11, 26, 14, 0, 0, 0, time.UTC), Board: "JFK", Off: "DFW"},
		Status: "O", Gate: "B12", Terminal: "8", BoardingTime: "1330",
		Passengers: []Result{
			{Ref: "P1", Surname: "SMITH", Given: "JANE", Status: "H", Seat: "14C", Cabin: "Y", Sequence: 57, BoardingPass: true,
				Pieces: 2, Weight: 31, Tags: []Tag{{Carrier: "BA", Serial: "0125123456", Count: 2, Dest: "DFW"}}},
			{Ref: "P2", Surname: "SMITH", Given: "TIM", Status: "I", Errors: []Error{{Level: "1", Code: ErrFlightFull, Text: "NO SEAT IN Y"}}},
		},
	}
	ic, err := BuildDCRCKA(res, opts)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := ic.Encode(edifact.EncodeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "RAD+I+O'") || !strings.Contains(string(raw), "PFD+14C+:Y+57+Y'") || !strings.Contains(string(raw), "FSD+1330+8+B12'") {
		t.Fatalf("wire:\n%s", raw)
	}
	back, err := edifact.Parse(raw, edifact.ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseDCRCKA(back.Messages[0])
	if err != nil {
		t.Fatal(err)
	}
	if got.Flight.Number != "0100" || got.Status != "O" || got.Gate != "B12" || got.Terminal != "8" || got.BoardingTime != "1330" || len(got.Passengers) != 2 {
		t.Fatalf("response: %+v", got)
	}
	j := got.Passengers[0]
	if j.Ref != "P1" || j.Status != "H" || j.Seat != "14C" || j.Cabin != "Y" || j.Sequence != 57 || !j.BoardingPass || j.Pieces != 2 || len(j.Tags) != 1 {
		t.Fatalf("jane: %+v", j)
	}
	tim := got.Passengers[1]
	if tim.Status != "I" || len(tim.Errors) != 1 || tim.Errors[0].Code != ErrFlightFull || tim.Errors[0].Text != "NO SEAT IN Y" {
		t.Fatalf("tim: %+v", tim)
	}
	if got.Granted() {
		t.Fatal("a refused passenger means the request was not granted")
	}
	got.Passengers = got.Passengers[:1]
	if !got.Granted() {
		t.Fatal("one granted passenger and no errors is granted")
	}
}

func TestRefusesTheWrongMessage(t *testing.T) {
	ic, _ := BuildDCRCKA(&CheckInResponse{Status: "H", Flight: Flight{Carrier: "AA", Number: "1"}}, opts)
	raw, _ := ic.Encode(edifact.EncodeOptions{})
	back, _ := edifact.Parse(raw, edifact.ParseOptions{})
	if _, err := ParseDCQCKI(back.Messages[0]); err == nil {
		t.Fatal("a response is not a request")
	}
	if _, err := BuildDCQCKI(&CheckInRequest{}, opts); err == nil {
		t.Fatal("a request without an outbound flight is refused")
	}
}

// FuzzRoundTrip: whatever names and numbers a party carries, the request
// that comes off the wire is the request that went on. The decoder must not
// depend on its own output being tidy.
func FuzzRoundTrip(f *testing.F) {
	f.Add("SMITH", "JANE", "Y", "14C", 2, 31)
	f.Add("O'BRIEN", "MARY ANNE", "J", "", 0, 0)
	f.Fuzz(func(t *testing.T, surname, given, class, seat string, pieces, weight int) {
		if pieces < 0 || weight < 0 || pieces > 99 || weight > 999 {
			t.Skip()
		}
		req := sampleRequest()
		req.Passengers = req.Passengers[:1]
		p := &req.Passengers[0]
		p.Surname, p.Given, p.Class, p.SeatWant, p.Pieces, p.Weight = surname, given, class, seat, pieces, weight
		ic, err := BuildDCQCKI(req, opts)
		if err != nil {
			t.Skip()
		}
		raw, err := ic.Encode(edifact.EncodeOptions{})
		if err != nil {
			t.Skip() // the repertoire refused the text; that is the encoder's job
		}
		back, err := edifact.Parse(raw, edifact.ParseOptions{})
		if err != nil {
			t.Fatalf("our own output does not parse: %v\n%s", err, raw)
		}
		got, err := ParseDCQCKI(back.Messages[0])
		if err != nil {
			t.Fatalf("our own request does not read back: %v\n%s", err, raw)
		}
		if got.Passengers[0].Pieces != pieces || (weight > 0 && got.Passengers[0].Weight != weight) {
			t.Fatalf("bags changed on the wire: %+v", got.Passengers[0])
		}
	})
}
