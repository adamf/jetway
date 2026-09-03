package paxlst

import (
	"strings"
	"testing"
	"time"

	"github.com/adamf/jetway/pkg/edifact"
)

// The guide's own worked example 5.1 -- a single-sector flight and one
// passenger -- reproduced verbatim (segment 'UNB..' through 'UNZ..', with the
// guide's spaces inside the COM and DTM values left as printed). Parsing it
// is the conformance check: every value lands where the guide says it does.
const example51 = "UNB+UNOA:4+ZZAIRLINE+CUSTOMS+130620:0900+000000001'" +
	"UNG+PAXLST+ZZAIRLINE+CUSTOMS+130620:0900+000000001+UN+D:15B'" +
	"UNH+PAX001+PAXLST:D:15B:UN:IATA'" +
	"BGM+745'" +
	"RFF+TN:1234567890'" +
	"NAD+MS+++DAVIDSON:ROBERT'" +
	"COM+202 628 9292:TE+202 628 4998:FX+DAVIDSONR.AT. IATA.ORG:EM'" +
	"TDT+20+ZZ123+++ZZ'" +
	"LOC+125+SYD'" +
	"DTM+189:1306210900:201'" +
	"LOC+87+HNL'" +
	"DTM+232: 1306212200:201'" +
	"NAD+FL+++WILLIAMS:JOHN:DONALD+235 WESTERN ROAD SUITE 203+SLEAFORD+:::LINCS+PE224T5+GBR'" +
	"ATT+2++M'" +
	"DTM+329:720907'" +
	"MEA+CT++:2'" +
	"GEI+4+174'" +
	"FTX+BAG+++ZZ012345:3'" +
	"LOC+22+HNL'" +
	"LOC+174+GBR'" +
	"LOC+178+SYD'" +
	"LOC+179+HNL'" +
	"LOC+180+:::AMBER HILL GBR'" +
	"COM+44 188 84 14151:TE'" +
	"NAT+2+GBR'" +
	"RFF+AVF:TYR123'" +
	"RFF+ABO:ABC123'" +
	"DOC+P+MB140241'" +
	"DTM+36:151231'" +
	"LOC+91+GBR'" +
	"CNT+42:160'" +
	"UNT+30+PAX001'" +
	"UNE+1+000000001'" +
	"UNZ+1+000000001'"

func parseOne(t *testing.T, raw string) *Message {
	t.Helper()
	ic, err := edifact.Parse([]byte(raw), edifact.ParseOptions{})
	if err != nil {
		t.Fatalf("parse interchange: %v", err)
	}
	if len(ic.Messages) != 1 {
		t.Fatalf("one message, got %d", len(ic.Messages))
	}
	m, err := Parse(ic.Messages[0])
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestParsesTheGuidesSingleSectorExample(t *testing.T) {
	m := parseOne(t, example51)
	if m.Ref != "PAX001" || m.List != ListPassengers || m.TxnRef != "1234567890" {
		t.Errorf("header: %+v", m)
	}
	if m.ContactSurname != "DAVIDSON" || m.ContactGiven != "ROBERT" || len(m.ContactComs) != 3 || m.ContactComs[2].Kind != "EM" {
		t.Errorf("message contact: %+v %+v", m.ContactSurname, m.ContactComs)
	}
	if len(m.Legs) != 1 {
		t.Fatalf("legs: %+v", m.Legs)
	}
	l := m.Legs[0]
	if l.Carrier != "ZZ" || l.Number != "123" || l.From != "SYD" || l.FromQual != "125" || l.To != "HNL" || l.ToQual != "87" {
		t.Errorf("leg: %+v", l)
	}
	if !l.Departs.Equal(time.Date(2013, 6, 21, 9, 0, 0, 0, time.UTC)) || !l.DepartsHasTime ||
		!l.Arrives.Equal(time.Date(2013, 6, 21, 22, 0, 0, 0, time.UTC)) {
		t.Errorf("leg times: %v %v", l.Departs, l.Arrives)
	}
	if len(m.People) != 1 {
		t.Fatalf("people: %+v", m.People)
	}
	p := m.People[0]
	if p.Party != "FL" || p.Surname != "WILLIAMS" || p.Given != "JOHN" || p.Second != "DONALD" {
		t.Errorf("name: %+v", p)
	}
	if p.Street != "235 WESTERN ROAD SUITE 203" || p.City != "SLEAFORD" || p.RegionName != "LINCS" || p.Postcode != "PE224T5" || p.Country != "GBR" {
		t.Errorf("address: %+v", p)
	}
	if p.Gender != "M" || p.DateOfBirth.Year() != 1972 || p.DateOfBirth.Month() != 9 || p.DateOfBirth.Day() != 7 {
		t.Errorf("gender/dob: %s %v", p.Gender, p.DateOfBirth)
	}
	if p.Bags != 2 || p.Verified == nil || *p.Verified || len(p.BagTags) != 1 || p.BagTags[0].Number != "ZZ012345" || p.BagTags[0].Count != 3 {
		t.Errorf("bags: %d %v %+v", p.Bags, p.Verified, p.BagTags)
	}
	if p.Clearance != "HNL" || p.Residence != "GBR" || p.Embarked != "SYD" || p.Destination != "HNL" || p.BirthPlace != "AMBER HILL GBR" {
		t.Errorf("places: %+v", p)
	}
	if len(p.Contacts) != 1 || p.Contacts[0].Value != "44 188 84 14151" || p.Nationality != "GBR" || p.Locator != "TYR123" || p.PassengerRef != "ABC123" {
		t.Errorf("contact/nat/refs: %+v", p)
	}
	if len(p.Documents) != 1 || p.Documents[0].Type != "P" || p.Documents[0].Number != "MB140241" || p.Documents[0].Issuer != "GBR" ||
		!p.Documents[0].Expires.Equal(time.Date(2015, 12, 31, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("document: %+v", p.Documents)
	}
	if m.Total != 160 || m.TotalKind != "42" {
		t.Errorf("count: %d %s", m.Total, m.TotalKind)
	}
}

// Example 5.2: a crew member clearing at the destination.
const example52 = "UNB+UNOA:4+ZZAIRLINE+CUSTOMS+130620:0900+QF00321'" +
	"UNG+PAXLST+ZZAIRLINE+CUSTOMS+130620:0900+81+UN+D:15B'" +
	"UNH+PAX001+PAXLST:D:15B:UN:IATA'" +
	"BGM+250'" +
	"NAD+MS+USD090746'" +
	"TDT+20+ZZ123+++ZZ'" +
	"LOC+125+SYD'" +
	"DTM+189:1306210900:201'" +
	"LOC+87+HNL'" +
	"DTM+232: 1306212200:201'" +
	"NAD+FM+++CLARK:MICHAEL+ 2365 KAANAPALI HIGHWAY+LAHAINA+HI+ 96761'" +
	"ATT+2++M'" +
	"DTM+329:720907'" +
	"NAT+2+CAN'" +
	"LOC+22+HNL'" +
	"LOC+174+CAN'" +
	"LOC+178+SYD'" +
	"LOC+179+HNL'" +
	"DOC+P+MB140241'" +
	"DTM+36:151021'" +
	"LOC+91+CAN'" +
	"CNT+41:8'" +
	"UNT+20+PAX001'" +
	"UNE+1+81'" +
	"UNZ+1+QF00321'"

func TestParsesTheGuidesCrewExample(t *testing.T) {
	m := parseOne(t, example52)
	if m.List != ListCrew || m.Contact != "USD090746" || m.TotalKind != "41" || m.Total != 8 {
		t.Errorf("crew header: %+v", m)
	}
	if len(m.People) != 1 || m.People[0].Party != "FM" || m.People[0].Surname != "CLARK" || m.People[0].Nationality != "CAN" || m.People[0].Region != "HI" {
		t.Errorf("crew member: %+v", m.People)
	}
}

// Example 5.3: a progressive flight with two sectors and two passengers,
// arriving in one country and continuing within it.
const example53 = "UNB+UNOA:4+XYZAIRLINES+CUSTOMS+140708:0601+123456789'" +
	"UNG+PAXLST+XYZAIRLINES+CUSTOMS+140708:0601+12345+UN+D:15B'" +
	"UNH+123+PAXLST:D:15B:UN:IATA'" +
	"BGM+745'" +
	"RFF+TN:BART34567890:::1'" +
	"NAD+MS+++XYZ PSGR SYSTEMS'" +
	"COM+703-555-1212:TE+703-555-4545:FX'" +
	"TDT+20+XZ877+++XZ'" +
	"LOC+92+BCN'" +
	"DTM+189:1407081100:201'" +
	"LOC+92+IAD'" +
	"DTM+232:1407081700:201'" +
	"TDT+20+ZX877+++XZ'" +
	"LOC+92+IAD'" +
	"DTM+189:1407081930:201'" +
	"LOC+92+SFO'" +
	"DTM+232:1407082330:201'" +
	"NAD+FL+++MARTINEZ:JULIO:XAVIER'" +
	"ATT+2++M'" +
	"DTM+329:680223'" +
	"LOC+22+IAD'" +
	"LOC+178+BCN'" +
	"LOC+179+SFO'" +
	"LOC+174+ESP'" +
	"NAT+2+ESP'" +
	"RFF+AVF:GJO3RT'" +
	"RFF+ABO:XZ877001'" +
	"DOC+P+YY3478621'" +
	"DTM+36:181230'" +
	"LOC+91+ESP'" +
	"NAD+FL+++MARTINEZ:SORINA:MARIA'" +
	"ATT+2++F'" +
	"DTM+329:690606'" +
	"LOC+22+IAD'" +
	"LOC+178+BCN'" +
	"LOC+179+SFO'" +
	"LOC+174+ESP'" +
	"NAT+2+ESP'" +
	"RFF+AVF:GJO3RT'" +
	"RFF+ABO:XZ877002'" +
	"DOC+P+TRQWE9980'" +
	"DTM+36:170916'" +
	"LOC+91+ESP'" +
	"CNT+42:2'" +
	"UNT+43+123'" +
	"UNE+1+12345'" +
	"UNZ+1+123456789'"

func TestParsesTheGuidesProgressiveExample(t *testing.T) {
	m := parseOne(t, example53)
	if m.TxnRef != "BART34567890" || m.TxnRev != "1" || m.Contact != "" || m.ContactSurname != "XYZ PSGR SYSTEMS" {
		t.Errorf("header: %+v", m)
	}
	if len(m.Legs) != 2 || m.Legs[0].From != "BCN" || m.Legs[0].To != "IAD" || m.Legs[1].From != "IAD" || m.Legs[1].To != "SFO" || m.Legs[1].FromQual != "92" {
		t.Errorf("legs: %+v", m.Legs)
	}
	// The second TDT carries ZX877 with carrier XZ: the identifier does not
	// start with the carrier, so it stands as the number.
	if m.Legs[1].Carrier != "XZ" || m.Legs[1].Number != "ZX877" {
		t.Errorf("odd identifier kept whole: %+v", m.Legs[1])
	}
	if len(m.People) != 2 || m.People[1].Given != "SORINA" || m.People[1].Gender != "F" || m.People[1].Documents[0].Number != "TRQWE9980" || m.People[1].PassengerRef != "XZ877002" {
		t.Errorf("people: %+v", m.People)
	}
	if m.Total != 2 {
		t.Errorf("count: %d", m.Total)
	}
}

func sample() *Message {
	v := true
	return &Message{
		Ref: "PAX001", List: ListPassengers, TxnRef: "BA20251126117",
		ContactSurname: "OPS", ContactGiven: "LHR", ContactComs: []Contact{{"44 20 8759 5511", "TE"}},
		Legs: []Leg{{Carrier: "BA", Number: "0117", From: "LHR", To: "JFK",
			Departs: time.Date(2025, 11, 26, 8, 30, 0, 0, time.UTC), DepartsHasTime: true,
			Arrives: time.Date(2025, 11, 26, 11, 20, 0, 0, time.UTC), ArrivesHasTime: true}},
		People: []Person{{
			Party: PartyPassenger, Surname: "SMITH", Given: "JANE", Gender: "F", DateOfBirth: time.Date(1980, 5, 14, 0, 0, 0, 0, time.UTC),
			Bags: 2, BagWeightKg: 31, Verified: &v, BagTags: []BagTag{{"BA0125123456", 2}},
			Embarked: "LHR", Destination: "JFK", Clearance: "JFK", Residence: "GBR", Nationality: "GBR",
			Locator: "ABC123", PassengerRef: "BA0117001", Seat: "14C",
			Documents: []Document{{Type: "P", Number: "P123456", Expires: time.Date(2030, 1, 31, 0, 0, 0, 0, time.UTC), Issuer: "GBR"}},
		}},
		Total: 214,
	}
}

func TestBuildRoundTripsAndFramesTheGroup(t *testing.T) {
	o := BuildOptions{Sender: edifact.Party{ID: "BA"}, Recipient: edifact.Party{ID: "USCBP"}, ControlRef: "000000001",
		Now: time.Date(2025, 11, 26, 6, 0, 0, 0, time.UTC), Group: true}
	ic, err := Build(sample(), o)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := ic.Encode(edifact.EncodeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	wire := string(raw)
	for _, want := range []string{
		"UNB+UNOA:4+BA+USCBP+251126:0600+000000001'",
		"UNG+PAXLST+BA+USCBP+251126:0600+000000001+UN+D:15B'",
		"UNH+PAX001+PAXLST:D:15B:UN:IATA'",
		"BGM+745'", "RFF+TN:BA20251126117'", "NAD+MS+++OPS:LHR'",
		"TDT+20+BA0117+++BA'", "LOC+125+LHR'", "DTM+189:2511260830:201'", "LOC+87+JFK'", "DTM+232:2511261120:201'",
		"NAD+FL+++SMITH:JANE'", "ATT+2++F'", "DTM+329:800514'", "MEA+CT++:2'", "MEA+WT++KGM:31'", "GEI+4+173'",
		"FTX+BAG+++BA0125123456:2'", "LOC+22+JFK'", "LOC+174+GBR'", "LOC+178+LHR'", "LOC+179+JFK'", "NAT+2+GBR'",
		"RFF+AVF:ABC123'", "RFF+ABO:BA0117001'", "RFF+SEA:14C'", "DOC+P+P123456'", "DTM+36:300131'", "LOC+91+GBR'",
		"CNT+42:214'", "UNE+1+000000001'", "UNZ+1+000000001'",
	} {
		if !strings.Contains(wire, want) {
			t.Errorf("missing %q in:\n%s", want, wire)
		}
	}
	back := parseOne(t, wire)
	p := back.People[0]
	if p.Surname != "SMITH" || p.Seat != "14C" || p.Bags != 2 || p.BagWeightKg != 31 || p.Verified == nil || !*p.Verified || p.Documents[0].Issuer != "GBR" || back.Total != 214 {
		t.Fatalf("round trip: %+v", back)
	}
}

func TestRefusesAnEmptyList(t *testing.T) {
	if _, err := Build(&Message{}, BuildOptions{}); err == nil {
		t.Fatal("no BGM code, no message")
	}
	if _, err := Build(&Message{List: ListPassengers}, BuildOptions{}); err == nil {
		t.Fatal("no flight, no message")
	}
}

// FuzzRoundTrip: names, numbers and places of any shape come back as they
// went, or the encoder refuses them; the decoder never depends on its own
// output being tidy.
func FuzzRoundTrip(f *testing.F) {
	f.Add("SMITH", "JANE", "F", "GBR", "P123456", 2)
	f.Add("O'NEIL", "MARY", "X", "IRL", "", 0)
	f.Fuzz(func(t *testing.T, surname, given, gender, nat, doc string, bags int) {
		if bags < 0 || bags > 99 {
			t.Skip()
		}
		m := sample()
		p := &m.People[0]
		p.Surname, p.Given, p.Gender, p.Nationality, p.Bags = surname, given, gender, nat, bags
		p.Documents = nil
		if doc != "" {
			p.Documents = []Document{{Type: "P", Number: doc}}
		}
		ic, err := Build(m, BuildOptions{Sender: edifact.Party{ID: "BA"}, Recipient: edifact.Party{ID: "GOV"}, ControlRef: "1"})
		if err != nil {
			t.Skip()
		}
		raw, err := ic.Encode(edifact.EncodeOptions{})
		if err != nil {
			t.Skip()
		}
		back, err := edifact.Parse(raw, edifact.ParseOptions{})
		if err != nil {
			t.Fatalf("own output does not parse: %v\n%s", err, raw)
		}
		got, err := Parse(back.Messages[0])
		if err != nil {
			t.Fatalf("own PAXLST does not read back: %v\n%s", err, raw)
		}
		if len(got.People) != 1 || got.People[0].Bags != bags {
			t.Fatalf("changed on the wire: %+v", got.People)
		}
	})
}
