package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/adamf/jetway/pkg/dcs"
	"github.com/adamf/jetway/pkg/iatci"
)

// The wire maps onto the station's terms and back: the outbound flight is
// the key, the inbound the connection, refusals carry their codes.
func TestThroughCheckInMapsWireToStationAndBack(t *testing.T) {
	req := &iatci.CheckInRequest{
		Requestor: "BA", RequestorStation: "LHR",
		Inbound:  iatci.Flight{Carrier: "BA", Number: "0117", Date: time.Date(2025, 12, 16, 8, 30, 0, 0, time.UTC), Board: "LHR", Off: "JFK"},
		Outbound: iatci.Flight{Carrier: "AA", Number: "0100", Date: time.Date(2025, 12, 16, 14, 0, 0, 0, time.UTC), Board: "JFK", Off: "DFW"},
		Passengers: []iatci.Passenger{{Ref: "P1", Surname: "SMITH", Given: "JANE", Locator: "ABC123", SeatWant: "14C", Pieces: 2, Weight: 31}},
	}
	tr := throughRequestOf(req)
	if tr.Key != (dcs.Key{Flight: "AA0100", Date: "16DEC", Board: "JFK"}) {
		t.Fatalf("key: %+v", tr.Key)
	}
	if tr.Inbound.Flight != "BA0117" || tr.Inbound.Date != "16DEC" || tr.Inbound.Station != "JFK" || tr.Inbound.Dest != "DFW" {
		t.Fatalf("inbound: %+v", tr.Inbound)
	}
	if len(tr.Passengers) != 1 || tr.Passengers[0].BagPieces != 2 || tr.Passengers[0].Locator != "ABC123" {
		t.Fatalf("passenger: %+v", tr.Passengers)
	}
	res := throughResponseOf(req, &dcs.ThroughResult{Granted: false, Outcomes: []dcs.ThroughOutcome{
		{Ref: "P1", Surname: "SMITH", Given: "JANE", Accepted: true, Seat: "14C", Cabin: "Y", Sequence: 57},
		{Ref: "P2", Surname: "SMITH", Given: "TIM", Reason: dcs.ThroughRefusedFull, Text: "no seat available"},
	}})
	if res.Status != "O" || len(res.Passengers) != 2 || res.Passengers[0].Status != "H" || res.Passengers[0].Seat != "14C" || !res.Passengers[0].BoardingPass {
		t.Fatalf("response: %+v", res)
	}
	if res.Passengers[1].Status != "I" || res.Passengers[1].Errors[0].Code != iatci.ErrFlightFull {
		t.Fatalf("refusal: %+v", res.Passengers[1])
	}
	if res.Granted() {
		t.Fatal("a refused passenger means not granted")
	}
	_ = context.Background()
}
