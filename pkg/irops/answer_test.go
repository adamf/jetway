package irops

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/adamf/jetway/pkg/avail"
	"github.com/adamf/jetway/pkg/gateway"
	"github.com/adamf/jetway/pkg/inventory"
	"github.com/adamf/jetway/pkg/pnr"
	"github.com/adamf/jetway/pkg/queue"
	"github.com/adamf/jetway/pkg/store"
)

// carrierWithSeats is a distribution system wired to a carrier that answers
// from a real seat inventory: BA0175 and BA0179 out of LHR, with the seats
// given, so a sell is confirmed, waitlisted or refused by what is left.
func carrierWithSeats(t *testing.T, seats map[string]int) (*gateway.Gateway, *queue.Manager, *inventory.Inventory) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	gdsStore, airStore := store.NewMem(), store.NewMem()
	gds := gateway.New(gateway.Identity{Designator: "1G", TTYAddress: "LONRM1G", Name: "gds"}, gdsStore, gateway.NewBus(64), log, []byte("g"))
	air := gateway.New(gateway.Identity{Designator: "BA", TTYAddress: "LHRRMBA", Name: "BA-res"}, airStore, gateway.NewBus(64), log, []byte("a"))
	inv := inventory.New("BA", func(carrier, flight, date, board string) (map[string]int, bool) {
		n, ok := seats[flight]
		if !ok {
			return nil, false
		}
		return map[string]int{"Y": n}, true
	})
	air.Responder = inv
	gds.AddPeer(&gateway.Peer{Name: "BA", Carrier: "BA", Format: store.FormatTypeB, TTYAddress: "LHRRMBA"})
	air.AddPeer(&gateway.Peer{Name: "1G", Carrier: "1G", Format: store.FormatTypeB, TTYAddress: "LONRM1G"})
	gds.Sender = gateway.SenderFunc(func(ctx context.Context, peer string, raw []byte) error {
		_, err := air.Ingest(ctx, "1G", raw)
		return err
	})
	air.Sender = gateway.SenderFunc(func(ctx context.Context, peer string, raw []byte) error {
		_, err := gds.Ingest(ctx, "BA", raw)
		return err
	})
	gds.Avail = avail.NewCache() // empty: every alternative is an ask
	q := &queue.Manager{Store: gdsStore, Log: log}
	gds.Queues = q
	return gds, q, inv
}

func strand(t *testing.T, gds *gateway.Gateway) (locator string) {
	t.Helper()
	ctx := context.Background()
	res, err := gds.Book(ctx, &gateway.BookingRequest{
		Passengers: []gateway.BookingPassenger{{Surname: "STRANDED", Given: "ANN", Title: "MS"}},
		Segments:   []gateway.BookingSegment{{Carrier: "BA", FlightNum: "0117", Class: "Y", Date: "16DEC", Board: "LHR", Off: "JFK", Seats: 1}},
		Agent:      "test", Channel: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gds.Ingest(ctx, "BA", []byte("QU LONRM1G\n.LHRRMBA 161200\nASM\nUTC\nCNL\nBA0117/16DEC\nLHR JFK\n")); err != nil {
		t.Fatal(err)
	}
	return res.PNR.RecordLocator
}

func alternatives(day time.Time) Schedule {
	return ScheduleFunc(func(ctx context.Context, dead pnr.Segment) ([]Candidate, error) {
		return []Candidate{
			{Carrier: "BA", FlightNum: "0175", Board: "LHR", Off: "JFK", Depart: day, DepartTime: "1300"},
			{Carrier: "BA", FlightNum: "0179", Board: "LHR", Off: "JFK", Depart: day, DepartTime: "1800"},
		}, nil
	})
}

func statuses(t *testing.T, st store.Store, locator string) map[string]string {
	t.Helper()
	rec, err := st.GetPNR(context.Background(), locator)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, s := range rec.Segments {
		out[s.FlightNum] = s.Status
	}
	return out
}

// The engine waits for the carrier's answer. The first alternative is full
// and waitlists the passenger; the second confirms; the dead leg and the
// waitlist both come off and the item is worked.
func TestEngineTakesTheConfirmedSeatOverTheWaitlist(t *testing.T) {
	ctx := context.Background()
	gds, q, _ := carrierWithSeats(t, map[string]int{"0117": 9, "0175": 0, "0179": 9})
	loc := strand(t, gds)
	day := depart(t, "16DEC")
	var out []Outcome
	e := &Engine{Gateway: gds, Store: gds.Store, Queues: q, By: "irops", AskCarriers: true, ReplyTimeout: 2 * time.Second,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Schedule: alternatives(day),
		OnRebooked: func(ctx context.Context, item *store.QueueItem, o Outcome) { out = append(out, o) }}
	moved, err := e.Work(ctx)
	if err != nil || moved != 1 || len(out) != 1 {
		t.Fatalf("moved %d out %v err %v", moved, out, err)
	}
	if !strings.Contains(out[0].To, "BA0179") || out[0].Tried != 2 {
		t.Errorf("outcome %+v", out[0])
	}
	got := statuses(t, gds.Store, loc)
	if got["0117"] != "XX" || got["0175"] != "XX" || got["0179"] != "HK" {
		t.Errorf("the passenger should hold 0179 only: %v", got)
	}
	items, _ := gds.Store.ListQueue(ctx, store.QueueFilter{Queue: store.QueueScheduleChange})
	for _, it := range items {
		if it.Pending() {
			t.Errorf("a rebooked item is still pending: %+v", it)
		}
	}
}

// When every alternative is full the passenger is waitlisted where a seat
// may free up, keeps the dead leg until then, and the item stays for a
// person with the waitlists named.
func TestEngineWaitlistsWhenNothingConfirms(t *testing.T) {
	ctx := context.Background()
	gds, q, _ := carrierWithSeats(t, map[string]int{"0117": 9, "0175": 0, "0179": 0})
	loc := strand(t, gds)
	day := depart(t, "16DEC")
	e := &Engine{Gateway: gds, Store: gds.Store, Queues: q, By: "irops", AskCarriers: true, ReplyTimeout: 2 * time.Second,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Schedule: alternatives(day)}
	items, _ := gds.Store.ListQueue(ctx, store.QueueFilter{Queue: store.QueueScheduleChange})
	if len(items) != 1 {
		t.Fatalf("queue: %+v", items)
	}
	out, err := e.Rebook(ctx, items[0])
	if !errors.Is(err, ErrWaitlisted) || out == nil || len(out.Waitlisted) != 2 {
		t.Fatalf("expected two waitlists and ErrWaitlisted: %+v %v", out, err)
	}
	got := statuses(t, gds.Store, loc)
	if got["0117"] == "XX" || got["0175"] != "HL" || got["0179"] != "HL" {
		t.Errorf("the passenger should keep the dead leg and hold two waitlists: %v", got)
	}
	items, _ = gds.Store.ListQueue(ctx, store.QueueFilter{Queue: store.QueueScheduleChange})
	if len(items) != 1 || !items[0].Pending() {
		t.Errorf("the item should stay for a person: %+v", items)
	}
	// A cabin that refuses outright is not held either.
	gds2, q2, _ := carrierWithSeats(t, map[string]int{"0117": 9, "0175": 0, "0179": 0})
	loc2 := strand(t, gds2)
	// Fill both waitlists first so the engine is refused, not waitlisted.
	for i := 0; i < 2; i++ {
		for _, fl := range []string{"0175", "0179"} {
			if _, err := gds2.Book(ctx, &gateway.BookingRequest{
				Passengers: []gateway.BookingPassenger{{Surname: "EARLIER", Given: "BOB", Title: "MR"}},
				Segments:   []gateway.BookingSegment{{Carrier: "BA", FlightNum: fl, Class: "Y", Date: "16DEC", Board: "LHR", Off: "JFK", Seats: 1}},
				Agent:      "test", Channel: "test"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	e2 := &Engine{Gateway: gds2, Store: gds2.Store, Queues: q2, By: "irops", AskCarriers: true, ReplyTimeout: 2 * time.Second,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Schedule: alternatives(day)}
	items2, _ := gds2.Store.ListQueue(ctx, store.QueueFilter{Queue: store.QueueScheduleChange})
	if _, err := e2.Rebook(ctx, items2[0]); !errors.Is(err, ErrNoAlternative) {
		t.Fatalf("refused everywhere should be ErrNoAlternative: %v", err)
	}
	got2 := statuses(t, gds2.Store, loc2)
	if got2["0117"] == "XX" || got2["0175"] != "XX" || got2["0179"] != "XX" {
		t.Errorf("refused requests should come off the record, the dead leg stay: %v", got2)
	}
}
