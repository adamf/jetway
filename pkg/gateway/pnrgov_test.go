package gateway

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/adamf/jetway/pkg/dcs"
	"github.com/adamf/jetway/pkg/padis"
	"github.com/adamf/jetway/pkg/pnr"
	"github.com/adamf/jetway/pkg/store"
)

// The push a carrier makes to a state, end to end: the records the carrier
// holds on the flight, with the door's seat, sequence and bags for the
// travellers who checked in, go over an EDIFACT link and are read by the
// state's node without being taken for reservations there.
func TestPNRGOVPushCarriesRecordsAndCheckInToTheState(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	carrier := New(Identity{Designator: "BA", TTYAddress: "LONRMBA", Name: "ba"}, store.NewMem(), NewBus(100), log, []byte("s"))
	state := New(Identity{Designator: "UKBF", TTYAddress: "LONGVUK", Name: "border"}, store.NewMem(), NewBus(100), log, []byte("s"))
	var sent []byte
	carrier.AddPeer(&Peer{Name: "UKBF", Carrier: "UKBF", Format: store.FormatEDIFACT})
	carrier.Sender = SenderFunc(func(ctx context.Context, peer string, raw []byte) error {
		sent = append([]byte(nil), raw...)
		return nil
	})
	state.AddPeer(&Peer{Name: "BA", Carrier: "BA", Format: store.FormatEDIFACT})
	var got *padis.GovPush
	state.PNRGOV = func(ctx context.Context, peer *Peer, push *padis.GovPush) { got = push }

	now := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	dep := time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)
	mk := func(loc string, board string, pax ...string) {
		rec := &pnr.PNR{RecordLocator: loc, Status: pnr.StatusOpen, CreatedAt: now, UpdatedAt: now, Origin: pnr.Origin{Party: "1J", Agent: "LON"},
			Segments: []pnr.Segment{{Ref: 1, Type: pnr.SegmentAir, Carrier: "BA", FlightNum: "0117", Board: board, Off: "JFK",
				Status: "HK", Seats: len(pax), WireDate: "26NOV", Depart: dep, DepartTime: "0830", ArriveTime: "1120"}}}
		for i, p := range pax {
			rec.Passengers = append(rec.Passengers, pnr.Passenger{Ref: i + 1, Surname: p, Given: "ANN", Title: "MS"})
		}
		if err := carrier.Store.CreatePNR(ctx, rec, nil); err != nil {
			t.Fatal(err)
		}
	}
	mk("PUSH01", "LHR", "SMITH", "SMITH")
	mk("PUSH02", "LHR", "JONES")
	mk("OTHER1", "MAN", "BROWN") // same flight number, another leg

	fl := &dcs.Flight{Key: dcs.Key{Flight: "BA0117", Date: "26NOV", Board: "LHR"}, Carrier: "BA", Dest: "JFK", Passengers: []*dcs.Passenger{
		{ID: 1, Surname: "SMITH", Given: "ANNMS", Locator: "PUSH01", Status: dcs.StatusAccepted, Seat: "14C", Sequence: 7, Compartment: "Y",
			Bags: []dcs.Bag{{Tag: "0125123456", Weight: 18}, {Tag: "0125123457", Weight: 9, Offloaded: true}}},
		{ID: 2, Surname: "SMITH", Given: "ANNMS", Locator: "PUSH01", Status: dcs.StatusAccepted, Seat: "14D", Sequence: 8, Compartment: "Y"},
		{ID: 3, Surname: "JONES", Given: "ANNMS", Locator: "PUSH02", Status: dcs.StatusListed},
	}}
	push, err := carrier.PNRGOVFor(ctx, fl, PNRGOVOptions{Departs: dep.Add(8*time.Hour + 30*time.Minute), Arrives: dep.Add(11*time.Hour + 20*time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if len(push.Records) != 2 {
		t.Fatalf("records on the leg: %d", len(push.Records))
	}
	byLoc := map[string]padis.GovRecord{}
	for _, r := range push.Records {
		byLoc[r.PNR.RecordLocator] = r
	}
	if _, other := byLoc["OTHER1"]; other {
		t.Error("the other leg's record was pushed")
	}
	smith := byLoc["PUSH01"]
	if len(smith.CheckIn) != 2 || smith.CheckIn[0].Seat != "14C" || smith.CheckIn[0].PaxRef != 1 || smith.CheckIn[1].Seat != "14D" || smith.CheckIn[1].PaxRef != 2 {
		t.Fatalf("two Smiths, two seats, in order: %+v", smith.CheckIn)
	}
	if b := smith.CheckIn[0].Bags; len(b) != 1 || b[0].Tag != "0125123456" || smith.CheckIn[0].BagWeightKg != 18 {
		t.Errorf("an offloaded bag is not on the push: %+v", b)
	}
	if len(byLoc["PUSH02"].CheckIn) != 0 {
		t.Error("a listed, unaccepted passenger has no check-in data")
	}

	if err := carrier.SendPNRGOV(ctx, carrier.Peer("UKBF"), push); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sent), "UNH+") || !strings.Contains(string(sent), "PNRGOV:11:1:IA") || !strings.Contains(string(sent), "EQN+2'") {
		t.Fatalf("wire:\n%s", sent)
	}
	res, err := state.Ingest(ctx, "BA", sent)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != store.StatusApplied || got == nil {
		t.Fatalf("state did not hear the push: %+v", res)
	}
	if got.Count != 2 || len(got.Records) != 2 || got.Flight.Carrier != "BA" || got.Flight.Number != "0117" || got.Flight.Board != "LHR" {
		t.Errorf("push as read: %+v", got)
	}
	recs, _ := state.Store.ListPNRs(ctx, 10)
	if len(recs) != 0 {
		t.Errorf("a state's copy became %d reservations", len(recs))
	}
}
