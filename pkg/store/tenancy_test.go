package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/adamf/jetway/pkg/pnr"
)

func recordOn(locator, carrier, number, date string) *pnr.PNR {
	now := time.Now().UTC()
	return &pnr.PNR{
		RecordLocator: locator, Status: pnr.StatusOpen, CreatedAt: now, UpdatedAt: now,
		Passengers: []pnr.Passenger{{Ref: 1, Surname: "TEST", Given: "ONE"}},
		Segments: []pnr.Segment{{Ref: 1, Type: pnr.SegmentAir, Carrier: carrier, FlightNum: number,
			Class: "Y", WireDate: date, Board: "LHR", Off: "JFK", Status: "HK", Seats: 1}},
	}
}

// Two systems on one database see only their own rows: the same locator,
// the same flight, the same queue name, and neither knows the other exists.
func TestNodeViewsAreIsolated(t *testing.T) {
	dsn := os.Getenv("JETWAY_TEST_DSN")
	if dsn == "" {
		t.Skip("JETWAY_TEST_DSN not set")
	}
	ctx := context.Background()
	root, err := OpenPostgres(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := MigrateSchema(ctx, root); err != nil {
		t.Fatal(err)
	}
	if _, err := root.pool.Exec(ctx, `TRUNCATE queue_item, pnr_event, pnr, message`); err != nil {
		t.Fatal(err)
	}
	ba, lh := root.Node("BA"), root.Node("LH")

	// The same six characters are two carriers' two bookings.
	if err := ba.CreatePNR(ctx, recordOn("ABC123", "BA", "0117", "16DEC"), []Event{{Type: "create"}}); err != nil {
		t.Fatal(err)
	}
	if err := lh.CreatePNR(ctx, recordOn("ABC123", "LH", "0400", "16DEC"), []Event{{Type: "create"}}); err != nil {
		t.Fatalf("a locator must be unique per system, not per database: %v", err)
	}
	got, err := ba.GetPNR(ctx, "ABC123")
	if err != nil || got.Segments[0].Carrier != "BA" {
		t.Fatalf("BA's view returned %+v, %v", got, err)
	}
	got, err = lh.GetPNR(ctx, "ABC123")
	if err != nil || got.Segments[0].Carrier != "LH" {
		t.Fatalf("LH's view returned %+v, %v", got, err)
	}
	if _, err := root.GetPNR(ctx, "ABC123"); err != ErrNotFound {
		t.Errorf("the single-tenant view saw a tenant's record: %v", err)
	}
	if recs, _ := ba.ListPNRs(ctx, 10); len(recs) != 1 {
		t.Errorf("BA lists %d records, want 1", len(recs))
	}
	if recs, _ := ba.FindPNRsByFlight(ctx, "LH0400", "16DEC", 10); len(recs) != 0 {
		t.Errorf("BA's flight lookup found LH's booking")
	}
	if recs, _ := lh.FindPNRsByFlight(ctx, "LH400", "16DEC", 10); len(recs) != 1 {
		t.Errorf("LH's flight lookup found %d, want its own booking", len(recs))
	}
	// Updates carry the scope too: BA cannot touch LH's record by id.
	lhRec, _ := lh.GetPNR(ctx, "ABC123")
	lhRec.Remarks = append(lhRec.Remarks, pnr.Remark{Text: "X"})
	if err := ba.UpdatePNR(ctx, lhRec, lhRec.Version, nil); err != ErrNotFound {
		t.Errorf("BA updated LH's record through its id: %v", err)
	}

	// Messages and queues likewise.
	if err := ba.AppendMessage(ctx, newMsg("net", []byte("BA"))); err != nil {
		t.Fatal(err)
	}
	if msgs, _ := lh.ListMessages(ctx, MessageFilter{Limit: 10}); len(msgs) != 0 {
		t.Errorf("LH sees BA's messages")
	}
	if msgs, _ := ba.ListMessages(ctx, MessageFilter{Limit: 10}); len(msgs) != 1 {
		t.Errorf("BA sees %d of its own message", len(msgs))
	}
	baRec, _ := ba.GetPNR(ctx, "ABC123")
	if err := ba.Enqueue(ctx, &QueueItem{Queue: "confirmation", PNRID: baRec.ID, Code: "KK", Reason: "t", PlacedBy: "t"}); err != nil {
		t.Fatal(err)
	}
	if counts, _ := lh.QueueCounts(ctx); counts["confirmation"] != 0 {
		t.Errorf("LH counts BA's queue: %v", counts)
	}
	if counts, _ := ba.QueueCounts(ctx); counts["confirmation"] != 1 {
		t.Errorf("BA's queue: %v", counts)
	}

	// Closing a view leaves the shared pool alone.
	if err := ba.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := lh.GetPNR(ctx, "ABC123"); err != nil {
		t.Errorf("closing one view closed the pool: %v", err)
	}
}

// Purge discards what is older than the moment and nothing else, on every
// backend, and one node's purge leaves another's rows alone.
func TestPurgeIsBoundedByTimeAndNode(t *testing.T) {
	eachBackend(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		old := time.Now().UTC().Add(-48 * time.Hour)
		cut := time.Now().UTC().Add(-24 * time.Hour)

		stale := recordOn("OLD111", "BA", "0117", "16DEC")
		stale.CreatedAt, stale.UpdatedAt = old, old
		if err := s.CreatePNR(ctx, stale, []Event{{Type: "create", At: old}}); err != nil {
			t.Fatal(err)
		}
		if err := s.Enqueue(ctx, &QueueItem{Queue: "confirmation", PNRID: stale.ID, Code: "KK", Reason: "t", PlacedBy: "t", PlacedAt: old}); err != nil {
			t.Fatal(err)
		}
		fresh := recordOn("NEW222", "BA", "0117", "16DEC")
		if err := s.CreatePNR(ctx, fresh, []Event{{Type: "create"}}); err != nil {
			t.Fatal(err)
		}
		m1 := newMsg("net", []byte("old"))
		m1.At = old
		m2 := newMsg("net", []byte("new"))
		for _, m := range []*Message{m1, m2} {
			if err := s.AppendMessage(ctx, m); err != nil {
				t.Fatal(err)
			}
		}

		var other Store
		if pg, ok := s.(*Postgres); ok {
			other = pg.Node("ZZ")
			bystander := recordOn("OLD333", "ZZ", "0001", "16DEC")
			bystander.CreatedAt, bystander.UpdatedAt = old, old
			if err := other.CreatePNR(ctx, bystander, nil); err != nil {
				t.Fatal(err)
			}
		}

		got, err := s.Purge(ctx, cut)
		if err != nil {
			t.Fatal(err)
		}
		if got.Records != 1 || got.Messages != 1 || got.QueueItems != 1 {
			t.Errorf("purged %+v, want one record, one message, one queue item", got)
		}
		if _, err := s.GetPNR(ctx, "OLD111"); err != ErrNotFound {
			t.Errorf("the stale record survived: %v", err)
		}
		if _, err := s.GetPNR(ctx, "NEW222"); err != nil {
			t.Errorf("the fresh record went: %v", err)
		}
		if _, err := s.GetMessage(ctx, m1.ID); err != ErrNotFound {
			t.Errorf("the old message survived: %v", err)
		}
		if _, err := s.GetMessage(ctx, m2.ID); err != nil {
			t.Errorf("the new message went: %v", err)
		}
		if counts, _ := s.QueueCounts(ctx); counts["confirmation"] != 0 {
			t.Errorf("the stale record's queue item survived: %v", counts)
		}
		if other != nil {
			if _, err := other.GetPNR(ctx, "OLD333"); err != nil {
				t.Errorf("one node's purge removed another's record: %v", err)
			}
		}
	})
}

// Split sends messages one way and records the other, and both halves
// answer as one store.
func TestSplitStoreRoutesByKind(t *testing.T) {
	ctx := context.Background()
	msgs, recs := NewMem(), NewMem()
	s := Split{Messages: msgs, Records: recs}
	if err := s.AppendMessage(ctx, newMsg("net", []byte("x"))); err != nil {
		t.Fatal(err)
	}
	if err := s.CreatePNR(ctx, recordOn("SPL111", "BA", "0117", "16DEC"), []Event{{Type: "create"}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := msgs.ListMessages(ctx, MessageFilter{Limit: 10}); len(got) != 1 {
		t.Errorf("message not in the message store")
	}
	if got, _ := recs.ListMessages(ctx, MessageFilter{Limit: 10}); len(got) != 0 {
		t.Errorf("message leaked into the record store")
	}
	if _, err := recs.GetPNR(ctx, "SPL111"); err != nil {
		t.Errorf("record not in the record store: %v", err)
	}
	if _, err := msgs.GetPNR(ctx, "SPL111"); err != ErrNotFound {
		t.Errorf("record leaked into the message store: %v", err)
	}
	if got, _ := s.FindPNRsByFlight(ctx, "BA0117", "16DEC", 10); len(got) != 1 {
		t.Errorf("flight lookup through the split found %d", len(got))
	}
	if _, err := s.Purge(ctx, time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ListPNRs(ctx, 10); len(got) != 0 {
		t.Error("purge through the split left records")
	}
}
