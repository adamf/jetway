package dcs

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/adamf/jetway/pkg/pnl"
)

func fixedClock() func() time.Time {
	at := time.Date(2026, 12, 16, 9, 30, 0, 0, time.UTC)
	return func() time.Time { return at }
}

// station is a BA departure control at LHR that knows its aircraft.
func station(t *testing.T, equipment string) *Station {
	t.Helper()
	s := NewStation("BA")
	s.AccountingCode = "125"
	s.Now = fixedClock()
	s.Equipment = func(k Key) (Equipment, bool) {
		return Equipment{Type: equipment, Registration: "GBZHA", Dest: "JFK", Crew: "2/6"}, true
	}
	return s
}

var testKey = Key{Flight: "BA0117", Date: "16DEC", Board: "LHR"}

// listOf is the PNL for a small flight: a party of two in business, three
// singles in economy, one with a wheelchair request and a ticket.
func listOf(t *testing.T) *pnl.Message {
	t.Helper()
	m, err := pnl.Parse(strings.Join([]string{
		"PNL",
		"BA0117/16DEC LHR PART1",
		"-JFK002J",
		"2COSTA/RUIMR/ANAMRS .L/AAA111",
		"-JFK003Y",
		"1SMITH/JOHNMR .L/BBB222 .R/WCHR HK1 .R/TKNE HK1 1251234567890C1",
		"1NG/MEIMS .L/CCC333",
		"1OKAFOR/ADAMR .L/DDD444 .R/VGML HK1",
		"ENDPNL",
	}, "\n"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestNameListOpensAndListsTheFlight(t *testing.T) {
	ctx := context.Background()
	s := station(t, "320")
	f, err := s.ApplyNameList(ctx, listOf(t))
	if err != nil {
		t.Fatal(err)
	}
	if f.State != StateOpen || !f.Complete {
		t.Fatalf("state %s complete %v after a final PNL part", f.State, f.Complete)
	}
	if got := f.Counts(); got.Listed != 5 || got.Seats != 180 {
		t.Fatalf("counts %+v, want 5 listed on a 180-seat cabin", got)
	}
	if f.Equipment != "320" || f.Registration != "GBZHA" || f.Dest != "JFK" || f.Version != "Y180" {
		t.Errorf("equipment not taken from the lookup: %+v", f)
	}
	var smith *Passenger
	for _, p := range f.Passengers {
		if p.Surname == "SMITH" {
			smith = p
		}
		if p.Surname == "COSTA" && p.Compartment != "Y" {
			// An all-economy aircraft seats a business booking in Y.
			t.Errorf("COSTA booked J on a Y-only cabin landed in %s", p.Compartment)
		}
	}
	if smith == nil {
		t.Fatal("SMITH not listed")
	}
	if smith.Ticket != "1251234567890C1" {
		t.Errorf("TKNE not read into the ticket: %q", smith.Ticket)
	}
	if len(smith.SSRs) != 1 || smith.SSRs[0].Code != "WCHR" {
		t.Errorf("SSRs %+v, want WCHR only (TKNE is not a service)", smith.SSRs)
	}
	if smith.Locator != "BBB222" || smith.Class != "Y" {
		t.Errorf("locator/class wrong: %+v", smith)
	}
}

func TestBusinessBookingsSeatInBusinessWhenTheCabinHasOne(t *testing.T) {
	ctx := context.Background()
	s := station(t, "789")
	f, err := s.ApplyNameList(ctx, listOf(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range f.Passengers {
		if p.Surname == "COSTA" && p.Compartment != "C" {
			t.Errorf("COSTA booked J landed in %s on a two-cabin aircraft", p.Compartment)
		}
	}
	if f.Version != "C32Y258" {
		t.Errorf("version %q", f.Version)
	}
}

func TestADLDeletesAddsAndKeepsAcceptedPassengers(t *testing.T) {
	ctx := context.Background()
	s := station(t, "320")
	if _, err := s.ApplyNameList(ctx, listOf(t)); err != nil {
		t.Fatal(err)
	}
	// SMITH checks in before the ADL arrives.
	if _, err := s.Accept(ctx, testKey, AcceptRequest{Locator: "BBB222"}); err != nil {
		t.Fatal(err)
	}
	adl, err := pnl.Parse(strings.Join([]string{
		"ADL",
		"BA0117/16DEC LHR PART1",
		"-JFK004Y",
		"DEL",
		"1NG/MEIMS .L/CCC333",
		"1SMITH/JOHNMR .L/BBB222",
		"ADD",
		"1PATEL/RAVIMR .L/EEE555",
		"CHG",
		"1OKAFOR/ADAMR .L/DDD444 .R/VGML HK1 .R/UMNR HK1",
		"ENDADL",
	}, "\n"))
	if err != nil {
		t.Fatal(err)
	}
	f, err := s.ApplyNameList(ctx, adl)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*Passenger{}
	for _, p := range f.Passengers {
		byName[p.Surname] = p
	}
	if byName["NG"].Status != StatusDeleted {
		t.Errorf("NG should be deleted, is %s", byName["NG"].Status)
	}
	if byName["SMITH"].Status != StatusAccepted || !byName["SMITH"].DeletedAfterAcceptance {
		t.Errorf("SMITH was accepted; the ADL must not unseat him: %+v", byName["SMITH"])
	}
	if byName["PATEL"] == nil || byName["PATEL"].Status != StatusListed {
		t.Errorf("PATEL not added: %+v", byName["PATEL"])
	}
	if got := len(byName["OKAFOR"].SSRs); got != 2 {
		t.Errorf("CHG did not replace OKAFOR's elements: %+v", byName["OKAFOR"].SSRs)
	}
	found := false
	for _, a := range f.Alerts {
		if a.Code == "adl_deletes_accepted" {
			found = true
		}
	}
	if !found {
		t.Errorf("no alert for the deletion of an accepted passenger: %+v", f.Alerts)
	}
	if f.ADLs != 1 {
		t.Errorf("ADLs %d", f.ADLs)
	}
}

func TestPNLAfterAcceptanceIsRefused(t *testing.T) {
	ctx := context.Background()
	s := station(t, "320")
	if _, err := s.ApplyNameList(ctx, listOf(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Accept(ctx, testKey, AcceptRequest{Locator: "CCC333"}); err != nil {
		t.Fatal(err)
	}
	again := listOf(t)
	again.Part = 2
	if _, err := s.ApplyNameList(ctx, again); !errors.Is(err, ErrListAfterAccept) {
		t.Fatalf("a PNL after acceptance began must be refused; got %v", err)
	}
}

func TestDuplicatePNLPartIsIgnored(t *testing.T) {
	ctx := context.Background()
	s := station(t, "320")
	if _, err := s.ApplyNameList(ctx, listOf(t)); err != nil {
		t.Fatal(err)
	}
	f, err := s.ApplyNameList(ctx, listOf(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Counts().Listed; got != 5 {
		t.Errorf("a repeated part doubled the list to %d", got)
	}
}

func TestAcceptSeatsThePartyTogetherAndTagsBags(t *testing.T) {
	ctx := context.Background()
	s := station(t, "320")
	if _, err := s.ApplyNameList(ctx, listOf(t)); err != nil {
		t.Fatal(err)
	}
	acc, err := s.Accept(ctx, testKey, AcceptRequest{Locator: "AAA111", Bags: []int{18, 23}})
	if err != nil {
		t.Fatal(err)
	}
	if len(acc.Passengers) != 2 {
		t.Fatalf("party of two, %d accepted", len(acc.Passengers))
	}
	a, b := acc.Passengers[0], acc.Passengers[1]
	if rowOf(a.Seat) != rowOf(b.Seat) || a.Seat == b.Seat {
		t.Errorf("party split across rows: %s %s", a.Seat, b.Seat)
	}
	if a.Sequence != 1 || b.Sequence != 2 {
		t.Errorf("sequence numbers %d %d", a.Sequence, b.Sequence)
	}
	if len(acc.Tags) != 2 || !strings.HasPrefix(acc.Tags[0].Tag, "0125") || len(acc.Tags[0].Tag) != 10 {
		t.Errorf("tags %+v: want two ten-digit plates leading 0125", acc.Tags)
	}
	if acc.Tags[0].Tag == acc.Tags[1].Tag {
		t.Error("two bags, one licence plate")
	}
	if len(a.Bags) != 2 || len(b.Bags) != 0 {
		t.Errorf("bags belong to the lead: %d/%d", len(a.Bags), len(b.Bags))
	}
	// Accepting again is refused, not doubled.
	if _, err := s.Accept(ctx, testKey, AcceptRequest{Locator: "AAA111"}); !errors.Is(err, ErrAlreadyAccepted) {
		t.Errorf("second acceptance: %v", err)
	}
}

func TestRequestedSeatIsHonouredOrRefused(t *testing.T) {
	ctx := context.Background()
	s := station(t, "320")
	if _, err := s.ApplyNameList(ctx, listOf(t)); err != nil {
		t.Fatal(err)
	}
	acc, err := s.Accept(ctx, testKey, AcceptRequest{Locator: "BBB222", Seat: "12A"})
	if err != nil {
		t.Fatal(err)
	}
	if acc.Passengers[0].Seat != "12A" {
		t.Errorf("seat %s, asked for 12A", acc.Passengers[0].Seat)
	}
	if _, err := s.Accept(ctx, testKey, AcceptRequest{Locator: "CCC333", Seat: "12A"}); !errors.Is(err, ErrSeatTaken) {
		t.Errorf("12A twice: %v", err)
	}
	if _, err := s.Accept(ctx, testKey, AcceptRequest{Locator: "CCC333", Seat: "99Z"}); !errors.Is(err, ErrNoSuchSeat) {
		t.Errorf("99Z: %v", err)
	}
	if _, err := s.Accept(ctx, testKey, AcceptRequest{Locator: "NOPE"}); !errors.Is(err, ErrPassengerNotFound) {
		t.Errorf("unknown locator: %v", err)
	}
}

func TestGoShowStandbyAndClearance(t *testing.T) {
	ctx := context.Background()
	// A two-seat aircraft type, to fill.
	s := station(t, "320")
	s.Fleet = &FleetData{DefaultType: "TINY", Types: map[string]*AircraftType{"TINY": {
		Code: "TINY", DOW: 1000, MZFW: 5000, MTOW: 6000, MLW: 5500, RefArm: 5, C: 100, K: 50, LEMAC: 4, MAC: 2, FwdMAC: 10, AftMAC: 90, FuelArm: 5,
		Cabin:        CabinLayout{Sections: []Section{{Compartment: "Y", FromRow: 1, ToRow: 1, Letters: "AC"}}},
		Compartments: []Compartment{{Name: "1", Max: 500, Arm: 4}},
		Zones:        []Zone{{Name: "OA", FromRow: 1, ToRow: 1, Arm: 5}},
	}}}
	s.Equipment = func(k Key) (Equipment, bool) { return Equipment{Type: "TINY", Dest: "JFK"}, true }
	m, _ := pnl.Parse("PNL\nBA0117/16DEC LHR PART1\n-JFK001Y\n1SMITH/JOHNMR .L/BBB222\nENDPNL")
	if _, err := s.ApplyNameList(ctx, m); err != nil {
		t.Fatal(err)
	}
	// A revenue go-show takes the second seat; the next one waits.
	acc, err := s.AcceptGoShow(ctx, testKey, GoShow{Surname: "LATE", Given: "ANNMRS", Locator: "ZZZ999"})
	if err != nil {
		t.Fatal(err)
	}
	if acc.Passengers[0].Status != StatusAccepted || acc.Passengers[0].Category != CategoryGoShow {
		t.Errorf("go-show with a locator: %+v", acc.Passengers[0])
	}
	sb, err := s.AcceptGoShow(ctx, testKey, GoShow{Surname: "WAIT", Given: "BOBMR"})
	if err != nil {
		t.Fatal(err)
	}
	if sb.Passengers[0].Status != StatusStandby || sb.Passengers[0].Category != CategoryNoRec {
		t.Errorf("no seat left: want standby NOREC, got %+v", sb.Passengers[0])
	}
	// SMITH never turns up. Check-in closes, which is the moment his seat is
	// no longer his, and the standby clears into it.
	f, cleared, err := s.CloseCheckIn(ctx, testKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(cleared) != 1 || cleared[0].Surname != "WAIT" || cleared[0].Status != StatusAccepted {
		t.Errorf("cleared %+v, want WAIT accepted", cleared)
	}
	if f.State != StateCheckInClosed {
		t.Errorf("state %s", f.State)
	}
	if _, err := s.Accept(ctx, testKey, AcceptRequest{Locator: "BBB222"}); !errors.Is(err, ErrCheckInClosed) {
		t.Errorf("acceptance after close: %v", err)
	}
	// A supervisor can force acceptance, but not conjure a seat: SMITH
	// turning up now finds the aircraft full.
	if _, err := s.Accept(ctx, testKey, AcceptRequest{Locator: "BBB222", Force: true}); !errors.Is(err, ErrNoSeat) {
		t.Errorf("forced acceptance onto a full aircraft: %v", err)
	}
}

func TestBoardOffloadAndCloseProduceTheMessages(t *testing.T) {
	ctx := context.Background()
	s := station(t, "789")
	if _, err := s.ApplyNameList(ctx, listOf(t)); err != nil {
		t.Fatal(err)
	}
	// Everyone but NG checks in; OKAFOR through-checks to BOS; SMITH has bags.
	costa, err := s.Accept(ctx, testKey, AcceptRequest{Locator: "AAA111", Bags: []int{20}})
	if err != nil {
		t.Fatal(err)
	}
	smith, err := s.Accept(ctx, testKey, AcceptRequest{Locator: "BBB222", Bags: []int{15, 25}})
	if err != nil {
		t.Fatal(err)
	}
	okafor, err := s.Accept(ctx, testKey, AcceptRequest{Locator: "DDD444", Bags: []int{12},
		Onward: &Connection{Flight: "BA0178", Date: "16DEC", Station: "JFK", Dest: "BOS", Class: "Y"}})
	if err != nil {
		t.Fatal(err)
	}
	// COSTA and OKAFOR board; SMITH does not.
	for _, p := range append(costa.Passengers, okafor.Passengers...) {
		if _, err := s.Board(ctx, testKey, p.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Board(ctx, testKey, costa.Passengers[0].ID); !errors.Is(err, ErrAlreadyBoarded) {
		t.Errorf("boarding twice: %v", err)
	}
	if _, err := s.CloseFlight(ctx, testKey, CloseOptions{}); !errors.Is(err, ErrCheckInOpen) {
		t.Errorf("close with check-in open: %v", err)
	}
	cl, err := s.CloseFlight(ctx, testKey, CloseOptions{Force: true, Fuel: FuelPlan{TakeOff: 45000, Trip: 38000}, Cargo: 3000})
	if err != nil {
		t.Fatal(err)
	}
	c := cl.Counts
	if c.Boarded != 3 || c.NoShow != 1 || c.Offload != 1 {
		t.Errorf("counts %+v: want 3 boarded, NG no-show, SMITH offloaded", c)
	}
	if len(cl.Offloaded) != 1 || cl.Offloaded[0].ID != smith.Passengers[0].ID {
		t.Errorf("offloaded %+v, want SMITH", cl.Offloaded)
	}
	for _, b := range cl.Offloaded[0].Bags {
		if !b.Offloaded {
			t.Errorf("SMITH's bag %s still loaded after he failed to board", b.Tag)
		}
	}
	if c.Bags != 2 || c.BagKilos != 32 {
		t.Errorf("bags on board %d/%d kg, want COSTA's 20 and OKAFOR's 12", c.Bags, c.BagKilos)
	}
	// The messages.
	pfs := strings.Join(cl.PFS, "\n")
	if !strings.Contains(pfs, "NOSHO\n1NG/MEIMS .L/CCC333") {
		t.Errorf("PFS does not report NG as a no-show:\n%s", pfs)
	}
	if !strings.Contains(pfs, "OFFLD\n1SMITH/JOHNMR .L/BBB222") {
		t.Errorf("PFS does not report SMITH offloaded:\n%s", pfs)
	}
	if strings.Contains(pfs, "COSTA") {
		t.Errorf("PFS reports a passenger handled as listed:\n%s", pfs)
	}
	ptm := strings.Join(cl.PTM, "\n")
	if !strings.Contains(ptm, "BA0178/16 BOS 1Y 1B OKAFOR/ADAMR") {
		t.Errorf("PTM:\n%s", ptm)
	}
	if !strings.Contains(ptm, "BA0117/16DEC LHRJFK PART1") {
		t.Errorf("PTM header:\n%s", ptm)
	}
	psm := strings.Join(cl.PSM, "\n")
	if !strings.Contains(psm, "NIL") {
		// SMITH had the wheelchair and did not fly; nobody on board needs help.
		t.Errorf("PSM should be NIL:\n%s", psm)
	}
	etl := strings.Join(cl.ETL, "\n")
	if !strings.Contains(etl, "OKAFOR/ADAMR .L/DDD444 .S/") || strings.Contains(etl, "SMITH") {
		t.Errorf("ETL:\n%s", etl)
	}
	if !strings.HasPrefix(cl.LDM, "LDM\nBA0117/16.GBZHA.C32Y258.2/6\n-JFK.3/0/0.T") {
		t.Errorf("LDM:\n%s", cl.LDM)
	}
	if !strings.Contains(cl.LDM, ".PAX/2/1.PAD/0/0") {
		t.Errorf("LDM cabin split (COSTA x2 in C, OKAFOR in Y):\n%s", cl.LDM)
	}
	if cl.CPM == "" || !strings.HasPrefix(cl.CPM, "CPM\nBA0117/16.GBZHA.C32Y258\n-") {
		t.Errorf("CPM for a containerised aircraft:\n%s", cl.CPM)
	}
	if !strings.Contains(cl.Loadsheet, "LOADSHEET") || !strings.Contains(cl.Loadsheet, "SI NIL") {
		t.Errorf("loadsheet:\n%s", cl.Loadsheet)
	}
	if cl.Load.ZFW != 129000+3*84+32+3000 {
		t.Errorf("ZFW %d", cl.Load.ZFW)
	}
	if len(cl.Load.Violations) != 0 {
		t.Errorf("violations %v", cl.Load.Violations)
	}
	// Nothing changes after close.
	if _, err := s.Board(ctx, testKey, smith.Passengers[0].ID); !errors.Is(err, ErrFlightClosed) {
		t.Errorf("boarding after close: %v", err)
	}
	if _, err := s.ApplyNameList(ctx, listOf(t)); !errors.Is(err, ErrFlightClosed) {
		t.Errorf("name list after close: %v", err)
	}
}

func TestOffloadFreesTheSeatAndFlagsTheBags(t *testing.T) {
	ctx := context.Background()
	s := station(t, "320")
	if _, err := s.ApplyNameList(ctx, listOf(t)); err != nil {
		t.Fatal(err)
	}
	acc, err := s.Accept(ctx, testKey, AcceptRequest{Locator: "BBB222", Seat: "5C", Bags: []int{20}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Offload(ctx, testKey, acc.Passengers[0].ID, "security")
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != StatusOffloaded || !p.Bags[0].Offloaded || p.OffloadReason != "security" {
		t.Errorf("%+v", p)
	}
	if _, err := s.Accept(ctx, testKey, AcceptRequest{Locator: "CCC333", Seat: "5C"}); err != nil {
		t.Errorf("5C should be free again: %v", err)
	}
	bsm := s.BSMFor(mustFlight(t, s), p, "DEL")
	if bsm.Change != "DEL" || len(bsm.Tags) != 1 || bsm.Tags[0].Number != p.Bags[0].Tag {
		t.Errorf("BSM DEL: %+v", bsm)
	}
}

func mustFlight(t *testing.T, s *Station) *Flight {
	t.Helper()
	f, err := s.Flight(testKey)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestBagReportMarksLoadedAndCatchesStrangers(t *testing.T) {
	ctx := context.Background()
	s := station(t, "320")
	if _, err := s.ApplyNameList(ctx, listOf(t)); err != nil {
		t.Fatal(err)
	}
	acc, err := s.Accept(ctx, testKey, AcceptRequest{Locator: "BBB222", Bags: []int{20, 21}})
	if err != nil {
		t.Fatal(err)
	}
	bsm := s.BSMFor(mustFlight(t, s), acc.Passengers[0], "")
	bsm.Kind = "BPM"
	bsm.Tags = append(bsm.Tags, struct {
		Number string
		Count  int
	}{"0999000001", 1})
	f, unknown, err := s.ApplyBagReport(ctx, bsm)
	if err != nil {
		t.Fatal(err)
	}
	if len(unknown) != 1 || unknown[0] != "0999000001" {
		t.Errorf("unknown %v", unknown)
	}
	for _, p := range f.Passengers {
		if p.Surname == "SMITH" {
			for _, b := range p.Bags {
				if !b.Loaded {
					t.Errorf("bag %s not marked loaded", b.Tag)
				}
			}
		}
	}
	if len(f.Alerts) == 0 || f.Alerts[len(f.Alerts)-1].Code != "unaccompanied_bag" {
		t.Errorf("no unaccompanied bag alert: %+v", f.Alerts)
	}
}

func TestStoreRoundTripKeepsCounters(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()
	s := station(t, "320")
	s.Store = st
	if _, err := s.ApplyNameList(ctx, listOf(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Accept(ctx, testKey, AcceptRequest{Locator: "AAA111", Bags: []int{1}}); err != nil {
		t.Fatal(err)
	}
	// A new station over the same store picks up where this one was.
	s2 := station(t, "320")
	s2.Store = st
	if err := s2.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	acc, err := s2.Accept(ctx, testKey, AcceptRequest{Locator: "BBB222"})
	if err != nil {
		t.Fatal(err)
	}
	if acc.Passengers[0].Sequence != 3 {
		t.Errorf("sequence restarted: %d", acc.Passengers[0].Sequence)
	}
	f := mustFlight(t, s2)
	if f.Cabin.Occupied[acc.Passengers[0].Seat] != acc.Passengers[0].ID {
		t.Error("occupancy not rebuilt from the passengers")
	}
	if len(f.Cabin.Occupied) != 3 {
		t.Errorf("%d seats occupied, want the two COSTAs and SMITH", len(f.Cabin.Occupied))
	}
	keys, _ := st.ListFlights(ctx)
	if len(keys) != 1 || keys[0] != testKey {
		t.Errorf("stored keys %v", keys)
	}
}

func TestNameListWithoutEquipmentStillOpensWithAnAlert(t *testing.T) {
	ctx := context.Background()
	s := NewStation("BA")
	s.Now = fixedClock()
	f, err := s.ApplyNameList(ctx, listOf(t))
	if err != nil {
		t.Fatal(err)
	}
	if f.Dest != "JFK" {
		t.Errorf("destination should come from the list: %q", f.Dest)
	}
	if len(f.Alerts) == 0 || f.Alerts[0].Code != "no_equipment" {
		t.Errorf("alerts %+v", f.Alerts)
	}
	if f.Cabin == nil || f.Cabin.Seats() == 0 {
		t.Error("no default cabin")
	}
}
