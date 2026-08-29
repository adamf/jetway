package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/adamf/jetway/internal/store"
	"github.com/adamf/jetway/pkg/pnr"
)

func partyRecord(t *testing.T, gw *Gateway, locator string, pax int) *pnr.PNR {
	t.Helper()
	now := time.Now().UTC()
	rec := &pnr.PNR{
		RecordLocator: locator, Status: pnr.StatusOpen, CreatedAt: now, UpdatedAt: now,
		Origin: pnr.Origin{Party: "1J"},
		Segments: []pnr.Segment{
			{Ref: 1, Type: pnr.SegmentAir, Carrier: "AA", FlightNum: "0050", Class: "Y",
				Board: "DFW", Off: "LHR", Status: "HK", Seats: pax, WireDate: "15DEC",
				Depart: now.AddDate(0, 1, 0)},
			{Ref: 2, Type: pnr.SegmentAir, Carrier: "BA", FlightNum: "0117", Class: "Y",
				Board: "LHR", Off: "JFK", Status: "HK", Seats: pax, WireDate: "16DEC",
				Depart: now.AddDate(0, 1, 1)},
		},
		Locators: []pnr.ExternalLocator{{Owner: "AA", Value: "AA1234"}, {Owner: "BA", Value: "BA5678"}},
	}
	for i := 1; i <= pax; i++ {
		rec.Passengers = append(rec.Passengers, pnr.Passenger{
			Ref: i, Surname: "PAX", Given: string(rune('A' + i - 1)),
		})
	}
	if err := gw.Store.CreatePNR(context.Background(), rec, nil); err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestSplitCreatesALiveChild(t *testing.T) {
	gw, _ := cancelNode(t)
	ctx := context.Background()
	partyRecord(t, gw, "SPL001", 3)

	res, err := gw.Split(ctx, SplitRequest{Locator: "SPL001", Passengers: []int{3}, By: "adam"})
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	if res.Child.RecordLocator == "" || res.Child.RecordLocator == "SPL001" {
		t.Fatalf("child locator = %q", res.Child.RecordLocator)
	}

	// Both are ordinary live records afterwards; neither is in some third state.
	if res.Parent.Status != pnr.StatusOpen || res.Child.Status != pnr.StatusOpen {
		t.Errorf("statuses = %q / %q, want both open", res.Parent.Status, res.Child.Status)
	}
	if res.Child.SplitFrom != "SPL001" {
		t.Errorf("SplitFrom = %q", res.Child.SplitFrom)
	}
	if len(res.Parent.SplitTo) != 1 {
		t.Errorf("SplitTo = %v", res.Parent.SplitTo)
	}

	// Both are readable from the store, which is the point of writing them.
	stored, err := gw.Store.GetPNR(ctx, res.Child.RecordLocator)
	if err != nil {
		t.Fatalf("the child was not persisted: %v", err)
	}
	if len(stored.Passengers) != 1 {
		t.Errorf("stored child has %d passengers", len(stored.Passengers))
	}
	parent, _ := gw.Store.GetPNR(ctx, "SPL001")
	if len(parent.Passengers) != 2 {
		t.Errorf("stored parent has %d passengers, want 2", len(parent.Passengers))
	}
	for _, s := range parent.Segments {
		if s.Seats != 2 {
			t.Errorf("parent still holds %d seats", s.Seats)
		}
	}
}

func TestSplitKeepsTheCarrierReference(t *testing.T) {
	gw, _ := cancelNode(t)
	ctx := context.Background()
	partyRecord(t, gw, "SPL002", 2)

	res, err := gw.Split(ctx, SplitRequest{Locator: "SPL002", Passengers: []int{2}, By: "adam"})
	if err != nil {
		t.Fatal(err)
	}
	// The carrier holds one booking. Giving the child no reference would be
	// tidier and false: an agent ringing them would find nothing.
	if len(res.Child.Locators) != 2 {
		t.Fatalf("child carrier locators = %+v, want both", res.Child.Locators)
	}
	found := map[string]string{}
	for _, l := range res.Child.Locators {
		found[l.Owner] = l.Value
	}
	if found["AA"] != "AA1234" || found["BA"] != "BA5678" {
		t.Errorf("child locators = %v", found)
	}
}

func TestSplitSurfacesThatCarriersHaveNotBeenTold(t *testing.T) {
	gw, _ := cancelNode(t)
	ctx := context.Background()
	partyRecord(t, gw, "SPL003", 2)

	res, err := gw.Split(ctx, SplitRequest{Locator: "SPL003", Passengers: []int{2}, By: "adam"})
	if err != nil {
		t.Fatal(err)
	}
	// Not an error: it is the state of the world until the divide message
	// exists. But it must not be silent.
	if len(res.Unadvised) != 2 {
		t.Errorf("Unadvised = %v, want both carriers", res.Unadvised)
	}
	items, err := gw.Store.ListQueue(ctx, store.QueueFilter{Queue: store.QueueDivergence})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("expected one divergence per carrier, got %d", len(items))
	}
	for _, it := range items {
		if it.Locator != res.Child.RecordLocator {
			t.Errorf("divergence filed against %s, want the child", it.Locator)
		}
	}
}

func TestSplitRefusals(t *testing.T) {
	gw, _ := cancelNode(t)
	ctx := context.Background()
	partyRecord(t, gw, "SPL004", 2)

	if _, err := gw.Split(ctx, SplitRequest{Locator: "SPL004"}); err == nil {
		t.Error("a split with no passengers should be refused")
	}
	if _, err := gw.Split(ctx, SplitRequest{Locator: "SPL004", Passengers: []int{1, 2}}); err == nil {
		t.Error("splitting every passenger should be refused")
	}
	if _, err := gw.Split(ctx, SplitRequest{Locator: "NOSUCH", Passengers: []int{1}}); err == nil {
		t.Error("splitting a record that does not exist should be refused")
	}

	rec, _ := gw.Store.GetPNR(ctx, "SPL004")
	rec.Status = pnr.StatusCancelled
	if err := gw.Store.UpdatePNR(ctx, rec, rec.Version, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := gw.Split(ctx, SplitRequest{Locator: "SPL004", Passengers: []int{1}}); err == nil {
		t.Error("splitting a cancelled record should be refused")
	}
}

func TestSplitTicketsFollowTheirPassenger(t *testing.T) {
	gw, _ := cancelNode(t)
	ctx := context.Background()
	partyRecord(t, gw, "SPL005", 2)
	if _, err := gw.IssueTickets(ctx, "SPL005", IssueOptions{AirlineCode: "125", IssuedBy: "adam"}); err != nil {
		t.Fatal(err)
	}

	res, err := gw.Split(ctx, SplitRequest{Locator: "SPL005", Passengers: []int{2}, By: "adam"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Child.Tickets) != 1 || len(res.Parent.Tickets) != 1 {
		t.Fatalf("tickets: parent %d, child %d", len(res.Parent.Tickets), len(res.Child.Tickets))
	}
	// A document belongs to a person, and the person moved.
	if res.Child.Tickets[0].PaxRef != 1 {
		t.Errorf("child ticket points at passenger %d on a one-passenger record",
			res.Child.Tickets[0].PaxRef)
	}
	if res.Child.Tickets[0].Number.Compact() == res.Parent.Tickets[0].Number.Compact() {
		t.Error("the two halves ended up sharing a document number")
	}
}
