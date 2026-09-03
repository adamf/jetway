package dcs

import (
	"context"
	"testing"
)

// An aircraft substitution rebuilds the cabin: seats that exist in the new
// layout in the same compartment are kept, the rest are re-seated, and when
// the smaller aircraft has no seat left the last to have arrived goes to
// standby with an alert. A closed flight cannot change aircraft.
func TestChangeEquipmentReseatsWhatFitsAndDisplacesTheRest(t *testing.T) {
	ctx := context.Background()
	s := station(t, "789")
	if _, err := s.ApplyNameList(ctx, listOf(t)); err != nil {
		t.Fatal(err)
	}
	fl := mustFlight(t, s)
	before := fl.Cabin.Seats()
	// Accept everyone listed.
	var accepted []*Passenger
	for _, p := range fl.Passengers {
		acc, err := s.Accept(ctx, testKey, AcceptRequest{PassengerID: p.ID})
		if err != nil {
			continue
		}
		accepted = append(accepted, acc.Passengers...)
	}
	if len(accepted) < 3 {
		t.Fatalf("need a few accepted passengers: %d", len(accepted))
	}
	// Substitute the smallest type in the fleet: a regional jet for a 789.
	smallest := ""
	fewest := before
	for code, at := range s.fleet().Types {
		if n := at.Cabin.Seats(); n < fewest {
			smallest, fewest = code, n
		}
	}
	if smallest == "" {
		t.Skip("fleet has no smaller type")
	}
	ch, err := s.ChangeEquipment(ctx, testKey, Equipment{Type: smallest, Registration: "G-SUBS"})
	if err != nil {
		t.Fatal(err)
	}
	if ch.From != "789" || ch.To != smallest || ch.SeatsAfter != fewest || ch.Flight.Registration != "G-SUBS" {
		t.Fatalf("change: %+v", ch)
	}
	seated := 0
	for _, p := range ch.Flight.Passengers {
		if p.Status == StatusAccepted && p.Seat != "" {
			if _, ok := ch.Flight.Cabin.Has(p.Seat); !ok {
				t.Fatalf("%s holds %s which the %s does not have", p.Surname, p.Seat, smallest)
			}
			seated++
		}
	}
	if seated != ch.Kept+len(ch.Reseated) || seated > fewest {
		t.Fatalf("seated %d vs kept %d + reseated %d, cabin %d", seated, ch.Kept, len(ch.Reseated), fewest)
	}
	if len(accepted) > fewest && len(ch.Displaced) == 0 {
		t.Fatalf("%d accepted onto %d seats must displace someone", len(accepted), fewest)
	}
	for _, p := range ch.Displaced {
		if p.Status != StatusStandby || p.Seat != "" {
			t.Fatalf("displaced passenger is on standby without a seat: %+v", p)
		}
	}
	found := false
	for _, a := range ch.Flight.Alerts {
		if a.Code == "equipment_change" {
			found = true
		}
	}
	if !found {
		t.Fatal("the change is an alert a supervisor sees")
	}
	if _, err := s.CloseFlight(ctx, testKey, CloseOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ChangeEquipment(ctx, testKey, Equipment{Type: "789"}); err == nil {
		t.Fatal("a closed flight cannot change aircraft")
	}
}
