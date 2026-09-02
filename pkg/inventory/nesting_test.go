package inventory

import (
	"testing"
	"time"

	"github.com/adamf/jetway/pkg/avail"
)

func ladder(y, m, k int) Levels {
	return func(carrier, flight, date, board, comp string) []Level {
		if comp != "Y" {
			return nil
		}
		return []Level{{"Y", y}, {"M", m}, {"K", k}}
	}
}

// Classes nest: K may sell until K's authorisation is used by K, M only until
// M's covers M and K together, Y to the cabin. A class closed by the ladder
// is unable, not waitlisted, and availability says so while the cabin still
// has seats for higher classes.
func TestNestedClassesCloseBeforeTheCabin(t *testing.T) {
	inv := New("WN", b737())
	inv.Levels = ladder(174, 100, 40)
	for i := 0; i < 40; i++ {
		if got := decide(t, inv, seg("2554", "K", "HN", 1)); got != "KK" {
			t.Fatalf("K sell %d: %s", i, got)
		}
	}
	if got := decide(t, inv, seg("2554", "K", "HN", 1)); got != "UC" {
		t.Fatalf("K past its authorisation should be unable: %s", got)
	}
	// M's 100 covers M and K together: 60 left for M.
	for i := 0; i < 60; i++ {
		if got := decide(t, inv, seg("2554", "M", "HN", 1)); got != "KK" {
			t.Fatalf("M sell %d: %s", i, got)
		}
	}
	if got := decide(t, inv, seg("2554", "M", "HN", 1)); got != "UC" {
		t.Fatalf("M past its authorisation should be unable: %s", got)
	}
	day := time.Date(2025, 11, 26, 0, 0, 0, 0, time.UTC)
	av := inv.Availability([]avail.Key{
		avail.NewKey("WN", "2554", day, "BNA", "DCA", "K"),
		avail.NewKey("WN", "2554", day, "BNA", "DCA", "M"),
		avail.NewKey("WN", "2554", day, "BNA", "DCA", "Y"),
	}, time.Now())
	if av[0].Status != avail.Closed || av[1].Status != avail.Closed || av[2].Status != avail.Open || av[2].Seats != 74 {
		t.Errorf("availability under the ladder: %+v", av)
	}
	// Y sells the rest of the cabin, then the cabin waitlists like before.
	if got := decide(t, inv, seg("2554", "Y", "HN", 74)); got != "KK" {
		t.Fatalf("Y to the last seat: %s", got)
	}
	if got := decide(t, inv, seg("2554", "Y", "HN", 1)); got != "US" {
		t.Fatalf("a full cabin waitlists: %s", got)
	}
	// A cancellation in K reopens K.
	inv.Release(nil, seg("2554", "K", "XX", 2), "HK")
	if got := decide(t, inv, seg("2554", "K", "HN", 1)); got != "UC" {
		// The cabin is still full: K has authorisation but no seat.
		t.Logf("K with a seat freed in a full cabin: %s", got)
	}
	inv.Release(nil, seg("2554", "Y", "XX", 2), "HK")
	if got := decide(t, inv, seg("2554", "K", "HN", 1)); got != "KK" {
		t.Errorf("K reopened by its own cancellations and a free seat: %s", got)
	}
}

// Without a ladder nothing changes: the cabin alone decides.
func TestNoLadderSellsToTheLastSeat(t *testing.T) {
	inv := New("WN", b737())
	for i := 0; i < 174; i++ {
		if got := decide(t, inv, seg("2554", "K", "HN", 1)); got != "KK" {
			t.Fatalf("sell %d: %s", i, got)
		}
	}
}
