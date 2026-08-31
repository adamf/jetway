package gateway

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/adamf/jetway/pkg/queue"
	"github.com/adamf/jetway/pkg/store"
	"github.com/adamf/jetway/pkg/pnr"
)

func cancelNode(t *testing.T) (*Gateway, *sentTo) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := store.NewMem()
	gw := New(Identity{Designator: "1J", TTYAddress: "LONRM1J", Name: "jetway"},
		st, NewBus(100), log, []byte("secret"))
	gw.Queues = &queue.Manager{Store: st, Log: log}
	gw.AddPeer(&Peer{Name: "BA", Carrier: "BA", Format: store.FormatTypeB, TTYAddress: "LHRRMBA"})
	gw.AddPeer(&Peer{Name: "AA", Carrier: "AA", Format: store.FormatEDIFACT, TTYAddress: "DFWRMAA"})
	sent := newSentTo()
	gw.Sender = sent.sender()
	return gw, sent
}

func interlineRecord(t *testing.T, gw *Gateway, locator string) *pnr.PNR {
	t.Helper()
	now := time.Now().UTC()
	rec := &pnr.PNR{
		RecordLocator: locator, Status: pnr.StatusOpen, CreatedAt: now, UpdatedAt: now,
		Origin:     pnr.Origin{Party: "1J"},
		Passengers: []pnr.Passenger{{Ref: 1, Surname: "FLETCHER", Given: "ADAM"}},
		Segments: []pnr.Segment{
			{Ref: 1, Type: pnr.SegmentAir, Carrier: "AA", FlightNum: "0050", Class: "Y",
				Board: "DFW", Off: "LHR", Status: "HK", Seats: 1, WireDate: "15DEC",
				Depart: now.AddDate(0, 1, 0)},
			{Ref: 2, Type: pnr.SegmentAir, Carrier: "BA", FlightNum: "0117", Class: "Y",
				Board: "LHR", Off: "JFK", Status: "HK", Seats: 1, WireDate: "16DEC",
				Depart: now.AddDate(0, 1, 1)},
		},
		Locators: []pnr.ExternalLocator{{Owner: "BA", Value: "XY7QP2"}},
	}
	if err := gw.Store.CreatePNR(context.Background(), rec, nil); err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestCancelTellsEveryCarrier(t *testing.T) {
	gw, sent := cancelNode(t)
	ctx := context.Background()
	interlineRecord(t, gw, "CAN001")

	res, err := gw.Cancel(ctx, "CAN001", CancelOptions{By: "adam", Reason: "passenger request"})
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if len(res.Unreachable) != 0 {
		t.Errorf("Unreachable = %v, want none", res.Unreachable)
	}
	if len(res.Notified) != 2 {
		t.Errorf("Notified = %v, want both carriers", res.Notified)
	}
	if res.PNR.Status != pnr.StatusCancelled {
		t.Errorf("Status = %q, want cancelled", res.PNR.Status)
	}
	for _, s := range res.PNR.Segments {
		if s.Status != "XX" {
			t.Errorf("segment %d = %q, want XX", s.Ref, s.Status)
		}
	}

	if sent.count("BA") != 1 || sent.count("AA") != 1 {
		t.Fatalf("sent BA=%d AA=%d, want 1 each", sent.count("BA"), sent.count("AA"))
	}
	ba := string(sent.msgs["BA"][0])
	if !strings.Contains(ba, "XX") {
		t.Errorf("the teletype cancellation does not carry XX:\n%s", ba)
	}
	// A cancellation the carrier cannot file against a booking does not happen.
	if !strings.Contains(ba, "XY7QP2") {
		t.Errorf("the carrier's own locator is missing:\n%s", ba)
	}
	aa := string(sent.msgs["AA"][0])
	if !strings.Contains(aa, "RPI+1+XX") {
		t.Errorf("the EDIFACT cancellation does not carry XX:\n%s", aa)
	}
}

func TestCancelOneSegmentLeavesTheRest(t *testing.T) {
	gw, sent := cancelNode(t)
	ctx := context.Background()
	interlineRecord(t, gw, "CAN002")

	res, err := gw.Cancel(ctx, "CAN002", CancelOptions{Segments: []int{2}, By: "adam"})
	if err != nil {
		t.Fatal(err)
	}
	// One leg off an interline booking is not the booking cancelled.
	if res.PNR.Status == pnr.StatusCancelled {
		t.Error("a record with a live segment must not be marked cancelled")
	}
	if res.PNR.Segments[0].Status != "HK" || res.PNR.Segments[1].Status != "XX" {
		t.Errorf("segments = %q/%q, want HK/XX",
			res.PNR.Segments[0].Status, res.PNR.Segments[1].Status)
	}
	// Only the carrier losing a segment hears about it.
	if sent.count("BA") != 1 || sent.count("AA") != 0 {
		t.Errorf("sent BA=%d AA=%d, want 1 and 0", sent.count("BA"), sent.count("AA"))
	}
}

func TestCarrierNotToldBecomesADivergence(t *testing.T) {
	gw, sent := cancelNode(t)
	ctx := context.Background()
	rec := interlineRecord(t, gw, "CAN003")
	// A third leg on a carrier this node has no link to. The seats stay held
	// and nobody is telling them.
	rec.Segments = append(rec.Segments, pnr.Segment{
		Ref: 3, Type: pnr.SegmentAir, Carrier: "LH", FlightNum: "0400", Class: "Y",
		Board: "FRA", Off: "JFK", Status: "HK", Seats: 1, WireDate: "18DEC",
		Depart: time.Now().UTC().AddDate(0, 1, 2),
	})
	if err := gw.Store.UpdatePNR(ctx, rec, rec.Version, nil); err != nil {
		t.Fatal(err)
	}

	res, err := gw.Cancel(ctx, "CAN003", CancelOptions{By: "adam"})
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if len(res.Unreachable) != 1 || res.Unreachable[0] != "LH" {
		t.Fatalf("Unreachable = %v, want [LH]", res.Unreachable)
	}
	// The reachable carriers are still told: one bad link must not block the rest.
	if sent.count("AA") != 1 || sent.count("BA") != 1 {
		t.Errorf("the reachable carriers should still have been told: AA=%d BA=%d",
			sent.count("AA"), sent.count("BA"))
	}
	items, err := gw.Store.ListQueue(ctx, store.QueueFilter{Queue: store.QueueDivergence})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected a divergence item, got %d", len(items))
	}
	if !strings.Contains(items[0].Reason, "may still hold the seats") {
		t.Errorf("the divergence must say what is wrong: %q", items[0].Reason)
	}
}

func TestCancelIsNotRepeatable(t *testing.T) {
	gw, _ := cancelNode(t)
	ctx := context.Background()
	interlineRecord(t, gw, "CAN004")

	if _, err := gw.Cancel(ctx, "CAN004", CancelOptions{By: "adam"}); err != nil {
		t.Fatal(err)
	}
	// Cancelling twice would invite the carrier to look for a holding they no
	// longer have.
	if _, err := gw.Cancel(ctx, "CAN004", CancelOptions{By: "adam"}); err != ErrNothingToCancel {
		t.Errorf("second cancel = %v, want ErrNothingToCancel", err)
	}
}

func TestSweeperCancelsOnExpiryOnlyWhenAsked(t *testing.T) {
	ctx := context.Background()
	past := time.Now().UTC().Add(-2 * time.Hour)

	// Default: raise it and leave the booking alone.
	gw, sent := cancelNode(t)
	rec := interlineRecord(t, gw, "EXP001")
	rec.Ticketing = []pnr.Ticketing{{Text: "TKTL", Deadline: &past}}
	if err := gw.Store.UpdatePNR(ctx, rec, rec.Version, nil); err != nil {
		t.Fatal(err)
	}
	sw := &queue.Sweeper{Records: gw.Store, Queues: gw.Queues}
	if _, err := sw.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	after, _ := gw.Store.GetPNR(ctx, "EXP001")
	if after.Status == pnr.StatusCancelled {
		t.Error("the sweeper must not cancel unless asked to")
	}
	if sent.count("BA") != 0 {
		t.Error("no cancellation should have been sent")
	}

	// Asked: cancel, and tell the carriers.
	gw2, sent2 := cancelNode(t)
	rec2 := interlineRecord(t, gw2, "EXP002")
	rec2.Ticketing = []pnr.Ticketing{{Text: "TKTL", Deadline: &past}}
	if err := gw2.Store.UpdatePNR(ctx, rec2, rec2.Version, nil); err != nil {
		t.Fatal(err)
	}
	sw2 := &queue.Sweeper{Records: gw2.Store, Queues: gw2.Queues, Cancel: gw2}
	if _, err := sw2.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	after2, _ := gw2.Store.GetPNR(ctx, "EXP002")
	if after2.Status != pnr.StatusCancelled {
		t.Errorf("Status = %q, want cancelled", after2.Status)
	}
	if sent2.count("BA") != 1 || sent2.count("AA") != 1 {
		t.Errorf("both carriers must be told: BA=%d AA=%d", sent2.count("BA"), sent2.count("AA"))
	}
}
