package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/adamf/jetway/pkg/store"
)

// pinClocks installs one frozen instant on every clock a node reads.
func pinClocks(gds, air *node, at time.Time) {
	tick := func() time.Time { return at }
	for _, n := range []*node{gds, air} {
		n.gw.Now = tick
		n.gw.Bus.Now = tick
		n.st.Now = tick
		if n.gw.Queues != nil {
			n.gw.Queues.Now = tick
		}
	}
}

// With the clock pinned, every timestamp the system writes -- records,
// events, messages, both sides of the wire -- must be the pinned instant.
// Any site still reading the wall clock shows up here as a mismatch, which
// is the point: this test is the sweep that keeps the seam complete.
func TestPinnedClockStampsEverything(t *testing.T) {
	ctx := context.Background()
	gds, air := wire(t, "BA", store.FormatEDIFACT)
	at := time.Date(2030, time.June, 1, 12, 0, 0, 0, time.UTC)
	pinClocks(gds, air, at)

	res, err := gds.gw.Book(ctx, booking("Y", 1))
	if err != nil {
		t.Fatalf("Book: %v", err)
	}
	if _, err := gds.gw.Cancel(ctx, res.PNR.RecordLocator, CancelOptions{By: "test"}); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	for side, n := range map[string]*node{"gds": gds, "carrier": air} {
		recs, _ := n.st.ListPNRs(ctx, 10)
		if len(recs) == 0 {
			t.Fatalf("%s: no records", side)
		}
		for _, rec := range recs {
			if !rec.CreatedAt.Equal(at) || !rec.UpdatedAt.Equal(at) {
				t.Errorf("%s record %s stamped %v/%v, want the pinned clock %v",
					side, rec.RecordLocator, rec.CreatedAt, rec.UpdatedAt, at)
			}
			evs, _ := n.st.Events(ctx, rec.ID)
			if len(evs) == 0 {
				t.Errorf("%s record %s has no events", side, rec.RecordLocator)
			}
			for _, e := range evs {
				if !e.At.Equal(at) {
					t.Errorf("%s event %q stamped %v, want %v", side, e.Type, e.At, at)
				}
			}
		}
		msgs, _ := n.st.ListMessages(ctx, store.MessageFilter{Limit: 50})
		if len(msgs) == 0 {
			t.Fatalf("%s: no messages", side)
		}
		for _, m := range msgs {
			if !m.At.Equal(at) {
				t.Errorf("%s message %s (%s) stamped %v, want %v", side, m.ID, m.Kind, m.At, at)
			}
		}
	}
}
