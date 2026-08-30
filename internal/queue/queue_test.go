package queue

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/adamf/jetway/internal/store"
	"github.com/adamf/jetway/pkg/pnr"
)

func newManager(st store.Store) *Manager { return &Manager{Store: st} }

func record(t *testing.T, st store.Store, locator string, segs ...pnr.Segment) *pnr.PNR {
	t.Helper()
	now := time.Now().UTC()
	p := &pnr.PNR{
		RecordLocator: locator, Status: pnr.StatusOpen,
		CreatedAt: now, UpdatedAt: now, Segments: segs,
	}
	if err := st.CreatePNR(context.Background(), p, nil); err != nil {
		t.Fatalf("CreatePNR: %v", err)
	}
	return p
}

func TestForStatus(t *testing.T) {
	cases := []struct {
		status string
		queue  string
	}{
		{"HK", store.QueueConfirmation},
		{"KK", store.QueueConfirmation},
		{"HL", store.QueueWaitlist},
		{"UU", store.QueueWaitlist},
		{"UC", store.QueueUnable},
		{"NO", store.QueueUnable},
		{"XX", store.QueueUnable},
		{"ZZ", store.QueueDivergence},
		// Our own outstanding request is not yet anybody's task.
		{"NN", ""},
		{"HN", ""},
		{"SS", ""},
		{"", ""},
	}
	for _, c := range cases {
		q, code, reason, ok := ForStatus(c.status)
		if c.queue == "" {
			if ok {
				t.Errorf("ForStatus(%q) queued on %s, want no queue", c.status, q)
			}
			continue
		}
		if !ok || q != c.queue {
			t.Errorf("ForStatus(%q) = %q, want %q", c.status, q, c.queue)
		}
		if code == "" || reason == "" {
			t.Errorf("ForStatus(%q) gave an empty code or reason", c.status)
		}
	}
}

func TestPlaceIsIdempotent(t *testing.T) {
	ctx := context.Background()
	st := store.NewMem()
	m := newManager(st)
	p := record(t, st, "QQQ111")

	item := func() *store.QueueItem {
		return &store.QueueItem{
			Queue: store.QueueUnable, PNRID: p.ID, Locator: p.RecordLocator,
			Code: "unable_UC", Reason: "partner refused",
		}
	}
	placed, err := m.Place(ctx, item())
	if err != nil || !placed {
		t.Fatalf("first Place = %v, %v; want true, nil", placed, err)
	}
	placed, err = m.Place(ctx, item())
	if err != nil {
		t.Fatalf("second Place errored: %v", err)
	}
	if placed {
		t.Error("a repeat placement must report false, not create a second task")
	}
}

func TestPlacePublishesButDoesNotDependOnIt(t *testing.T) {
	ctx := context.Background()
	st := store.NewMem()
	var published int
	m := &Manager{
		Store: st,
		Publish: PublisherFunc(func(ctx context.Context, it *store.QueueItem) error {
			published++
			return errors.New("broker unreachable")
		}),
	}
	p := record(t, st, "QQQ222")
	placed, err := m.Place(ctx, &store.QueueItem{
		Queue: store.QueueConfirmation, PNRID: p.ID, Code: "confirmed_HK", Reason: "ok",
	})
	// A broker that is down must not lose work that is already durable.
	if err != nil || !placed {
		t.Fatalf("Place = %v, %v; a failing publisher must not fail the placement", placed, err)
	}
	if published != 1 {
		t.Errorf("publisher called %d times, want 1", published)
	}
	items, _ := st.ListQueue(ctx, store.QueueFilter{})
	if len(items) != 1 {
		t.Errorf("item not stored despite publish failure: %d items", len(items))
	}
}

func TestSweepRaisesTicketingLimits(t *testing.T) {
	ctx := context.Background()
	st := store.NewMem()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

	past := now.Add(-2 * time.Hour)
	soon := now.Add(3 * time.Hour)
	far := now.Add(30 * 24 * time.Hour)

	expired := record(t, st, "TKT001")
	expired.Ticketing = []pnr.Ticketing{{Text: "TKTL", Deadline: &past}}
	if err := st.UpdatePNR(ctx, expired, expired.Version, nil); err != nil {
		t.Fatal(err)
	}
	near := record(t, st, "TKT002")
	near.Ticketing = []pnr.Ticketing{{Text: "TKTL", Deadline: &soon}}
	if err := st.UpdatePNR(ctx, near, near.Version, nil); err != nil {
		t.Fatal(err)
	}
	distant := record(t, st, "TKT003")
	distant.Ticketing = []pnr.Ticketing{{Text: "TKTL", Deadline: &far}}
	if err := st.UpdatePNR(ctx, distant, distant.Version, nil); err != nil {
		t.Fatal(err)
	}

	sw := &Sweeper{Records: st, Queues: newManager(st), Now: func() time.Time { return now }}
	n, err := sw.Sweep(ctx)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if n != 2 {
		t.Fatalf("Sweep placed %d, want 2 (one expired, one approaching)", n)
	}

	items, _ := st.ListQueue(ctx, store.QueueFilter{Queue: store.QueueTicketing})
	got := map[string]string{}
	for _, it := range items {
		got[it.Locator] = it.Code
	}
	if got["TKT001"] != "tktl_expired" {
		t.Errorf("TKT001 code = %q, want tktl_expired", got["TKT001"])
	}
	if got["TKT002"] != "tktl_near" {
		t.Errorf("TKT002 code = %q, want tktl_near", got["TKT002"])
	}
	if _, ok := got["TKT003"]; ok {
		t.Error("a deadline a month out must not be queued yet")
	}

	// Sweeping again must not stack duplicates.
	again, err := sw.Sweep(ctx)
	if err != nil || again != 0 {
		t.Errorf("second sweep placed %d (err %v), want 0", again, err)
	}
}

func TestSweepRaisesUnansweredRequests(t *testing.T) {
	ctx := context.Background()
	st := store.NewMem()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

	stale := record(t, st, "PND001", pnr.Segment{Ref: 1, Carrier: "BA", FlightNum: "175", Status: "HN"})
	stale.UpdatedAt = now.Add(-8 * time.Hour)
	if err := st.UpdatePNR(ctx, stale, stale.Version, nil); err != nil {
		t.Fatal(err)
	}
	fresh := record(t, st, "PND002", pnr.Segment{Ref: 1, Carrier: "BA", FlightNum: "176", Status: "HN"})
	fresh.UpdatedAt = now.Add(-10 * time.Minute)
	if err := st.UpdatePNR(ctx, fresh, fresh.Version, nil); err != nil {
		t.Fatal(err)
	}
	settled := record(t, st, "PND003", pnr.Segment{Ref: 1, Carrier: "BA", FlightNum: "177", Status: "HK"})
	settled.UpdatedAt = now.Add(-8 * time.Hour)
	if err := st.UpdatePNR(ctx, settled, settled.Version, nil); err != nil {
		t.Fatal(err)
	}

	sw := &Sweeper{
		Records: st, Queues: newManager(st),
		PendingAfter: 6 * time.Hour, Now: func() time.Time { return now },
	}
	if _, err := sw.Sweep(ctx); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	items, _ := st.ListQueue(ctx, store.QueueFilter{Queue: store.QueuePending})
	if len(items) != 1 {
		t.Fatalf("pending queue has %d items, want 1: %+v", len(items), items)
	}
	if items[0].Locator != "PND001" {
		t.Errorf("queued %s, want PND001", items[0].Locator)
	}
}

func TestSweepSkipsCancelledRecords(t *testing.T) {
	ctx := context.Background()
	st := store.NewMem()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)

	p := record(t, st, "CXL001")
	p.Status = pnr.StatusCancelled
	p.Ticketing = []pnr.Ticketing{{Text: "TKTL", Deadline: &past}}
	if err := st.UpdatePNR(ctx, p, p.Version, nil); err != nil {
		t.Fatal(err)
	}

	sw := &Sweeper{Records: st, Queues: newManager(st), Now: func() time.Time { return now }}
	n, err := sw.Sweep(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("swept a cancelled record onto %d queues; nothing is owed on it", n)
	}
}

// The sweep used to read the most recently updated records and look for stale
// ones among them. That is inverted: the freshest records are by definition not
// the stale ones, so once the store held more records than the limit, a
// ticketing time limit could never fire and an unanswered segment could never
// be raised. The pass reported nothing to do, with no error and no log line --
// a booking would sit past its deadline forever and nobody would be told.
func TestSweepFindsDueRecordsBuriedUnderFreshOnes(t *testing.T) {
	ctx := context.Background()
	st := store.NewMem()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	past := now.Add(-2 * time.Hour)

	// One record past its ticketing deadline, and one whose request has gone
	// unanswered for longer than the agreed time.
	expired := record(t, st, "DUE001")
	expired.Ticketing = []pnr.Ticketing{{Text: "TKTL", Deadline: &past}}
	expired.UpdatedAt = now.Add(-90 * 24 * time.Hour)
	if err := st.UpdatePNR(ctx, expired, expired.Version, nil); err != nil {
		t.Fatal(err)
	}
	stalled := record(t, st, "DUE002", pnr.Segment{
		Ref: 1, Type: pnr.SegmentAir, Carrier: "BA", FlightNum: "0117",
		Board: "LHR", Off: "JFK", Status: "NN", Seats: 1,
	})
	stalled.UpdatedAt = now.Add(-90 * 24 * time.Hour)
	if err := st.UpdatePNR(ctx, stalled, stalled.Version, nil); err != nil {
		t.Fatal(err)
	}

	// Bury both under more freshly touched records than a pass will handle.
	for i := 0; i < DefaultSweepLimit+50; i++ {
		fresh := record(t, st, fmt.Sprintf("FR%04d", i))
		fresh.UpdatedAt = now.Add(-time.Minute)
		if err := st.UpdatePNR(ctx, fresh, fresh.Version, nil); err != nil {
			t.Fatal(err)
		}
	}

	sw := &Sweeper{Records: st, Queues: newManager(st), Now: func() time.Time { return now }}
	if _, err := sw.Sweep(ctx); err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	for _, want := range []struct{ queue, locator, code string }{
		{store.QueueTicketing, "DUE001", "tktl_expired"},
		{store.QueuePending, "DUE002", "unanswered_NN"},
	} {
		items, err := st.ListQueue(ctx, store.QueueFilter{Queue: want.queue})
		if err != nil {
			t.Fatalf("ListQueue: %v", err)
		}
		found := false
		for _, it := range items {
			if it.Locator == want.locator && it.Code == want.code {
				found = true
			}
		}
		if !found {
			t.Errorf("%s was never raised on the %s queue; it was buried under "+
				"%d freshly touched records, which is exactly when the old "+
				"sweep stopped finding anything", want.locator, want.queue,
				DefaultSweepLimit+50)
		}
	}
}
