package dcs

import (
	"context"
	"errors"
	"testing"
)

// Bag reconciliation at the door: a bag the hold reports loaded whose
// passenger did not board holds the flight until it is pulled; a boarded
// passenger's bag the hold never reported is short-shipped and named.
func TestCloseReconcilesLoadedBagsAgainstBoardedPassengers(t *testing.T) {
	ctx := context.Background()
	s := station(t, "789")
	if _, err := s.ApplyNameList(ctx, listOf(t)); err != nil {
		t.Fatal(err)
	}
	costa, err := s.Accept(ctx, testKey, AcceptRequest{Locator: "AAA111", Bags: []int{20, 18}})
	if err != nil {
		t.Fatal(err)
	}
	smith, err := s.Accept(ctx, testKey, AcceptRequest{Locator: "BBB222", Bags: []int{15}})
	if err != nil {
		t.Fatal(err)
	}
	// COSTA boards; SMITH does not. The hold reports SMITH's bag and one of
	// COSTA's two loaded.
	if _, err := s.Board(ctx, testKey, costa.Passengers[0].ID); err != nil {
		t.Fatal(err)
	}
	bpm := s.BSMFor(mustFlight(t, s), smith.Passengers[0], "")
	bpm.Kind = "BPM"
	bpm.Tags = append(bpm.Tags, struct {
		Number string
		Count  int
	}{costa.Passengers[0].Bags[0].Tag, 1})
	if _, _, err := s.ApplyBagReport(ctx, bpm); err != nil {
		t.Fatal(err)
	}

	// With reconciliation required the door stays open and names the bag.
	_, err = s.CloseFlight(ctx, testKey, CloseOptions{Force: true, RequireReconciled: true})
	var ue *UnreconciledError
	if !errors.As(err, &ue) || !errors.Is(err, ErrUnreconciledBags) {
		t.Fatalf("a loaded bag without its passenger must hold the door: %v", err)
	}
	if len(ue.Bags) != 1 || ue.Bags[0].Tag != smith.Passengers[0].Bags[0].Tag || ue.Bags[0].Surname != "SMITH" {
		t.Fatalf("the unaccompanied bag is SMITH's: %+v", ue.Bags)
	}

	// Without it the flight closes, the report says what happened, and the
	// unaccompanied bag has come off the load.
	cl, err := s.CloseFlight(ctx, testKey, CloseOptions{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	r := cl.Reconciliation
	if r.Loaded != 2 || r.Boarded != 1 || len(r.Unaccompanied) != 1 || r.Unaccompanied[0].Surname != "SMITH" {
		t.Fatalf("two loaded, one boarded, SMITH's unaccompanied: %+v", r)
	}
	if len(r.NotLoaded) != 1 || r.NotLoaded[0].Tag != costa.Passengers[0].Bags[1].Tag {
		t.Fatalf("COSTA's second bag was never loaded: %+v", r.NotLoaded)
	}
	if r.Clear() {
		t.Fatal("a flight with an unaccompanied bag is not clear")
	}
	for _, p := range cl.Flight.Passengers {
		if p.Surname == "SMITH" {
			for _, b := range p.Bags {
				if b.Loaded || !b.Offloaded {
					t.Fatalf("SMITH's bag must be off the aircraft: %+v", b)
				}
			}
		}
	}
}
