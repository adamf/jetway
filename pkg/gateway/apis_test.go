package gateway

import (
	"strings"
	"testing"
	"time"

	"github.com/adamf/jetway/pkg/dcs"
	"github.com/adamf/jetway/pkg/edifact"
	"github.com/adamf/jetway/pkg/paxlst"
)

// The flight-close list: boarded passengers only, names without their
// titles, the document from the record's DOCS where there is one and "not
// verified" where there is not, seats, bags and tags, the flight's sector.
func TestAPISForBuildsTheFlightCloseListFromTheManifest(t *testing.T) {
	fl := &dcs.Flight{Flight: "BA0117", Date: "26NOV", Board: "LHR", Dest: "JFK", Passengers: []*dcs.Passenger{
		{ID: 1, Surname: "SMITH", Given: "JANEMRS", Locator: "ABC123", Status: dcs.StatusBoarded, Seat: "14C", Sequence: 57, Dest: "JFK",
			SSRs: []dcs.SSR{{Code: "DOCS", Text: "P/GBR/P123456/GBR/14MAY80/F/31JAN30/SMITH/JANE"}},
			Bags: []dcs.Bag{{Tag: "0125123456", Weight: 18, Loaded: true}, {Tag: "0125123457", Weight: 13, Loaded: true}}},
		{ID: 2, Surname: "JONES", Given: "TOMMR", Locator: "DEF456", Status: dcs.StatusBoarded, Seat: "22A", Sequence: 58, Dest: "BOS"},
		{ID: 3, Surname: "NOSHOW", Given: "ANNMS", Locator: "GHI789", Status: dcs.StatusAccepted, Seat: "30B", Sequence: 59},
	}}
	m := APISFor(fl, APISOptions{Departs: time.Date(2025, 11, 26, 8, 30, 0, 0, time.UTC), Arrives: time.Date(2025, 11, 26, 11, 20, 0, 0, time.UTC),
		Function: paxlst.FuncCloseOnBoard, TxnRef: "BA0117-26NOV", ContactSurname: "OPS", ContactGiven: "LHR", OnBoardOnly: true})
	if len(m.Legs) != 1 || m.Legs[0].Carrier != "BA" || m.Legs[0].Number != "0117" || m.Legs[0].From != "LHR" || m.Legs[0].To != "JFK" {
		t.Fatalf("leg: %+v", m.Legs)
	}
	if len(m.People) != 2 || m.Total != 2 {
		t.Fatalf("boarded only: %+v", m.People)
	}
	jane, tom := m.People[0], m.People[1]
	if jane.Given != "JANE" || jane.Gender != "F" || jane.Nationality != "GBR" || len(jane.Documents) != 1 || jane.Documents[0].Number != "P123456" || jane.Verified == nil || !*jane.Verified {
		t.Fatalf("jane from her DOCS: %+v", jane)
	}
	if jane.Seat != "14C" || jane.Bags != 2 || jane.BagWeightKg != 31 || len(jane.BagTags) != 2 || jane.PassengerRef != "BA0117057" || jane.Locator != "ABC123" {
		t.Fatalf("jane's seat and bags: %+v", jane)
	}
	if tom.Given != "TOM" || tom.Verified == nil || *tom.Verified || len(tom.Documents) != 0 || tom.Destination != "BOS" || tom.Clearance != "JFK" {
		t.Fatalf("tom without a document: %+v", tom)
	}
	// And it goes on the wire as the guide frames it.
	ic, err := paxlst.Build(m, paxlst.BuildOptions{Sender: edifact.Party{ID: "BA"}, Recipient: edifact.Party{ID: "USCBP"}, ControlRef: "1", Group: true,
		Now: time.Date(2025, 11, 26, 8, 40, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := ic.Encode(edifact.EncodeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"BGM+745+CLOB'", "TDT+20+BA0117+++BA'", "NAD+FL+++SMITH:JANE'", "DOC+P+P123456'", "GEI+4+173'", "NAD+FL+++JONES:TOM'", "GEI+4+174'", "CNT+42:2'"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("missing %q:\n%s", want, raw)
		}
	}
}
