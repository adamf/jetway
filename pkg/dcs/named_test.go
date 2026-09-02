package dcs

import (
	"context"
	"testing"

	"github.com/adamf/jetway/pkg/pnl"
)

// A family on one name item: the child's CHLD and the infant's INFT belong
// to the passengers named in the element, not to the whole party, and each
// name's ticket is its own. An element naming nobody stays the party's.
func TestElementsNamingAPassengerBelongToThatPassenger(t *testing.T) {
	st := NewStation("WN")
	st.Fleet = DefaultFleet()
	st.Equipment = func(k Key) (Equipment, bool) { return Equipment{Type: "320", Dest: "DCA"}, true }
	m := &pnl.Message{Kind: pnl.KindPNL, Flight: "WN2554", Date: "26NOV", Board: "BNA", Part: 1, Final: true,
		Groups: []pnl.Group{{Dest: "DCA", Class: "Y", Count: 3, Names: []pnl.Name{{
			Party: 3, Surname: "SMITH", Givens: []string{"JOHNMR", "ANNMRS", "TIMMSTR"},
			Elements: []string{".L/ABC123",
				".R/CHLD HK1 SMITH/TIMMSTR",
				".R/WCHR HK1 SMITH/ANNMRS",
				".R/VGML HK3",
				".R/TKNE HK1 526-2000000001C1 SMITH/JOHNMR",
				".R/TKNE HK1 526-2000000002C1 SMITH/ANNMRS",
				".R/TKNE HK1 526-2000000003C1 SMITH/TIMMSTR"},
		}}}}}
	fl, err := st.ApplyNameList(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if len(fl.Passengers) != 3 {
		t.Fatalf("passengers: %d", len(fl.Passengers))
	}
	by := map[string]*Passenger{}
	for _, p := range fl.Passengers {
		by[p.Given] = p
	}
	if by["TIMMSTR"].Type != PaxChild || by["JOHNMR"].Type != PaxAdult || by["ANNMRS"].Type != PaxAdult {
		t.Errorf("types: %s %s %s", by["JOHNMR"].Type, by["ANNMRS"].Type, by["TIMMSTR"].Type)
	}
	codes := func(p *Passenger) []string {
		var out []string
		for _, s := range p.SSRs {
			out = append(out, s.Code)
		}
		return out
	}
	if got := codes(by["JOHNMR"]); len(got) != 1 || got[0] != "VGML" {
		t.Errorf("John should carry only the party's meal: %v", got)
	}
	if got := codes(by["ANNMRS"]); len(got) != 2 || got[1] != "WCHR" {
		t.Errorf("Ann should carry the meal and her wheelchair: %v", got)
	}
	if got := codes(by["TIMMSTR"]); len(got) != 2 || got[1] != "CHLD" {
		t.Errorf("Tim should carry the meal and CHLD: %v", got)
	}
	if by["ANNMRS"].Ticket != "526-2000000002" || by["TIMMSTR"].Ticket != "526-2000000003" {
		t.Errorf("each name its own ticket: %q %q", by["ANNMRS"].Ticket, by["TIMMSTR"].Ticket)
	}
}
