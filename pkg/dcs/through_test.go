package dcs

import (
	"context"
	"testing"
)

// Another carrier's DCS through-checks a party onto our flight: the name on
// the list is accepted with a seat and a sequence and its inbound
// connection recorded; a name not on the list is refused by itself, not the
// whole party; a closed flight refuses everyone.
func TestThroughCheckInAcceptsWhatIsListedAndRefusesTheRest(t *testing.T) {
	ctx := context.Background()
	s := station(t, "789")
	if _, err := s.ApplyNameList(ctx, listOf(t)); err != nil {
		t.Fatal(err)
	}
	req := ThroughRequest{
		Requestor: "AA", Station: "JFK", Key: testKey,
		Inbound: Connection{Flight: "AA0100", Date: "16DEC", Station: "LHR"},
		Passengers: []ThroughPassenger{
			{Ref: "P1", Surname: "COSTA", Locator: "AAA111", SeatWant: "1A", BagPieces: 2, BagWeight: 30},
			{Ref: "P2", Surname: "NOBODY", Locator: "ZZZ999"},
		},
	}
	res, err := s.ThroughCheckIn(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Granted || len(res.Outcomes) != 2 {
		t.Fatalf("one of two accepted is not granted: %+v", res)
	}
	costa, nobody := res.Outcomes[0], res.Outcomes[1]
	if !costa.Accepted || costa.Seat == "" || costa.Sequence == 0 || costa.Ref != "P1" {
		t.Fatalf("COSTA should be seated with a sequence: %+v", costa)
	}
	if nobody.Accepted || nobody.Reason != ThroughRefusedNotFound {
		t.Fatalf("NOBODY is not on the list: %+v", nobody)
	}
	fl := mustFlight(t, s)
	// The party under AAA111 has more than one COSTA; exactly one was asked
	// for and exactly one is accepted.
	var p *Passenger
	accepted := 0
	for _, x := range fl.Passengers {
		if x.Surname == "COSTA" && x.Status == StatusAccepted {
			p = x
			accepted++
		}
	}
	if accepted != 1 || p.Inbound == nil || p.Inbound.Flight != "AA0100" || len(p.Bags) != 2 {
		t.Fatalf("COSTA accepted with the inbound connection and two connecting bags: %+v", p)
	}
	// The party under AAA111 has two COSTAs: asking again by name takes the
	// second, not the one already seated; a third time there is nobody left.
	again, _ := s.ThroughCheckIn(ctx, ThroughRequest{Key: testKey, Passengers: []ThroughPassenger{{Ref: "P1", Surname: "COSTA", Locator: "AAA111"}}})
	if !again.Granted || !again.Outcomes[0].Accepted || again.Outcomes[0].Seat == costa.Seat {
		t.Fatalf("the second COSTA gets her own seat: %+v", again.Outcomes)
	}
	third, _ := s.ThroughCheckIn(ctx, ThroughRequest{Key: testKey, Passengers: []ThroughPassenger{{Ref: "P1", Surname: "COSTA", Locator: "AAA111"}}})
	if third.Granted || third.Outcomes[0].Reason != ThroughRefusedNotFound {
		t.Fatalf("nobody of that name is left to accept: %+v", third.Outcomes)
	}
	// A flight not under control refuses the whole party with the flight code.
	none, _ := s.ThroughCheckIn(ctx, ThroughRequest{Key: Key{Flight: "BA0999", Date: "16DEC", Board: "LHR"}, Passengers: req.Passengers})
	if none.Granted || len(none.Outcomes) != 2 || none.Outcomes[1].Reason != ThroughRefusedFlight {
		t.Fatalf("unknown flight: %+v", none.Outcomes)
	}
}
