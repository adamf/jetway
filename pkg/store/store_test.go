package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/adamf/jetway/pkg/pnr"
	"github.com/adamf/jetway/pkg/ulid"
)

// Every backend must satisfy the same contract. Running one suite over both is
// the only way the in-memory store stays a faithful stand-in for Postgres --
// otherwise tests pass against memory and the semantics that matter, above all
// optimistic concurrency, are never exercised where they are implemented.
func eachBackend(t *testing.T, fn func(t *testing.T, s Store)) {
	t.Helper()
	t.Run("mem", func(t *testing.T) { fn(t, NewMem()) })

	dsn := os.Getenv("JETWAY_TEST_DSN")
	if dsn == "" {
		t.Log("JETWAY_TEST_DSN not set; skipping the postgres backend")
		return
	}
	t.Run("postgres", func(t *testing.T) {
		ctx := context.Background()
		pg, err := OpenPostgres(ctx, dsn)
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		t.Cleanup(func() { pg.Close() })
		if err := MigrateSchema(ctx, pg); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		// Each subtest gets a clean slate so ordering assertions hold.
		if _, err := pg.pool.Exec(ctx, `TRUNCATE queue_item, pnr_event, pnr, message`); err != nil {
			t.Fatalf("truncate: %v", err)
		}
		fn(t, pg)
	})
}

func newMsg(peer string, raw []byte) *Message {
	return &Message{
		ID: ulid.New(), Direction: Inbound, At: time.Now().UTC(),
		Transport: "link", Peer: peer, Format: FormatTypeB,
		Raw: raw, SHA256: fmt.Sprintf("%x", len(raw)), Size: len(raw),
		Status: StatusReceived,
	}
}

func TestMessageRoundTrip(t *testing.T) {
	eachBackend(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		raw := []byte("QU LHRRMBA\r\n.LONRM1J 121430\r\nBA0175Y15JUNLHRJFKNN1\r\n\x00\xff")
		m := newMsg("BA", raw)
		if err := s.AppendMessage(ctx, m); err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
		got, err := s.GetMessage(ctx, m.ID)
		if err != nil {
			t.Fatalf("GetMessage: %v", err)
		}
		// Raw bytes are evidence. Any transformation, including a helpful one,
		// destroys the property the log exists for.
		if string(got.Raw) != string(raw) {
			t.Errorf("raw bytes changed:\n got %q\nwant %q", got.Raw, raw)
		}
		if got.Peer != "BA" || got.Status != StatusReceived || got.Size != len(raw) {
			t.Errorf("round trip lost fields: %+v", got)
		}
	})
}

func TestMessageStatusTransition(t *testing.T) {
	eachBackend(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		m := newMsg("BA", []byte("x"))
		if err := s.AppendMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
		m.Status = StatusApplied
		m.Kind = "AIRIMP/sell"
		m.PNRID = "pnr-1"
		m.Diagnostics = []Diagnostic{{Layer: "airimp", Severity: "warn", Code: "x", Detail: "y"}}
		if err := s.UpdateMessage(ctx, m); err != nil {
			t.Fatalf("UpdateMessage: %v", err)
		}
		got, _ := s.GetMessage(ctx, m.ID)
		if got.Status != StatusApplied || got.Kind != "AIRIMP/sell" || got.PNRID != "pnr-1" {
			t.Errorf("transition not stored: %+v", got)
		}
		if len(got.Diagnostics) != 1 || got.Diagnostics[0].Code != "x" {
			t.Errorf("diagnostics not stored: %+v", got.Diagnostics)
		}
		if err := s.UpdateMessage(ctx, &Message{ID: "nope"}); !errors.Is(err, ErrNotFound) {
			t.Errorf("updating a missing message = %v, want ErrNotFound", err)
		}
	})
}

func TestListMessagesOrderingAndLimit(t *testing.T) {
	eachBackend(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		var ids []string
		for i := 0; i < 12; i++ {
			m := newMsg("BA", []byte(fmt.Sprintf("msg-%02d", i)))
			if err := s.AppendMessage(ctx, m); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, m.ID)
		}
		all, err := s.ListMessages(ctx, MessageFilter{Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		if len(all) != 12 {
			t.Fatalf("got %d messages, want 12", len(all))
		}
		for i := 1; i < len(all); i++ {
			if all[i-1].ID >= all[i].ID {
				t.Fatalf("listing is not chronological at %d", i)
			}
		}
		// A limit must keep the newest traffic, not the oldest: an operator
		// looking at a link wants what just happened.
		recent, err := s.ListMessages(ctx, MessageFilter{Limit: 3})
		if err != nil {
			t.Fatal(err)
		}
		if len(recent) != 3 || recent[2].ID != ids[11] {
			t.Errorf("limit kept the wrong end of the log: %v", idsOf(recent))
		}
	})
}

func TestListMessagesFilters(t *testing.T) {
	eachBackend(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		for _, peer := range []string{"BA", "BA", "AA"} {
			m := newMsg(peer, []byte("x"))
			if peer == "AA" {
				m.Status = StatusDLQ
			}
			if err := s.AppendMessage(ctx, m); err != nil {
				t.Fatal(err)
			}
		}
		ba, _ := s.ListMessages(ctx, MessageFilter{Peer: "BA", Limit: 10})
		if len(ba) != 2 {
			t.Errorf("peer filter returned %d, want 2", len(ba))
		}
		dlq, _ := s.ListMessages(ctx, MessageFilter{Status: StatusDLQ, Limit: 10})
		if len(dlq) != 1 {
			t.Errorf("status filter returned %d, want 1", len(dlq))
		}
	})
}

func TestDedupLookup(t *testing.T) {
	eachBackend(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		m := newMsg("BA", []byte("x"))
		if err := s.AppendMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
		// The key is learned during decode, after capture. A backend that only
		// indexes on insert will never recognise a retransmission.
		m.DedupKey = "unb:12345"
		m.Status = StatusApplied
		if err := s.UpdateMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
		id, found, err := s.FindByDedupKey(ctx, "BA", "unb:12345")
		if err != nil {
			t.Fatal(err)
		}
		if !found || id != m.ID {
			t.Errorf("FindByDedupKey = %q,%v; want %q,true", id, found, m.ID)
		}
		// The key is scoped to the peer: two partners may use the same
		// reference numbering without colliding.
		if _, found, _ := s.FindByDedupKey(ctx, "AA", "unb:12345"); found {
			t.Error("dedup key leaked across peers")
		}
		if _, found, _ := s.FindByDedupKey(ctx, "BA", ""); found {
			t.Error("an empty key must never match")
		}
	})
}

func samplePNR(loc string) *pnr.PNR {
	now := time.Now().UTC()
	return &pnr.PNR{
		RecordLocator: loc, Status: pnr.StatusOpen, CreatedAt: now, UpdatedAt: now,
		Passengers: []pnr.Passenger{{Ref: 1, Surname: "SMITH", Given: "JOHN", Title: "MR"}},
		Segments: []pnr.Segment{{
			Ref: 1, Type: pnr.SegmentAir, Carrier: "BA", FlightNum: "0175", Class: "Y",
			Depart: now.AddDate(0, 0, 30), WireDate: "15JUN", Board: "LHR", Off: "JFK",
			Status: "HN", Seats: 1,
		}},
		SSRs: []pnr.SSR{{Code: "DOCS", Status: "HK", Count: 1, Text: "P/GBR/123", Sensitive: true}},
	}
}

func TestPNRCreateAndRead(t *testing.T) {
	eachBackend(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		p := samplePNR("ABC23D")
		events := []Event{{Type: "created", Detail: "test", At: time.Now().UTC()}}
		if err := s.CreatePNR(ctx, p, events); err != nil {
			t.Fatalf("CreatePNR: %v", err)
		}
		if p.Version != 1 {
			t.Errorf("Version = %d, want 1", p.Version)
		}
		got, err := s.GetPNR(ctx, "ABC23D")
		if err != nil {
			t.Fatalf("GetPNR: %v", err)
		}
		if got.Version != 1 || len(got.Segments) != 1 || got.Segments[0].Carrier != "BA" {
			t.Errorf("record did not round trip: %+v", got)
		}
		if len(got.SSRs) != 1 || !got.SSRs[0].Sensitive || got.SSRs[0].Text != "P/GBR/123" {
			t.Errorf("SSR did not round trip: %+v", got.SSRs)
		}
		byID, err := s.GetPNRByID(ctx, p.ID)
		if err != nil || byID.RecordLocator != "ABC23D" {
			t.Errorf("GetPNRByID = %+v, %v", byID, err)
		}
		if _, err := s.GetPNR(ctx, "NOSUCH"); !errors.Is(err, ErrNotFound) {
			t.Errorf("missing record = %v, want ErrNotFound", err)
		}
	})
}

func TestPNRDuplicateLocator(t *testing.T) {
	eachBackend(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		if err := s.CreatePNR(ctx, samplePNR("DUP123"), nil); err != nil {
			t.Fatal(err)
		}
		err := s.CreatePNR(ctx, samplePNR("DUP123"), nil)
		if !errors.Is(err, ErrDuplicate) {
			t.Errorf("second create = %v, want ErrDuplicate", err)
		}
	})
}

// Optimistic concurrency is the property the whole design leans on: a gateway
// and a carrier can be changing one record at the same instant.
func TestPNROptimisticConcurrency(t *testing.T) {
	eachBackend(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		p := samplePNR("VER123")
		if err := s.CreatePNR(ctx, p, nil); err != nil {
			t.Fatal(err)
		}

		a, _ := s.GetPNR(ctx, "VER123")
		b, _ := s.GetPNR(ctx, "VER123")

		a.Segments[0].Status = "HK"
		if err := s.UpdatePNR(ctx, a, 1, []Event{{Type: "confirmed", At: time.Now().UTC()}}); err != nil {
			t.Fatalf("first writer: %v", err)
		}
		if a.Version != 2 {
			t.Errorf("Version = %d, want 2", a.Version)
		}

		// The second writer read version 1 and must be refused, not allowed to
		// overwrite the confirmation it never saw.
		b.Segments[0].Status = "UC"
		if err := s.UpdatePNR(ctx, b, 1, nil); !errors.Is(err, ErrConflict) {
			t.Fatalf("stale writer = %v, want ErrConflict", err)
		}
		cur, _ := s.GetPNR(ctx, "VER123")
		if cur.Segments[0].Status != "HK" {
			t.Errorf("status = %q; the losing writer overwrote the winner", cur.Segments[0].Status)
		}
		if cur.Version != 2 {
			t.Errorf("Version = %d, want 2 (a refused write must not bump it)", cur.Version)
		}

		// Re-reading and retrying is the documented recovery, and must work.
		fresh, _ := s.GetPNR(ctx, "VER123")
		fresh.Segments[0].Status = "HL"
		if err := s.UpdatePNR(ctx, fresh, fresh.Version, nil); err != nil {
			t.Errorf("retry after re-read: %v", err)
		}
	})
}

func TestPNRConcurrentWritersExactlyOneWins(t *testing.T) {
	eachBackend(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		if err := s.CreatePNR(ctx, samplePNR("RACE12"), nil); err != nil {
			t.Fatal(err)
		}
		const writers = 8
		var wg sync.WaitGroup
		var mu sync.Mutex
		var wins, conflicts int
		for i := 0; i < writers; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				p, err := s.GetPNR(ctx, "RACE12")
				if err != nil {
					return
				}
				p.Remarks = append(p.Remarks, pnr.Remark{Text: fmt.Sprintf("writer %d", i)})
				err = s.UpdatePNR(ctx, p, 1, []Event{{Type: "remark", At: time.Now().UTC()}})
				mu.Lock()
				defer mu.Unlock()
				switch {
				case err == nil:
					wins++
				case errors.Is(err, ErrConflict):
					conflicts++
				default:
					t.Errorf("unexpected error: %v", err)
				}
			}(i)
		}
		wg.Wait()
		if wins != 1 {
			t.Errorf("%d writers succeeded against version 1; exactly one must", wins)
		}
		if conflicts != writers-1 {
			t.Errorf("%d conflicts, want %d", conflicts, writers-1)
		}
	})
}

func TestEventSequenceIsDenseAndOrdered(t *testing.T) {
	eachBackend(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		p := samplePNR("EVT123")
		if err := s.CreatePNR(ctx, p, []Event{
			{Type: "created", At: time.Now().UTC()},
			{Type: "add_segment", At: time.Now().UTC()},
		}); err != nil {
			t.Fatal(err)
		}
		if err := s.UpdatePNR(ctx, p, 1, []Event{
			{Type: "confirmed", MessageID: "msg-1", At: time.Now().UTC()},
		}); err != nil {
			t.Fatal(err)
		}
		events, err := s.Events(ctx, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(events) != 3 {
			t.Fatalf("events = %d, want 3", len(events))
		}
		for i, e := range events {
			if e.Seq != int64(i+1) {
				t.Errorf("event %d has seq %d; the sequence must be dense and ordered", i, e.Seq)
			}
		}
		if events[2].Type != "confirmed" || events[2].MessageID != "msg-1" {
			t.Errorf("provenance lost: %+v", events[2])
		}
	})
}

// Locator allocation depends on never seeing a counter value twice, including
// across concurrent callers.
func TestLocatorCounterIsUnique(t *testing.T) {
	eachBackend(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		const n = 200
		out := make(chan uint64, n)
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				v, err := s.NextLocatorCounter(ctx)
				if err != nil {
					t.Errorf("NextLocatorCounter: %v", err)
					return
				}
				out <- v
			}()
		}
		wg.Wait()
		close(out)
		seen := map[uint64]bool{}
		for v := range out {
			if seen[v] {
				t.Fatalf("counter value %d was handed out twice", v)
			}
			seen[v] = true
		}
		if len(seen) != n {
			t.Errorf("got %d distinct values, want %d", len(seen), n)
		}
	})
}

func idsOf(ms []*Message) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.ID
	}
	return out
}

// An unbounded in-memory store behind a public console is a memory leak with a
// submit button. The bound discards the oldest and keeps everything else usable.
func TestMemBoundsRetention(t *testing.T) {
	ctx := context.Background()
	s := NewMem()
	s.MaxMessages, s.MaxRecords = 5, 3

	var ids []string
	for i := 0; i < 12; i++ {
		m := newMsg("BA", []byte(fmt.Sprintf("m%02d", i)))
		m.DedupKey = fmt.Sprintf("key-%02d", i)
		if err := s.AppendMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, m.ID)
	}
	got, err := s.ListMessages(ctx, MessageFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("messages retained = %d, want 5", len(got))
	}
	// The newest survive and the oldest are gone.
	if got[len(got)-1].ID != ids[11] {
		t.Errorf("newest message was discarded")
	}
	if _, err := s.GetMessage(ctx, ids[0]); !errors.Is(err, ErrNotFound) {
		t.Errorf("oldest message should have been discarded, got %v", err)
	}
	// A discarded message must not leave its dedup key behind, or a later
	// retransmission is refused against a message that no longer exists.
	if id, found, _ := s.FindByDedupKey(ctx, "BA", "key-00"); found {
		t.Errorf("dedup key for a discarded message still resolves to %s", id)
	}
	if _, found, _ := s.FindByDedupKey(ctx, "BA", "key-11"); !found {
		t.Error("the newest message's dedup key should still resolve")
	}

	for i := 0; i < 7; i++ {
		p := samplePNR(fmt.Sprintf("REC%03d", i))
		if err := s.CreatePNR(ctx, p, []Event{{Type: "created", At: time.Now().UTC()}}); err != nil {
			t.Fatal(err)
		}
	}
	recs, _ := s.ListPNRs(ctx, 100)
	if len(recs) != 3 {
		t.Fatalf("records retained = %d, want 3", len(recs))
	}
	if _, err := s.GetPNR(ctx, "REC000"); !errors.Is(err, ErrNotFound) {
		t.Errorf("oldest record should have been discarded, got %v", err)
	}
	if _, err := s.GetPNR(ctx, "REC006"); err != nil {
		t.Errorf("newest record should survive: %v", err)
	}
}

// Zero means unbounded, which is what every other test relies on.
func TestMemUnboundedByDefault(t *testing.T) {
	ctx := context.Background()
	s := NewMem()
	for i := 0; i < 50; i++ {
		if err := s.AppendMessage(ctx, newMsg("BA", []byte("x"))); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := s.ListMessages(ctx, MessageFilter{Limit: 1000})
	if len(got) != 50 {
		t.Errorf("retained = %d, want all 50", len(got))
	}
}

func TestQueuePlacementAndWorking(t *testing.T) {
	eachBackend(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		p := samplePNR("QUE111")
		if err := s.CreatePNR(ctx, p, nil); err != nil {
			t.Fatal(err)
		}

		item := &QueueItem{
			Queue: QueueTicketing, PNRID: p.ID, Locator: p.RecordLocator,
			Code: "tktl_expired", Reason: "ticketing time limit passed",
			PlacedBy: "sweeper", PlacedAt: time.Now().UTC(),
		}
		if err := s.Enqueue(ctx, item); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
		if item.ID == "" {
			t.Error("Enqueue must assign an id")
		}

		// A sweeper runs repeatedly; the second placement must be refused
		// rather than stack up a second identical task.
		again := &QueueItem{
			Queue: QueueTicketing, PNRID: p.ID, Code: "tktl_expired",
			Reason: "ticketing time limit passed", PlacedBy: "sweeper",
		}
		if err := s.Enqueue(ctx, again); !errors.Is(err, ErrDuplicate) {
			t.Fatalf("second placement = %v, want ErrDuplicate", err)
		}

		// A different reason on the same queue is a different task.
		other := &QueueItem{
			Queue: QueueTicketing, PNRID: p.ID, Code: "tktl_near",
			Reason: "ticketing time limit approaching", PlacedBy: "sweeper",
		}
		if err := s.Enqueue(ctx, other); err != nil {
			t.Fatalf("distinct code must be allowed: %v", err)
		}

		counts, err := s.QueueCounts(ctx)
		if err != nil {
			t.Fatalf("QueueCounts: %v", err)
		}
		if counts[QueueTicketing] != 2 {
			t.Errorf("pending on %s = %d, want 2", QueueTicketing, counts[QueueTicketing])
		}

		pending, err := s.ListQueue(ctx, QueueFilter{Queue: QueueTicketing})
		if err != nil {
			t.Fatalf("ListQueue: %v", err)
		}
		if len(pending) != 2 {
			t.Fatalf("ListQueue returned %d items, want 2", len(pending))
		}
		for _, it := range pending {
			if !it.Pending() {
				t.Errorf("item %s is not pending", it.ID)
			}
		}

		if err := s.WorkQueueItem(ctx, item.ID, "adam", "ticketed manually"); err != nil {
			t.Fatalf("WorkQueueItem: %v", err)
		}
		// Working twice must not silently overwrite who cleared it.
		if err := s.WorkQueueItem(ctx, item.ID, "someone", ""); !errors.Is(err, ErrConflict) {
			t.Errorf("re-working = %v, want ErrConflict", err)
		}
		if err := s.WorkQueueItem(ctx, "nosuchid", "adam", ""); !errors.Is(err, ErrNotFound) {
			t.Errorf("working a missing item = %v, want ErrNotFound", err)
		}

		counts, _ = s.QueueCounts(ctx)
		if counts[QueueTicketing] != 1 {
			t.Errorf("after working, pending = %d, want 1", counts[QueueTicketing])
		}

		// Worked items stay for the audit trail, but out of the working view.
		all, err := s.ListQueue(ctx, QueueFilter{Queue: QueueTicketing, IncludeWorked: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(all) != 2 {
			t.Fatalf("IncludeWorked returned %d, want 2", len(all))
		}
		var worked *QueueItem
		for _, it := range all {
			if it.ID == item.ID {
				worked = it
			}
		}
		if worked == nil {
			t.Fatal("worked item disappeared from the audit view")
		}
		if worked.Pending() || worked.WorkedBy != "adam" || worked.Note != "ticketed manually" {
			t.Errorf("working was not recorded: %+v", worked)
		}

		// Once worked, the same reason can recur: the situation can happen again.
		if err := s.Enqueue(ctx, &QueueItem{
			Queue: QueueTicketing, PNRID: p.ID, Code: "tktl_expired",
			Reason: "again", PlacedBy: "sweeper",
		}); err != nil {
			t.Errorf("re-placing a worked reason must be allowed: %v", err)
		}
	})
}

func TestQueueFilterByRecord(t *testing.T) {
	eachBackend(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		a, b := samplePNR("QUE222"), samplePNR("QUE333")
		for _, p := range []*pnr.PNR{a, b} {
			if err := s.CreatePNR(ctx, p, nil); err != nil {
				t.Fatal(err)
			}
		}
		for _, p := range []*pnr.PNR{a, b} {
			if err := s.Enqueue(ctx, &QueueItem{
				Queue: QueueConfirmation, PNRID: p.ID, Code: "kk", Reason: "confirmed",
				PlacedBy: "gateway",
			}); err != nil {
				t.Fatal(err)
			}
		}
		got, err := s.ListQueue(ctx, QueueFilter{PNRID: a.ID})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].PNRID != a.ID {
			t.Errorf("filter by record returned %d items: %+v", len(got), got)
		}
	})
}

// An interline record has one segment per carrier, and each carrier answers
// separately. Two partners confirming the same booking are two pieces of work;
// collapsing them onto one queue item loses the second carrier's answer.
func TestQueueDistinguishesSegments(t *testing.T) {
	eachBackend(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		p := samplePNR("SEG111")
		if err := s.CreatePNR(ctx, p, nil); err != nil {
			t.Fatal(err)
		}
		for _, ref := range []int{1, 2} {
			err := s.Enqueue(ctx, &QueueItem{
				Queue: QueueConfirmation, PNRID: p.ID, Locator: p.RecordLocator,
				Code: "confirmed_HK", Reason: "partner confirmed",
				SegmentRef: ref, PlacedBy: "gateway",
			})
			if err != nil {
				t.Fatalf("segment %d: %v", ref, err)
			}
		}
		items, err := s.ListQueue(ctx, QueueFilter{Queue: QueueConfirmation})
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 2 {
			t.Fatalf("got %d items, want one per segment", len(items))
		}
		// The same segment repeating is still a duplicate.
		err = s.Enqueue(ctx, &QueueItem{
			Queue: QueueConfirmation, PNRID: p.ID, Code: "confirmed_HK",
			Reason: "again", SegmentRef: 1, PlacedBy: "gateway",
		})
		if !errors.Is(err, ErrDuplicate) {
			t.Errorf("repeat for segment 1 = %v, want ErrDuplicate", err)
		}
	})
}

// The two directions are separate namespaces. An acknowledgement we sent and a
// request a partner sent can carry the same reference, and confusing them would
// let a message we originated suppress one that arrived.
func TestDedupLookupIsScopedByDirection(t *testing.T) {
	eachBackend(t, func(t *testing.T, s Store) {
		ctx := context.Background()

		in := newMsg("BA", []byte("inbound"))
		in.DedupKey = "unb:REF1"
		if err := s.AppendMessage(ctx, in); err != nil {
			t.Fatal(err)
		}
		out := newMsg("BA", []byte("outbound"))
		out.Direction = Outbound
		out.Status = StatusSent
		out.DedupKey = "unb:REF1"
		if err := s.AppendMessage(ctx, out); err != nil {
			t.Fatal(err)
		}

		gotIn, ok, err := s.FindByDedupKey(ctx, "BA", "unb:REF1")
		if err != nil || !ok {
			t.Fatalf("FindByDedupKey = %q, %v, %v", gotIn, ok, err)
		}
		if gotIn != in.ID {
			t.Errorf("inbound lookup returned %s, want the inbound message", gotIn)
		}

		gotOut, ok, err := s.FindOutboundByKey(ctx, "BA", "unb:REF1")
		if err != nil || !ok {
			t.Fatalf("FindOutboundByKey = %q, %v, %v", gotOut, ok, err)
		}
		if gotOut != out.ID {
			t.Errorf("outbound lookup returned %s, want the outbound message", gotOut)
		}

		if _, ok, _ := s.FindOutboundByKey(ctx, "BA", "unb:NOSUCH"); ok {
			t.Error("an unknown key must not match")
		}
		if _, ok, _ := s.FindOutboundByKey(ctx, "LH", "unb:REF1"); ok {
			t.Error("keys must not cross peers")
		}
	})
}
