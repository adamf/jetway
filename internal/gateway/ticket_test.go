package gateway

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/adamf/jetway/internal/queue"
	"github.com/adamf/jetway/internal/store"
	"github.com/adamf/jetway/pkg/pnr"
)

func ticketNode(t *testing.T) *Gateway {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := store.NewMem()
	gw := New(Identity{Designator: "1J", TTYAddress: "LONRM1J", Name: "jetway"},
		st, NewBus(100), log, []byte("secret"))
	gw.Queues = &queue.Manager{Store: st, Log: log}
	return gw
}

func recordToTicket(t *testing.T, gw *Gateway, locator string, pax int, segs int, deadline *time.Time) *pnr.PNR {
	t.Helper()
	now := time.Now().UTC()
	rec := &pnr.PNR{
		RecordLocator: locator, Status: pnr.StatusOpen, CreatedAt: now, UpdatedAt: now,
	}
	for i := 1; i <= pax; i++ {
		rec.Passengers = append(rec.Passengers, pnr.Passenger{Ref: i, Surname: "PAX", Given: "T"})
	}
	for i := 1; i <= segs; i++ {
		rec.Segments = append(rec.Segments, pnr.Segment{
			Ref: i, Type: pnr.SegmentAir, Carrier: "BA", FlightNum: "0117",
			Board: "LHR", Off: "JFK", Status: "HK", Depart: now.AddDate(0, 1, 0),
		})
	}
	if deadline != nil {
		rec.Ticketing = []pnr.Ticketing{{Text: "TKTL", Deadline: deadline}}
	}
	if err := gw.Store.CreatePNR(context.Background(), rec, nil); err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestIssueTicketsPerPassenger(t *testing.T) {
	gw := ticketNode(t)
	ctx := context.Background()
	recordToTicket(t, gw, "ISS001", 2, 2, nil)

	rec, err := gw.IssueTickets(ctx, "ISS001", IssueOptions{AirlineCode: "125", IssuedBy: "adam"})
	if err != nil {
		t.Fatalf("IssueTickets: %v", err)
	}
	if len(rec.Tickets) != 2 {
		t.Fatalf("issued %d tickets, want one per passenger", len(rec.Tickets))
	}
	seen := map[string]bool{}
	for _, tk := range rec.Tickets {
		if tk.Number.AirlineCode != "125" {
			t.Errorf("wrong stock: %+v", tk.Number)
		}
		if !tk.Number.CheckDigitOK() {
			t.Errorf("issued a number failing its own check digit: %s", tk.Number)
		}
		if seen[tk.Number.Compact()] {
			t.Errorf("issued the same document number twice: %s", tk.Number)
		}
		seen[tk.Number.Compact()] = true
		if len(tk.Coupons) != 2 {
			t.Errorf("coupons = %d, want one per segment", len(tk.Coupons))
		}
	}
	if !rec.Ticketed() {
		t.Error("record should be ticketed")
	}
	if rec.Status != pnr.StatusTicketed {
		t.Errorf("Status = %q, want ticketed", rec.Status)
	}
}

func TestIssueIsIdempotent(t *testing.T) {
	gw := ticketNode(t)
	ctx := context.Background()
	recordToTicket(t, gw, "ISS002", 1, 1, nil)

	first, err := gw.IssueTickets(ctx, "ISS002", IssueOptions{AirlineCode: "125"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := gw.IssueTickets(ctx, "ISS002", IssueOptions{AirlineCode: "125"})
	if err != nil {
		t.Fatal(err)
	}
	// Two live documents against one coupon is a refund problem, not a
	// booking one.
	if len(second.Tickets) != 1 || second.Tickets[0].Number != first.Tickets[0].Number {
		t.Errorf("issuing twice produced %+v", second.Tickets)
	}
}

func TestIssuanceSatisfiesTheTimeLimit(t *testing.T) {
	gw := ticketNode(t)
	ctx := context.Background()
	deadline := time.Now().UTC().Add(2 * time.Hour)
	rec := recordToTicket(t, gw, "ISS003", 1, 1, &deadline)

	// The sweeper raises the approaching limit first.
	sw := &queue.Sweeper{Records: gw.Store, Queues: gw.Queues}
	if _, err := sw.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	items, _ := gw.Store.ListQueue(ctx, store.QueueFilter{Queue: store.QueueTicketing})
	if len(items) != 1 {
		t.Fatalf("expected the limit to be raised, got %d items", len(items))
	}

	if _, err := gw.IssueTickets(ctx, rec.RecordLocator, IssueOptions{AirlineCode: "125", IssuedBy: "adam"}); err != nil {
		t.Fatal(err)
	}

	// Issuing is what the limit was waiting for, so the task is done.
	pending, _ := gw.Store.ListQueue(ctx, store.QueueFilter{Queue: store.QueueTicketing})
	if len(pending) != 0 {
		t.Errorf("ticketing task still pending after issuance: %+v", pending)
	}
	after, _ := gw.Store.GetPNR(ctx, rec.RecordLocator)
	for _, tk := range after.Ticketing {
		if tk.Deadline != nil {
			t.Error("the deadline should be cleared once met")
		}
		if tk.Text == "" {
			t.Error("the arrangement text says how it was to be ticketed and stays")
		}
	}

	// And the sweeper must not raise it again.
	n, err := sw.Sweep(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("sweeper raised %d tasks on a ticketed record", n)
	}
}

func TestConjunctionTicketsForLongItineraries(t *testing.T) {
	gw := ticketNode(t)
	ctx := context.Background()
	// Six segments does not fit four coupons.
	recordToTicket(t, gw, "ISS004", 1, 6, nil)

	rec, err := gw.IssueTickets(ctx, "ISS004", IssueOptions{AirlineCode: "125"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Tickets) != 2 {
		t.Fatalf("six segments need two documents, got %d", len(rec.Tickets))
	}
	if len(rec.Tickets[0].Coupons) != 4 || len(rec.Tickets[1].Coupons) != 2 {
		t.Errorf("coupons split %d/%d, want 4/2",
			len(rec.Tickets[0].Coupons), len(rec.Tickets[1].Coupons))
	}
	// Each document names the other, so a partner holding one can find the rest.
	for i, tk := range rec.Tickets {
		if len(tk.Conjunction) != 1 {
			t.Errorf("ticket %d lists %d conjunctions, want 1", i, len(tk.Conjunction))
		}
	}
	if !rec.Ticketed() {
		t.Error("all six segments should be covered")
	}
}

func TestIssueRefusesWhatItCannotTicket(t *testing.T) {
	gw := ticketNode(t)
	ctx := context.Background()

	recordToTicket(t, gw, "ISS005", 1, 0, nil)
	if _, err := gw.IssueTickets(ctx, "ISS005", IssueOptions{AirlineCode: "125"}); err == nil {
		t.Error("a record with no air segment cannot be ticketed")
	}

	rec := recordToTicket(t, gw, "ISS006", 1, 1, nil)
	rec.Status = pnr.StatusCancelled
	if err := gw.Store.UpdatePNR(ctx, rec, rec.Version, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := gw.IssueTickets(ctx, "ISS006", IssueOptions{AirlineCode: "125"}); err == nil {
		t.Error("a cancelled record cannot be ticketed")
	}

	if _, err := gw.IssueTickets(ctx, "ISS005", IssueOptions{AirlineCode: "BA"}); err == nil {
		t.Error("the two-letter designator is not a stock code and must be refused")
	}
}
