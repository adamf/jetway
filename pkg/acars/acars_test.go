package acars

import (
	"strings"
	"testing"
)

// OAG's "ACARS: Data Elements and Message Examples", verbatim bodies (the
// Type B address and origin lines are the gateway's business).
func TestParseOAGExamples(t *testing.T) {
	m, err := Parse("DEP\nFI JA401/AN CC-AWE/DA SPJC/DS SCEL/OT 0030/FB /BF\nDT DDL LIM 010030 M17A")
	if err != nil {
		t.Fatal(err)
	}
	if m.Kind != KindDEP || m.Flight != "JA401" || m.Registration != "CC-AWE" || m.Departure != "SPJC" || m.Destination != "SCEL" || m.Out != "0030" {
		t.Errorf("OUT report: %+v", m)
	}
	if m.FuelOnBoard != "" || m.BoardedFuel != "" {
		t.Errorf("empty fuel elements must stay empty: %q %q", m.FuelOnBoard, m.BoardedFuel)
	}
	if m.Service == nil || m.Service.Provider != "DDL" || m.Service.Station != "LIM" || m.Service.Time != "010030" || m.Service.Number != "M17A" {
		t.Errorf("DT: %+v", m.Service)
	}

	m, err = Parse("DEP\nFI HX0112/AN B-LPN/DA VHHH/DS ZSHC/OT 0007/FB 153/DC 2347")
	if err != nil {
		t.Fatal(err)
	}
	if m.FuelOnBoard != "153" || len(m.Elements) != 1 || m.Elements[0].ID != "DC" || m.Elements[0].Value != "2347" {
		t.Errorf("unknown element must be kept: %+v %+v", m.FuelOnBoard, m.Elements)
	}
	if m.Service != nil {
		t.Error("no DT line, yet a service block")
	}

	m, err = Parse("DEP\nFI JA304/AN CC-AWG/DA SCCI/DS SCEL/OF 0004\nDT DDL PUQ 310004 M18A")
	if err != nil || m.Off != "0004" || m.Out != "" {
		t.Errorf("OFF report: %+v %v", m, err)
	}
	m, err = Parse("ARR\nFI HX0762/AN B-LGE/DA VTBS/AD VHHH/ON 0028/FB 85")
	if err != nil || m.Kind != KindARR || m.Arrival != "VHHH" || m.On != "0028" || m.FuelOnBoard != "85" {
		t.Errorf("ON report: %+v %v", m, err)
	}
	m, err = Parse("ARR\nFI JA29/AN CC-AWB/DA SCFA/AD SCEL/IN 0000/FB /LA /LR\nDT DDL SCL 310000 M29A")
	if err != nil || m.In != "0000" || len(m.Elements) != 2 {
		t.Errorf("IN report: %+v %v", m, err)
	}
	// The airline-format variant: an FI line, a DT line, then free text.
	m, err = Parse("A80\nFI VV758/AN HK-5273\nDT DDL LIM 310002 M60A\n- 1101 OFFRP 0758/30 SPJC/SPZO HK-5273\n/OUT 2340/OFF 0002/FOB 00785/ETA")
	if err == nil {
		t.Errorf("A80 is a company format, not an OOOI report: %+v", m)
	}
}

func TestBuildRoundTrips(t *testing.T) {
	m := &Message{Kind: KindDEP, Flight: "BA117", Registration: "G-BZHA", Departure: "EGLL", Destination: "KJFK",
		Out: "1207", Off: "1219", FuelOnBoard: "62400", Service: &Service{Provider: "SIT", Station: "LHR", Time: "261219", Number: "M01A"}}
	text, err := Build(m)
	if err != nil {
		t.Fatal(err)
	}
	want := "DEP\nFI BA117/AN G-BZHA/DA EGLL/DS KJFK/OT 1207/OF 1219/FB 62400\nDT SIT LHR 261219 M01A"
	if text != want {
		t.Errorf("built:\n%s\nwant:\n%s", text, want)
	}
	if !IsOOOI(text) || IsOOOI("MVT\nBA117/26.GBZHA.LHR\nAD1207/1219") || IsOOOI("DEP\nnot an FI line") {
		t.Error("IsOOOI wrong")
	}
	back, err := Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	if back.Flight != m.Flight || back.Off != "1219" || back.Service.Number != "M01A" {
		t.Errorf("round trip: %+v", back)
	}
	if strings.Contains(text, "/ON") || strings.Contains(text, "/IN") {
		t.Error("a DEP report must not invent arrival times")
	}
}
