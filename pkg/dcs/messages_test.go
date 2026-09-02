package dcs

import (
	"strings"
	"testing"
)

// The PSM example the practice itself publishes (RP 1715 §3.1.1, as
// reproduced by Amsterdam Schiphol's handling documentation), verbatim.
const psmORYtoGVA = `PSM
AF556/27NOV ORY PART1
-GVA 5PAX/11SSR
BLND 000F 002Y
DEAF 001F 002Y
LANG 000F 001Y
MAAS 001F 002Y
VIP 001F 000Y
WCHR 000F 001Y
F CLASS 2PAX/3SSR
1MILLER/JACKMR 1D
 SR1234Y28ZRH
 DEAF USES AMERICAN SIGN LANGUAGE
1ROBINSON/JULIUSMR 4A
 MAAS
 VIP PRESIDENT OF GEORGIA STATE UNIV
Y CLASS 3PAX/8SSR
1NELSON/MARYDR 12A
 LANG SPEAKS ONLY NORWEGIAN
 WCHR NEEDS ASSISTANCE FOR ALL MOBILE ACTIVITIES
1WILSON/THOMASJOHNMR 22A
 BLND TRAVELLING WITH SERVICE ANIMAL. DOG NAMED VIKTOR.
 DEAF
 MAAS
1WILSON/GRACEANNEMRS 22B
 BLND TRAVELLING WITH SERVICE ANIMAL. MONKEY NAMED SAMMY.
 DEAF ABLE TO HEAR SPEAKER CLOSE TO EAR
 MAAS
-ATH 3PAX/6SSR
BLND 000F 002Y
MAAS 000F 002Y
MEDA 000F 001Y
STCR 000F 001Y
F CLASS NIL
Y CLASS 3PAX/6SSR
1BROWNE/TEDMR 14D
 MAAS TWO BROKEN LEGS. NURSE NELSON. PLEASE TRY TO CLEAR AMBULANCE
       TO MEET AIRCRAFT AIRSIDE
 MEDA
 STCR
1GREEN/MARILYNMRS 28D
SQ1234W28ATHNRT23251800/1HK
 BLND
1GREEN/ROBERTMR 28C
 BLND SEEING EYE DOG NAMED CHARLIE
 MAAS
SI
ATTN HANDLING/PAX COMPLAINTS TO BE EXPECTED AT ARRIVAL DUE DISEMBARKING AIRCRAFT
TWICE AFTER BOARDING BECAUSE OF TEC PROBLEMS/ SEND STAFF TO GATE TO MEET PAX
ENDPSM`

func TestParsePSMPublishedExample(t *testing.T) {
	m, err := ParsePSM(psmORYtoGVA)
	if err != nil {
		t.Fatal(err)
	}
	if m.Flight != "AF556" || m.Date != "27NOV" || m.Board != "ORY" || !m.Final {
		t.Errorf("header: %+v", m)
	}
	if len(m.Groups) != 2 || m.Groups[0].Dest != "GVA" || m.Groups[1].Dest != "ATH" {
		t.Fatalf("groups: %+v", m.Groups)
	}
	gva := m.Groups[0]
	if gva.Pax != 5 || gva.SSRs != 11 || len(gva.Passengers) != 5 {
		t.Errorf("GVA recap %d/%d, %d names", gva.Pax, gva.SSRs, len(gva.Passengers))
	}
	miller := gva.Passengers[0]
	if miller.Surname != "MILLER" || miller.Seat != "1D" || miller.Compartment != "F" {
		t.Errorf("MILLER: %+v", miller)
	}
	if miller.Onward != "SR1234Y28ZRH" {
		t.Errorf("MILLER onward %q", miller.Onward)
	}
	if len(miller.Services) != 1 || miller.Services[0].Code != "DEAF" || miller.Services[0].Text != "USES AMERICAN SIGN LANGUAGE" {
		t.Errorf("MILLER services %+v", miller.Services)
	}
	ath := m.Groups[1]
	browne := ath.Passengers[0]
	if browne.Compartment != "Y" || len(browne.Services) != 3 {
		t.Errorf("BROWNE: %+v", browne)
	}
	if !strings.HasSuffix(browne.Services[0].Text, "TO MEET AIRCRAFT AIRSIDE") {
		t.Errorf("continuation line lost: %q", browne.Services[0].Text)
	}
	if len(m.SI) != 2 || !strings.HasPrefix(m.SI[0], "ATTN HANDLING") {
		t.Errorf("SI %v", m.SI)
	}
}

func TestParsePSMAcceptsDigitsInNames(t *testing.T) {
	m, err := ParsePSM("PSM\nFR5416/02OCT OPO PART1\n-SXB 1PAX/1SSR\nWCHR 001Y\nY CLASS 1PAX/1SSR\n1DEMAND006835/PAX1MR 12A\n WCHR\nENDPSM")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Groups) != 1 || len(m.Groups[0].Passengers) != 1 || m.Groups[0].Passengers[0].Given != "PAX1MR" {
		t.Errorf("%+v", m.Groups)
	}
}

func TestParsePSMNilForms(t *testing.T) {
	m, err := ParsePSM("PSM\nCX123/03DEC HKG PART1\n-NRT NIL\n-YVR NIL\n-YYZ NIL\nSI\nREDUCED MEAL SERVICE DUE LABOR STRIKE MEET FLT WITH VOUCHERS\nENDPSM")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Groups) != 3 || len(m.SI) != 1 {
		t.Errorf("%+v", m)
	}
	m, err = ParsePSM("PSM\nAF556/27NOV ORY PART1\nNIL\nENDPSM")
	if err != nil {
		t.Fatal(err)
	}
	if !m.Nil {
		t.Error("NIL not recorded")
	}
}

func TestBuildPSMFollowsThePublishedShapeAndRoundTrips(t *testing.T) {
	f := &Flight{Key: testKey, Dest: "JFK", Version: "C32Y258"}
	f.Cabin = DefaultFleet().Types["789"].Cabin.instance()
	f.Passengers = []*Passenger{
		{ID: 1, Surname: "NELSON", Given: "MARYDR", Compartment: "Y", Dest: "JFK", Seat: "12A", Status: StatusBoarded,
			SSRs: []SSR{{Code: "WCHR", Text: "NEEDS ASSISTANCE"}, {Code: "VGML"}}},
		{ID: 2, Surname: "MILLER", Given: "JACKMR", Compartment: "C", Dest: "JFK", Seat: "1D", Status: StatusBoarded,
			SSRs: []SSR{{Code: "DEAF"}}, Class: "J",
			Onward: &Connection{Flight: "BA0178", Date: "16DEC", Dest: "BOS", Class: "J"}},
		{ID: 3, Surname: "QUIET", Given: "ANNMS", Compartment: "Y", Dest: "JFK", Seat: "20C", Status: StatusBoarded},
		{ID: 4, Surname: "GONE", Given: "BOBMR", Compartment: "Y", Dest: "JFK", Status: StatusNoShow, SSRs: []SSR{{Code: "WCHR"}}},
	}
	parts := BuildPSM(f)
	if len(parts) != 1 {
		t.Fatalf("%d parts", len(parts))
	}
	want := strings.Join([]string{
		"PSM",
		"BA0117/16DEC LHR PART1",
		"-JFK 2PAX/2SSR",
		"DEAF 001C 000Y",
		"WCHR 000C 001Y",
		"C CLASS 1PAX/1SSR",
		"1MILLER/JACKMR 1D",
		"BA0178J16BOS",
		" DEAF",
		"Y CLASS 1PAX/1SSR",
		"1NELSON/MARYDR 12A",
		" WCHR NEEDS ASSISTANCE",
		"ENDPSM",
	}, "\n")
	if parts[0] != want {
		t.Errorf("PSM built:\n%s\nwant:\n%s", parts[0], want)
	}
	m, err := ParsePSM(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Groups) != 1 || len(m.Groups[0].Passengers) != 2 || m.Groups[0].Passengers[0].Onward != "BA0178J16BOS" {
		t.Errorf("round trip: %+v", m.Groups)
	}
}

func TestParsePTMPublishedLine(t *testing.T) {
	m, err := ParsePTM("PTM\nSK210/14AUG KRSOSL PART1\nDY1256/14 AMS 1V 0B THAWEEKIT/MONNAPA\nSK4780/14 BGO 2Y 3B HANSEN/OLEMR/KARIMRS\nENDPTM")
	if err != nil {
		t.Fatal(err)
	}
	if m.Flight != "SK210" || m.Board != "KRS" || m.Dest != "OSL" || !m.Final {
		t.Errorf("header %+v", m)
	}
	if len(m.Transfers) != 2 {
		t.Fatalf("transfers %+v", m.Transfers)
	}
	tr := m.Transfers[0]
	if tr.Onward.Flight != "DY1256" || tr.Onward.Date != "14" || tr.Onward.Dest != "AMS" || tr.Count != 1 || tr.Onward.Class != "V" || tr.Bags != 0 || tr.Surname != "THAWEEKIT" || tr.Givens[0] != "MONNAPA" {
		t.Errorf("%+v", tr)
	}
	if m.Transfers[1].Count != 2 || m.Transfers[1].Bags != 3 || len(m.Transfers[1].Givens) != 2 {
		t.Errorf("%+v", m.Transfers[1])
	}
}

func TestPFSAndETLRoundTrip(t *testing.T) {
	f := &Flight{Key: testKey, Dest: "JFK"}
	f.Passengers = []*Passenger{
		{ID: 1, Surname: "NG", Given: "MEIMS", Locator: "CCC333", Party: "CCC333/NG", Dest: "JFK", Status: StatusNoShow},
		{ID: 2, Surname: "COSTA", Given: "RUIMR", Locator: "AAA111", Party: "AAA111/COSTA", Dest: "JFK", Status: StatusNoShow},
		{ID: 3, Surname: "COSTA", Given: "ANAMRS", Locator: "AAA111", Party: "AAA111/COSTA", Dest: "JFK", Status: StatusNoShow},
		{ID: 4, Surname: "LATE", Given: "ANNMRS", Locator: "ZZZ999", Party: "GOSHOW/4/LATE", Dest: "JFK", Status: StatusBoarded, Category: CategoryGoShow, Seat: "3A", Ticket: "1259876543210C1"},
		{ID: 5, Surname: "SMITH", Given: "JOHNMR", Locator: "BBB222", Party: "BBB222/SMITH", Dest: "JFK", Status: StatusOffloaded},
	}
	pfs := BuildPFS(f)
	want := strings.Join([]string{
		"PFS", "BA0117/16DEC LHR PART1", "-JFK",
		"NOSHO", "1NG/MEIMS .L/CCC333", "2COSTA/RUIMR/ANAMRS .L/AAA111",
		"GOSHO", "1LATE/ANNMRS .L/ZZZ999",
		"OFFLD", "1SMITH/JOHNMR .L/BBB222",
		"ENDPFS",
	}, "\n")
	if pfs[0] != want {
		t.Errorf("PFS:\n%s\nwant:\n%s", pfs[0], want)
	}
	m, err := ParsePFS(pfs[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Groups) != 1 || len(m.Groups[0].Items) != 4 {
		t.Fatalf("%+v", m.Groups)
	}
	if m.Groups[0].Items[1].Category != "NOSHO" || m.Groups[0].Items[1].Name.Party != 2 {
		t.Errorf("%+v", m.Groups[0].Items[1])
	}
	if m.Groups[0].Items[2].Category != "GOSHO" {
		t.Errorf("%+v", m.Groups[0].Items[2])
	}

	etl := BuildETL(f)
	if len(etl) != 1 || !strings.Contains(etl[0], "1LATE/ANNMRS .L/ZZZ999 .R/TKNE 1259876543210C1 .S/3A") {
		t.Errorf("ETL:\n%s", strings.Join(etl, "\n"))
	}
	e, err := ParseETL(etl[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Groups) != 1 || len(e.Groups[0].Names) != 1 || e.Groups[0].Names[0].Elements[1] != ".R/TKNE 1259876543210C1" {
		t.Errorf("%+v", e.Groups)
	}
}

func TestPaginateRepeatsHeadingsAcrossParts(t *testing.T) {
	f := &Flight{Key: testKey, Dest: "JFK"}
	for i := 0; i < 120; i++ {
		f.Passengers = append(f.Passengers, &Passenger{
			ID: i + 1, Surname: "PAX", Given: "NO" + strings.Repeat("X", i%3) + "MR",
			Party: "P" + string(rune('A'+i%26)) + string(rune('A'+i/26)), Dest: "JFK", Status: StatusNoShow,
		})
	}
	parts := BuildPFS(f)
	if len(parts) < 3 {
		t.Fatalf("120 no-shows fit in %d part(s)", len(parts))
	}
	for i, p := range parts {
		ls := strings.Split(p, "\n")
		if len(ls) > 51 {
			t.Errorf("part %d has %d lines", i+1, len(ls))
		}
		if !strings.Contains(p, "-JFK") || !strings.Contains(p, "NOSHO") {
			t.Errorf("part %d lacks its headings:\n%s", i+1, p)
		}
		if i < len(parts)-1 && !strings.HasSuffix(p, "ENDPART"+string(rune('1'+i))) {
			t.Errorf("part %d ends %q", i+1, ls[len(ls)-1])
		}
	}
	if !strings.HasSuffix(parts[len(parts)-1], "ENDPFS") {
		t.Error("last part has no ENDPFS")
	}
	total := 0
	for _, p := range parts {
		m, err := ParsePFS(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range m.Groups {
			total += len(g.Items)
		}
	}
	if total != 120 {
		t.Errorf("%d items across parts, want 120", total)
	}
}

func TestParseDispatchesOnKind(t *testing.T) {
	for _, text := range []string{
		psmORYtoGVA,
		"PTM\nSK210/14AUG KRSOSL PART1\nDY1256/14 AMS 1V 0B THAWEEKIT/MONNAPA\nENDPTM",
		"LDM\nVY5172/04.ECHQI.A320P.2/05\n-AMS.153/1/2.T1794.3/624.4/1170.PAX/154.PRF/0.DHC/0.B138/1794\nSI NIL",
		"CPM\nRAT0123/02.ECENZ.31904H01\n-11L/PKC/IST/630/C\n-5/IST/50/BY\nSI - TWO BABY-STROLLERS IN CPT 5",
	} {
		if !IsDepartureControl(text) {
			t.Errorf("not recognised: %q", firstLine(text))
		}
		m, err := Parse(text)
		if err != nil {
			t.Errorf("%s: %v", firstLine(text), err)
			continue
		}
		if string(m.Kind) != firstLine(text) || m.Flight == "" {
			t.Errorf("%+v", m)
		}
	}
	if IsDepartureControl("PNL\nBA0117/16DEC LHR PART1\nENDPNL") {
		t.Error("a PNL is not departure control output")
	}
}
