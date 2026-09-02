package irops

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/adamf/jetway/pkg/avail"
	"github.com/adamf/jetway/pkg/gateway"
	"github.com/adamf/jetway/pkg/pnr"
	"github.com/adamf/jetway/pkg/queue"
	"github.com/adamf/jetway/pkg/store"
)

// wire records what left the distribution system for each carrier link.
type wire struct {
	mu   sync.Mutex
	sent map[string][]string
}

func (w *wire) send(ctx context.Context, peer string, raw []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sent[peer] = append(w.sent[peer], string(raw))
	return nil
}

func (w *wire) texts(peer string) []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.sent[peer]...)
}

// gds is a distribution system with one Type B carrier link, an
// availability cache and a queue manager -- what the engine runs inside.
func gds(t *testing.T) (*gateway.Gateway, *wire, *queue.Manager) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := store.NewMem()
	gw := gateway.New(gateway.Identity{Designator: "1G", TTYAddress: "LONRM1G", Name: "gds"},
		st, gateway.NewBus(64), log, []byte("secret"))
	gw.AddPeer(&gateway.Peer{Name: "BA", Carrier: "BA", Format: store.FormatTypeB, TTYAddress: "LHRRMBA"})
	w := &wire{sent: map[string][]string{}}
	gw.Sender = gateway.SenderFunc(w.send)
	gw.Avail = avail.NewCache()
	q := &queue.Manager{Store: st, Log: log}
	gw.Queues = q
	return gw, w, q
}

func depart(t *testing.T, wire string) time.Time {
	t.Helper()
	d, err := pnr.ResolveDate(wire, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// A cancelled flight's passenger is moved to the next flight over the same
// city pair that has a seat: the new leg is sold on the record, only the
// new leg is requested from the carrier, the dead leg is cancelled, and the
// queue item is worked with a note saying so.
func TestEngineRebooksACancelledFlight(t *testing.T) {
	ctx := context.Background()
	gw, w, q := gds(t)
	day := depart(t, "16DEC")
	// BA0117 is the passenger's flight; BA0175 later the same day is full;
	// BA0179 later still has seats on free sale.
	for _, e := range []avail.Entry{
		{Key: avail.NewKey("BA", "0117", day, "LHR", "JFK", "Y"), Status: avail.Open, Seats: 9, SeatsKnown: true, Source: avail.SourceAVS},
		{Key: avail.NewKey("BA", "0175", day, "LHR", "JFK", "Y"), Status: avail.Closed, Source: avail.SourceAVS},
		{Key: avail.NewKey("BA", "0179", day, "LHR", "JFK", "Y"), Status: avail.Open, Seats: 9, SeatsKnown: true, Source: avail.SourceAVS},
	} {
		gw.Avail.Put(e)
	}
	res, err := gw.Book(ctx, &gateway.BookingRequest{
		Passengers: []gateway.BookingPassenger{{Surname: "STRANDED", Given: "ANN", Title: "MS"}},
		Segments:   []gateway.BookingSegment{{Carrier: "BA", FlightNum: "0117", Class: "Y", Date: "16DEC", Board: "LHR", Off: "JFK", Seats: 1}},
		Agent:      "test", Channel: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	loc := res.PNR.RecordLocator
	sellsBefore := len(w.texts("BA"))

	// The carrier cancels BA0117: the ASM lands and queues the booking.
	asm := []byte("QU LONRM1G\n.LHRRMBA 161200\nASM\nUTC\nCNL\nBA0117/16DEC\nLHR JFK\n")
	if _, err := gw.Ingest(ctx, "BA", asm); err != nil {
		t.Fatal(err)
	}
	items, _ := gw.Store.ListQueue(ctx, store.QueueFilter{Queue: store.QueueScheduleChange})
	if len(items) != 1 || items[0].Code != "schedule_cnl" {
		t.Fatalf("queue after the ASM: %+v", items)
	}

	var notified []Outcome
	e := &Engine{
		Gateway: gw, Store: gw.Store, Queues: q, By: "irops", Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Schedule: ScheduleFunc(func(ctx context.Context, dead pnr.Segment) ([]Candidate, error) {
			if dead.Board != "LHR" || dead.Off != "JFK" {
				t.Errorf("asked for alternatives to %s", dead.Describe())
			}
			return []Candidate{
				{Carrier: "BA", FlightNum: "0175", Board: "LHR", Off: "JFK", Depart: day, DepartTime: "1300"},
				{Carrier: "BA", FlightNum: "0179", Board: "LHR", Off: "JFK", Depart: day, DepartTime: "1800"},
			}, nil
		}),
		OnRebooked: func(ctx context.Context, item *store.QueueItem, out Outcome) { notified = append(notified, out) },
	}
	moved, err := e.Work(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if moved != 1 || len(notified) != 1 {
		t.Fatalf("moved %d, notified %d", moved, len(notified))
	}
	out := notified[0]
	if out.Locator != loc || !strings.Contains(out.To, "BA0179") || !strings.Contains(out.From, "BA0117") {
		t.Errorf("outcome %+v", out)
	}
	if out.Tried != 1 {
		t.Errorf("the closed flight must not be asked for: tried %d", out.Tried)
	}

	rec, _ := gw.Store.GetPNR(ctx, loc)
	var live, dead *pnr.Segment
	for i := range rec.Segments {
		switch rec.Segments[i].FlightNum {
		case "0179":
			live = &rec.Segments[i]
		case "0117":
			dead = &rec.Segments[i]
		}
	}
	if live == nil || live.Status != "HK" {
		t.Errorf("the new leg should be held on free sale: %+v", live)
	}
	if dead == nil || dead.Status != "XX" {
		t.Errorf("the dead leg should be cancelled: %+v", dead)
	}
	if rec.Status == pnr.StatusCancelled {
		t.Error("the record is alive on its new leg")
	}

	// The carrier was asked for the new leg alone, then told the old one is off.
	after := w.texts("BA")[sellsBefore:]
	if len(after) < 2 {
		t.Fatalf("carrier saw %d messages, want the sell and the cancel:\n%s", len(after), strings.Join(after, "\n---\n"))
	}
	sell := after[0]
	if !strings.Contains(sell, "BA0179") || strings.Contains(sell, "BA0117") {
		t.Errorf("the sell must carry only the new leg:\n%s", sell)
	}
	if !strings.Contains(after[1], "BA0117") {
		t.Errorf("the cancel must name the dead leg:\n%s", after[1])
	}

	items, _ = gw.Store.ListQueue(ctx, store.QueueFilter{Queue: store.QueueScheduleChange, IncludeWorked: true})
	if len(items) != 1 || items[0].Pending() || !strings.Contains(items[0].Note, "BA0179") {
		t.Errorf("queue item after rebooking: %+v", items)
	}
	// A second pass finds nothing to do.
	if moved, _ := e.Work(ctx); moved != 0 {
		t.Errorf("second pass moved %d", moved)
	}
}

// With no seat anywhere, the item stays on the queue for a person, and the
// engine does not hammer it on every pass.
func TestEngineLeavesTheUnsolvableForAPerson(t *testing.T) {
	ctx := context.Background()
	gw, _, q := gds(t)
	day := depart(t, "16DEC")
	gw.Avail.Put(avail.Entry{Key: avail.NewKey("BA", "0117", day, "LHR", "JFK", "Y"), Status: avail.Open, Seats: 9, SeatsKnown: true, Source: avail.SourceAVS})
	gw.Avail.Put(avail.Entry{Key: avail.NewKey("BA", "0175", day, "LHR", "JFK", "Y"), Status: avail.Closed, Source: avail.SourceAVS})
	if _, err := gw.Book(ctx, &gateway.BookingRequest{
		Passengers: []gateway.BookingPassenger{{Surname: "STUCK", Given: "BOB", Title: "MR"}},
		Segments:   []gateway.BookingSegment{{Carrier: "BA", FlightNum: "0117", Class: "Y", Date: "16DEC", Board: "LHR", Off: "JFK", Seats: 1}},
		Agent:      "test", Channel: "test",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := gw.Ingest(ctx, "BA", []byte("QU LONRM1G\n.LHRRMBA 161200\nASM\nUTC\nCNL\nBA0117/16DEC\nLHR JFK\n")); err != nil {
		t.Fatal(err)
	}
	asked := 0
	e := &Engine{Gateway: gw, Store: gw.Store, Queues: q, By: "irops", Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Schedule: ScheduleFunc(func(ctx context.Context, dead pnr.Segment) ([]Candidate, error) {
			asked++
			return []Candidate{{Carrier: "BA", FlightNum: "0175", Board: "LHR", Off: "JFK", Depart: day}}, nil
		})}
	for i := 0; i < 3; i++ {
		if moved, err := e.Work(ctx); err != nil || moved != 0 {
			t.Fatalf("pass %d: moved %d, %v", i, moved, err)
		}
	}
	if asked != 1 {
		t.Errorf("the schedule was asked %d times for an item nothing can be done about", asked)
	}
	items, _ := gw.Store.ListQueue(ctx, store.QueueFilter{Queue: store.QueueScheduleChange})
	if len(items) != 1 || !items[0].Pending() {
		t.Errorf("the item must stay pending for a person: %+v", items)
	}
}
