package store

import (
	"context"
	"testing"

	"github.com/adamf/jetway/pkg/pnr"
)

// A host's retention policy decides which records leave; the store does
// not guess. The rejected record goes with its events and queue items and
// cannot be found by locator afterwards; the kept one is untouched.
func TestPruneRecordsFollowsTheCallersPolicy(t *testing.T) {
	ctx := context.Background()
	s := NewMem()
	flown := recordOn("FLOWN1", "BA", "0117", "16DEC")
	if err := s.CreatePNR(ctx, flown, []Event{{Type: "create"}, {Type: "ticket"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Enqueue(ctx, &QueueItem{Queue: "confirmation", PNRID: flown.ID, Code: "KK", Reason: "t", PlacedBy: "t"}); err != nil {
		t.Fatal(err)
	}
	live := recordOn("LIVE22", "BA", "0117", "17DEC")
	if err := s.CreatePNR(ctx, live, []Event{{Type: "create"}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.PruneRecords(ctx, func(p *pnr.PNR) bool { return p.RecordLocator != "FLOWN1" })
	if err != nil {
		t.Fatal(err)
	}
	if got.Records != 1 || got.QueueItems != 1 || got.Messages != 0 {
		t.Fatalf("pruned %+v, want one record and its queue item", got)
	}
	if _, err := s.GetPNR(ctx, "FLOWN1"); err != ErrNotFound {
		t.Fatalf("pruned record still found: %v", err)
	}
	if ev, _ := s.Events(ctx, flown.ID); len(ev) != 0 {
		t.Fatalf("pruned record kept %d events", len(ev))
	}
	if _, err := s.GetPNR(ctx, "LIVE22"); err != nil {
		t.Fatalf("kept record lost: %v", err)
	}
	if ev, _ := s.Events(ctx, live.ID); len(ev) != 1 {
		t.Fatalf("kept record's events changed: %d", len(ev))
	}
	// Split forwards to the book of record.
	sp := Split{Messages: NewMem(), Records: s}
	if _, err := sp.PruneRecords(ctx, func(*pnr.PNR) bool { return false }); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.ListPNRs(ctx, 10); len(n) != 0 {
		t.Fatalf("split prune left %d records", len(n))
	}
}
