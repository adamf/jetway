package gateway

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/adamf/jetway/pkg/queue"
	"github.com/adamf/jetway/pkg/store"
	"github.com/adamf/jetway/pkg/pnr"
)

func scheduleNode(t *testing.T) *Gateway {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := store.NewMem()
	gw := New(Identity{Designator: "1J", TTYAddress: "LONRM1J", Name: "jetway"},
		st, NewBus(100), log, []byte("secret"))
	gw.Queues = &queue.Manager{Store: st, Log: log}
	gw.AddPeer(&Peer{Name: "BA", Carrier: "BA", Format: store.FormatTypeB, TTYAddress: "LHRRMBA"})
	gw.Sender = SenderFunc(func(ctx context.Context, peer string, raw []byte) error { return nil })
	return gw
}

func heldRecord(t *testing.T, gw *Gateway, locator, carrier, flight, date string) *pnr.PNR {
	t.Helper()
	now := time.Now().UTC()
	rec := &pnr.PNR{
		RecordLocator: locator, Status: pnr.StatusOpen, CreatedAt: now, UpdatedAt: now,
		Segments: []pnr.Segment{{
			Ref: 1, Type: pnr.SegmentAir, Carrier: carrier, FlightNum: flight,
			Board: "LHR", Off: "JFK", Status: "HK", WireDate: date,
		}},
	}
	if err := gw.Store.CreatePNR(context.Background(), rec, nil); err != nil {
		t.Fatal(err)
	}
	return rec
}

func scheduleMsg(text string) []byte {
	return []byte("QU LONRM1J\n.LHRRMBA 121430\n" + text + "\n")
}

func TestScheduleChangeQueuesAffectedRecords(t *testing.T) {
	gw := scheduleNode(t)
	ctx := context.Background()

	heldRecord(t, gw, "AFF001", "BA", "0117", "16DEC")
	heldRecord(t, gw, "AFF002", "BA", "117", "16DEC")  // same flight, written without the zero
	heldRecord(t, gw, "OTH001", "BA", "0117", "17DEC") // same flight, different day
	heldRecord(t, gw, "OTH002", "BA", "0999", "16DEC") // different flight

	res, err := gw.Ingest(ctx, "BA", scheduleMsg("ASM\nUTC\nCNL\nBA0117/16DEC\nLHR JFK"))
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if res.Status != store.StatusApplied {
		t.Errorf("Status = %q", res.Status)
	}

	items, err := gw.Store.ListQueue(ctx, store.QueueFilter{Queue: store.QueueScheduleChange})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, it := range items {
		got[it.Locator] = true
	}
	if !got["AFF001"] || !got["AFF002"] {
		t.Errorf("both holdings of the flight must be queued, got %v", got)
	}
	// A cancellation for one day must not sweep up the whole season.
	if got["OTH001"] {
		t.Error("a different date was queued")
	}
	if got["OTH002"] {
		t.Error("a different flight was queued")
	}
	if len(items) != 2 {
		t.Errorf("queued %d items, want 2", len(items))
	}
}

func TestScheduleChangeCreatesNoRecord(t *testing.T) {
	gw := scheduleNode(t)
	ctx := context.Background()

	if _, err := gw.Ingest(ctx, "BA", scheduleMsg("ASM\nUTC\nCNL\nBA0117/16DEC")); err != nil {
		t.Fatal(err)
	}
	recs, err := gw.Store.ListPNRs(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	// A broadcast that manufactured a record would create one per message.
	if len(recs) != 0 {
		t.Errorf("a schedule message created %d records", len(recs))
	}
}

func TestSuccessiveActionsRaiseSeparateTasks(t *testing.T) {
	gw := scheduleNode(t)
	ctx := context.Background()
	heldRecord(t, gw, "SEQ001", "BA", "0117", "16DEC")

	if _, err := gw.Ingest(ctx, "BA", scheduleMsg("ASM\nUTC\nTIM\nBA0117/16DEC\nLHR 0900 JFK 1200")); err != nil {
		t.Fatal(err)
	}
	if _, err := gw.Ingest(ctx, "BA", scheduleMsg("ASM\nUTC\nCNL\nBA0117/16DEC")); err != nil {
		t.Fatal(err)
	}

	items, _ := gw.Store.ListQueue(ctx, store.QueueFilter{Queue: store.QueueScheduleChange})
	if len(items) != 2 {
		t.Fatalf("a retime then a cancellation are two tasks, got %d: %+v", len(items), items)
	}
	codes := map[string]bool{}
	for _, it := range items {
		codes[it.Code] = true
	}
	if !codes["schedule_tim"] || !codes["schedule_cnl"] {
		t.Errorf("codes = %v, want one per action", codes)
	}
}

func TestRepeatedScheduleMessageIsNotADuplicateTask(t *testing.T) {
	gw := scheduleNode(t)
	ctx := context.Background()
	heldRecord(t, gw, "DUP001", "BA", "0117", "16DEC")

	// Two identical messages with different origin times, so deduplication at
	// the message layer does not hide what is being tested here.
	first := []byte("QU LONRM1J\n.LHRRMBA 121430\nASM\nUTC\nCNL\nBA0117/16DEC\n")
	second := []byte("QU LONRM1J\n.LHRRMBA 131500\nASM\nUTC\nCNL\nBA0117/16DEC\n")
	if _, err := gw.Ingest(ctx, "BA", first); err != nil {
		t.Fatal(err)
	}
	if _, err := gw.Ingest(ctx, "BA", second); err != nil {
		t.Fatal(err)
	}
	items, _ := gw.Store.ListQueue(ctx, store.QueueFilter{Queue: store.QueueScheduleChange})
	if len(items) != 1 {
		t.Errorf("the same change twice is one task, got %d", len(items))
	}
}

func TestCancelledRecordsAreLeftAlone(t *testing.T) {
	gw := scheduleNode(t)
	ctx := context.Background()
	rec := heldRecord(t, gw, "CXL777", "BA", "0117", "16DEC")
	rec.Status = pnr.StatusCancelled
	if err := gw.Store.UpdatePNR(ctx, rec, rec.Version, nil); err != nil {
		t.Fatal(err)
	}

	if _, err := gw.Ingest(ctx, "BA", scheduleMsg("ASM\nUTC\nCNL\nBA0117/16DEC")); err != nil {
		t.Fatal(err)
	}
	items, _ := gw.Store.ListQueue(ctx, store.QueueFilter{Queue: store.QueueScheduleChange})
	if len(items) != 0 {
		t.Errorf("a cancelled record owes nobody a schedule notification: %+v", items)
	}
}

// The failure this guards against had no error and no log line: a flight moved,
// the schedule message was accepted and marked applied, and the passengers
// booked long enough ago to have fallen off the recent-records list were simply
// never told. The further ahead the change, the more passengers it missed --
// which is exactly backwards, because a schedule change months out is the
// normal case.
func TestScheduleChangeReachesLongStandingBookings(t *testing.T) {
	gw := scheduleNode(t)
	ctx := context.Background()

	old := heldRecord(t, gw, "OLDBKG", "BA", "0117", "16DEC")
	old.UpdatedAt = time.Now().UTC().AddDate(0, -6, 0)
	if err := gw.Store.UpdatePNR(ctx, old, old.Version, nil); err != nil {
		t.Fatalf("age the record: %v", err)
	}

	// Bury it under more recent traffic than any scan limit would cover.
	limit := defaultScheduleScanLimit
	for i := 0; i < limit+50; i++ {
		heldRecord(t, gw, fmt.Sprintf("BG%04d", i), "AA", "0500", "16DEC")
	}

	if _, err := gw.Ingest(ctx, "BA", scheduleMsg("ASM\nUTC\nCNL\nBA0117/16DEC\nLHR JFK")); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	items, err := gw.Store.ListQueue(ctx, store.QueueFilter{Queue: store.QueueScheduleChange})
	if err != nil {
		t.Fatalf("ListQueue: %v", err)
	}
	for _, it := range items {
		if it.Locator == "OLDBKG" {
			return
		}
	}
	t.Fatalf("a booking made six months ago was not told its flight was cancelled; "+
		"%d schedule tasks were queued, none of them for it", len(items))
}
