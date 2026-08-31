package pnl

import (
	"strings"
	"testing"
)

// The worked example the free documentation agrees on: a PNL with two
// destination groups, a party of two, locators and service elements.
const samplePNL = `PNL
TP1234/16JUL LIS PART1
-OPO03Y
1ALMEIDA/RUIMR .L/A1B2C3
2COSTA/ANAMRS/TIAGOMSTR .R/CHLD HK1
-FAO02Y
1BRAGA/LUISAMS .R/WCHR HK1
1DUARTE/CARLOSMR .L/X9Y8Z7
ENDPNL`

const sampleADL = `ADL
TP1234/16JUL LIS PART1
-OPO02Y
DEL
1ALMEIDA/RUIMR
ADD
1MOTA/INESMS .R/VGML HK1
ENDADL`

func TestParsePNL(t *testing.T) {
	m, err := Parse(samplePNL)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m.Kind != KindPNL || m.Flight != "TP1234" || m.Date != "16JUL" ||
		m.Board != "LIS" || m.Part != 1 || !m.Final {
		t.Errorf("header parsed wrong: %+v", m)
	}
	if len(m.Groups) != 2 {
		t.Fatalf("groups = %d, want 2", len(m.Groups))
	}
	opo := m.Groups[0]
	if opo.Dest != "OPO" || opo.Count != 3 || opo.Class != "Y" {
		t.Errorf("first group = %+v", opo)
	}
	if len(opo.Names) != 2 {
		t.Fatalf("OPO names = %d, want 2", len(opo.Names))
	}
	if n := opo.Names[0]; n.Party != 1 || n.Surname != "ALMEIDA" ||
		len(n.Givens) != 1 || n.Givens[0] != "RUIMR" ||
		len(n.Elements) != 1 || n.Elements[0] != ".L/A1B2C3" {
		t.Errorf("first name = %+v", n)
	}
	if n := opo.Names[1]; n.Party != 2 || n.Surname != "COSTA" ||
		len(n.Givens) != 2 || n.Elements[0] != ".R/CHLD HK1" {
		t.Errorf("party of two = %+v", n)
	}
}

func TestParseADLSections(t *testing.T) {
	m, err := Parse(sampleADL)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m.Kind != KindADL || len(m.Groups) != 1 {
		t.Fatalf("parsed %+v", m)
	}
	g := m.Groups[0]
	if len(g.Sections) != 2 {
		t.Fatalf("sections = %d, want DEL and ADD", len(g.Sections))
	}
	if g.Sections[0].Change != ChangeDEL || g.Sections[0].Names[0].Surname != "ALMEIDA" {
		t.Errorf("DEL section = %+v", g.Sections[0])
	}
	if g.Sections[1].Change != ChangeADD || g.Sections[1].Names[0].Surname != "MOTA" {
		t.Errorf("ADD section = %+v", g.Sections[1])
	}
}

func TestBuildRoundTrips(t *testing.T) {
	for _, text := range []string{samplePNL, sampleADL} {
		m, err := Parse(text)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		built, err := Build(m)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if built != text {
			t.Errorf("round trip changed the message.\nwant:\n%s\ngot:\n%s", text, built)
		}
	}
}

func TestBuildPartsPartitionsLongLists(t *testing.T) {
	var names []Name
	for i := 0; i < 130; i++ {
		names = append(names, Name{Party: 1, Surname: "PAX" + string(rune('A'+i%26)),
			Givens: []string{"TESTMR"}})
	}
	parts, err := BuildParts(KindPNL, "BA0117", "16DEC", "LHR",
		[]Group{{Dest: "JFK", Class: "Y", Names: names}})
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) < 3 {
		t.Fatalf("130 names fit in %d parts; the 60-line envelope would burst", len(parts))
	}
	total := 0
	for i, p := range parts {
		if lines := strings.Count(p, "\n") + 1; lines > 55 {
			t.Errorf("part %d holds %d lines", i+1, lines)
		}
		m, err := Parse(p)
		if err != nil {
			t.Fatalf("part %d does not parse back: %v", i+1, err)
		}
		if m.Part != i+1 {
			t.Errorf("part %d numbers itself %d", i+1, m.Part)
		}
		wantFinal := i == len(parts)-1
		if m.Final != wantFinal {
			t.Errorf("part %d final = %v, want %v", i+1, m.Final, wantFinal)
		}
		for _, g := range m.Groups {
			total += len(g.Names)
		}
	}
	if total != 130 {
		t.Errorf("%d names survived partitioning, want 130", total)
	}
}

func TestIsNameList(t *testing.T) {
	if !IsNameList(samplePNL) || !IsNameList(sampleADL) {
		t.Error("the samples are not recognised")
	}
	if IsNameList("AVS\nBA0117/16DEC LHRJFK\nY/O2") {
		t.Error("an AVS is not a name list")
	}
}

// Dotted elements carry spaces inside them (.R/VGML HK1), so the split is on
// the dots, not the blanks -- two elements on one name must come apart.
func TestNameWithSeveralElements(t *testing.T) {
	n, err := parseName("1SANTOS/MARIAMRS .R/VGML HK1 .L/QW12ER")
	if err != nil {
		t.Fatal(err)
	}
	if len(n.Elements) != 2 {
		t.Fatalf("elements = %v, want two", n.Elements)
	}
	if n.Elements[0] != ".R/VGML HK1" || n.Elements[1] != ".L/QW12ER" {
		t.Errorf("elements split wrong: %v", n.Elements)
	}
}
