package ats

import (
	"strings"
	"testing"
)

// The FAA's ICAO Flight Planning Interface Reference Guide, verbatim.
const faaFPL = `(FPL-UAL1447-IS
-A320/M-SDGIRWZ/S
-KIAD2130
-N0440F360 DCT DAILY J61 HUBBS DCT KEMPR DCT ILM
AR21 CRANS FISEL2
-KFLL0206
-PBN/D1S1 NAV/RNVD1E2A1)`

const faaInternationalFPL = `(FPL-AAL945-IS
-B763/H-SXWJ5E3GDHIRYZ/SB2D1
-KDFW0210
-N0473F330 JPOOL9 BILEE J87 IAH DCT VUH B753 MARTE UB753 BZE
DCT LIB UG436 LIXAS/N0465F370 UG436 TRU UL780 SULNA DCT TOY
UW208 EMBAL BAYOS3
-SCEL0902 SAEZ
-PBN/A1B2D1 NAV/RNVD1E2A1 REG/N396AN EET/MMID0114 SEGU0417
SPIM0455 MOXES0623 SCFZ0655 LIVOR0742 SCEZ0810 SEL/KLPS CODE/A49920)`

func TestParseFAAFlightPlans(t *testing.T) {
	m, err := Parse(faaFPL)
	if err != nil {
		t.Fatal(err)
	}
	if m.Type != TypeFPL || m.AircraftID != "UAL1447" || m.Rules != "I" || m.FlightType != "S" {
		t.Errorf("3/7/8: %+v", m)
	}
	if m.AircraftType != "A320" || m.Wake != "M" || m.Equipment != "SDGIRWZ/S" {
		t.Errorf("9/10: %q %q %q", m.AircraftType, m.Wake, m.Equipment)
	}
	if m.Departure != "KIAD" || m.EOBT != "2130" {
		t.Errorf("13: %q %q", m.Departure, m.EOBT)
	}
	if !strings.HasPrefix(m.Route, "N0440F360 DCT DAILY") || !strings.HasSuffix(m.Route, "CRANS FISEL2") {
		t.Errorf("15: %q", m.Route)
	}
	if m.Destination != "KFLL" || m.EET != "0206" || len(m.Alternates) != 0 {
		t.Errorf("16: %q %q %v", m.Destination, m.EET, m.Alternates)
	}
	if m.OtherValue("PBN") != "D1S1" || m.OtherValue("NAV") != "RNVD1E2A1" {
		t.Errorf("18: %+v", m.Other)
	}

	m, err = Parse(faaInternationalFPL)
	if err != nil {
		t.Fatal(err)
	}
	if m.AircraftType != "B763" || m.Wake != "H" || m.Destination != "SCEL" || m.EET != "0902" {
		t.Errorf("%+v", m)
	}
	if len(m.Alternates) != 1 || m.Alternates[0] != "SAEZ" {
		t.Errorf("alternates %v", m.Alternates)
	}
	if m.OtherValue("REG") != "N396AN" || !strings.HasPrefix(m.OtherValue("EET"), "MMID0114 SEGU0417") || m.OtherValue("CODE") != "A49920" {
		t.Errorf("18: %+v", m.Other)
	}
	if !strings.Contains(m.Route, "LIXAS/N0465F370") {
		t.Errorf("a speed/level change inside the route was lost: %q", m.Route)
	}
}

// The movement messages, as the FAA's acceptance guidance shows them, and
// the ARR form of Doc 4444 Appendix 3.
func TestParseMovementMessages(t *testing.T) {
	m, err := Parse("(DEP-ABC123/A0254-NZAA2347-VTBS-DOF/091120)")
	if err != nil {
		t.Fatal(err)
	}
	if m.Type != TypeDEP || m.AircraftID != "ABC123" || m.SSR != "A0254" || m.Departure != "NZAA" || m.EOBT != "2347" || m.Destination != "VTBS" || m.OtherValue("DOF") != "091120" {
		t.Errorf("DEP %+v", m)
	}
	m, err = Parse("(DLA-ABC123-NZAA2345-VTBS-DOF/091120)")
	if err != nil || m.Type != TypeDLA || m.EOBT != "2345" {
		t.Errorf("DLA %+v %v", m, err)
	}
	m, err = Parse("(CNL-ABC123-NZAA2300-VTBS-DOF/091120)")
	if err != nil || m.Type != TypeCNL || m.Destination != "VTBS" {
		t.Errorf("CNL %+v %v", m, err)
	}
	m, err = Parse("(ARR-CSA406-LHBP-LKPR0913)")
	if err != nil {
		t.Fatal(err)
	}
	if m.Type != TypeARR || m.Departure != "LHBP" || m.Arrival != "LKPR" || m.ArrivalTime != "0913" || m.Destination != "LKPR" {
		t.Errorf("ARR %+v", m)
	}
	// A diverted arrival carries the intended destination before the field.
	m, err = Parse("(ARR-CSA406-LHBP-LKPR-LKTB0940)")
	if err != nil || m.Destination != "LKPR" || m.Arrival != "LKTB" || m.ArrivalTime != "0940" {
		t.Errorf("diverted ARR %+v %v", m, err)
	}
	m, err = Parse("(CHG-BAW117-EGLL1200-KJFK-DOF/251126-13/EGLL1300)")
	if err != nil || m.Type != TypeCHG || len(m.Amendments) != 1 || m.Amendments[0].Key != "13" || m.Amendments[0].Value != "EGLL1300" {
		t.Errorf("CHG %+v %v", m, err)
	}
}

func TestBuildRoundTrips(t *testing.T) {
	fpl := &Message{Type: TypeFPL, AircraftID: "BAW117", Rules: "I", FlightType: "S",
		AircraftType: "B772", Wake: "H", Equipment: "SDE3FGHIRWXY/LB1",
		Departure: "EGLL", EOBT: "1200", Route: "N0480F350 DCT CPT UL9 KENET UN14 STU DCT",
		Destination: "KJFK", EET: "0700", Alternates: []string{"KBOS"},
		Other: []Item{{"DOF", "251126"}, {"REG", "GBZHA"}}}
	text, err := Build(fpl)
	if err != nil {
		t.Fatal(err)
	}
	want := "(FPL-BAW117-IS\n-B772/H-SDE3FGHIRWXY/LB1\n-EGLL1200\n-N0480F350 DCT CPT UL9 KENET UN14 STU DCT\n-KJFK0700 KBOS\n-DOF/251126 REG/GBZHA)"
	if text != want {
		t.Errorf("FPL:\n%s\nwant:\n%s", text, want)
	}
	back, err := Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	if back.AircraftID != "BAW117" || back.Route != fpl.Route || back.Destination != "KJFK" || back.EET != "0700" || back.Alternates[0] != "KBOS" || back.OtherValue("REG") != "GBZHA" {
		t.Errorf("round trip: %+v", back)
	}

	dep := &Message{Type: TypeDEP, AircraftID: "BAW117", Departure: "EGLL", EOBT: "1215", Destination: "KJFK", Other: []Item{{"DOF", "251126"}}}
	text, _ = Build(dep)
	if text != "(DEP-BAW117-EGLL1215-KJFK-DOF/251126)" {
		t.Errorf("DEP %s", text)
	}
	arr := &Message{Type: TypeARR, AircraftID: "BAW117", Departure: "EGLL", Arrival: "KJFK", ArrivalTime: "1905"}
	text, _ = Build(arr)
	if text != "(ARR-BAW117-EGLL-KJFK1905)" {
		t.Errorf("ARR %s", text)
	}
	div := &Message{Type: TypeARR, AircraftID: "BAW117", Departure: "EGLL", Destination: "KJFK", Arrival: "KBOS", ArrivalTime: "1930"}
	text, _ = Build(div)
	if text != "(ARR-BAW117-EGLL-KJFK-KBOS1930)" {
		t.Errorf("diverted ARR %s", text)
	}
	cnl := &Message{Type: TypeCNL, AircraftID: "BAW117", Departure: "EGLL", EOBT: "1200", Destination: "KJFK", Other: []Item{{"DOF", "251126"}}}
	text, _ = Build(cnl)
	if text != "(CNL-BAW117-EGLL1200-KJFK-DOF/251126)" {
		t.Errorf("CNL %s", text)
	}
	for _, text := range []string{"(DEP-BAW117-EGLL1215-KJFK-DOF/251126)", "(ARR-BAW117-EGLL-KJFK-KBOS1930)", faaFPL} {
		if !Looks(text) {
			t.Errorf("Looks rejects %q", text[:12])
		}
	}
	if Looks("MVT\nBA117/26.GBZHA.LHR") {
		t.Error("a movement message was taken for ATS")
	}
}
