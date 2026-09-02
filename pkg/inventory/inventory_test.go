package inventory

import (
	"context"
	"testing"
	"time"

	"github.com/adamf/jetway/pkg/avail"
	"github.com/adamf/jetway/pkg/pnr"
)

func b737() Capacity {
	return func(carrier, flight, date string) (map[string]int, bool) {
		if carrier == "WN" && flight == "2554" {
			return map[string]int{"Y": 174}, true
		}
		if carrier == "WN" && flight == "1" {
			return map[string]int{"F": 8, "C": 24, "Y": 200}, true
		}
		return nil, false
	}
}

func seg(flight, class, status string, seats int) pnr.Segment {
	return pnr.Segment{Ref: 1, Type: pnr.SegmentAir, Carrier: "WN", FlightNum: flight, WireDate: "26NOV",
		Board: "BNA", Off: "DCA", Class: class, Status: status, Seats: seats}
}

func decide(t *testing.T, inv *Inventory, s pnr.Segment) string {
	t.Helper()
	out, err := inv.Decide(context.Background(), &pnr.PNR{Segments: []pnr.Segment{s}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return out[s.Key()]
}

// A cabin is a pool: every class in it sells from the same seats, the last
// seat confirms, the next request waitlists, and the waitlist has a floor.
func TestCabinIsOnePool(t *testing.T) {
	inv := New("WN", b737())
	for i := 0; i < 43; i++ {
		if got := decide(t, inv, seg("2554", []string{"Y", "M", "Q", "J"}[i%4], "HN", 4)); got != "KK" {
			t.Fatalf("sell %d: %s", i, got)
		}
	}
	if got := decide(t, inv, seg("2554", "Y", "HN", 2)); got != "KK" {
		t.Fatalf("the last two seats should confirm: %s", got)
	}
	if got := decide(t, inv, seg("2554", "Y", "HN", 1)); got != "US" {
		t.Fatalf("a full cabin waitlists: %s", got)
	}
	for i := 0; i < 16; i++ {
		decide(t, inv, seg("2554", "K", "HN", 1))
	}
	if got := decide(t, inv, seg("2554", "Y", "HN", 1)); got != "UC" {
		t.Fatalf("a full waitlist refuses: %s", got)
	}
	av := inv.Availability([]avail.Key{avail.NewKey("WN", "2554", time.Date(2025, 11, 26, 0, 0, 0, 0, time.UTC), "BNA", "DCA", "Y")}, time.Now())
	if len(av) != 1 || av[0].Status != avail.Closed || av[0].Seats != 0 || !av[0].SeatsKnown {
		t.Errorf("availability of a full, waitlist-closed cabin: %+v", av)
	}
	if got := decide(t, inv, seg("9999", "Y", "HN", 1)); got != "UN" {
		t.Errorf("a flight the carrier does not fly: %s", got)
	}
}

// Classes fall into the cabins the aircraft has; a three-cabin aircraft
// keeps its cabins apart.
func TestClassesMapToCabins(t *testing.T) {
	inv := New("WN", b737())
	if got := decide(t, inv, seg("1", "F", "HN", 8)); got != "KK" {
		t.Fatal(got)
	}
	if got := decide(t, inv, seg("1", "A", "HN", 1)); got != "US" {
		t.Errorf("first is full, A should waitlist: %s", got)
	}
	if got := decide(t, inv, seg("1", "J", "HN", 1)); got != "KK" {
		t.Errorf("business is open: %s", got)
	}
	day := time.Date(2025, 11, 26, 0, 0, 0, 0, time.UTC)
	av := inv.Availability([]avail.Key{
		avail.NewKey("WN", "1", day, "BNA", "DCA", "F"), avail.NewKey("WN", "1", day, "BNA", "DCA", "C"), avail.NewKey("WN", "1", day, "BNA", "DCA", "Y")}, time.Now())
	if len(av) != 3 || av[0].Status != avail.Waitlist || av[1].Seats != 23 || av[2].Seats != 200 {
		t.Errorf("availability by cabin: %+v", av)
	}
}

// The inventory is rebuilt from the book of record: seeded seats are sold
// seats, and a cancellation gives them back.
func TestSeedAndRelease(t *testing.T) {
	inv := New("WN", b737())
	for i := 0; i < 170; i++ {
		inv.Seed(seg("2554", "Y", "HK", 1))
	}
	inv.Seed(seg("2554", "Y", "HL", 1))
	if got := decide(t, inv, seg("2554", "Y", "HN", 5)); got != "US" {
		t.Fatalf("170 seeded of 174, a party of five waitlists: %s", got)
	}
	inv.Release(context.Background(), seg("2554", "Y", "XX", 10), "HK")
	if got := decide(t, inv, seg("2554", "Y", "HN", 5)); got != "KK" {
		t.Fatalf("ten seats released, five should confirm: %s", got)
	}
	snap := inv.Snapshot()
	if len(snap) != 1 || snap[0].Seats != 174 || snap[0].Sold != 165 || snap[0].Waitlisted != 6 {
		t.Errorf("snapshot: %+v", snap)
	}
	inv.Reset()
	if got := decide(t, inv, seg("2554", "Y", "HN", 174)); got != "KK" {
		t.Errorf("after a reset the whole cabin is open: %s", got)
	}
}
