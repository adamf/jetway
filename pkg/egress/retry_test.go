package egress

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/adamf/jetway/pkg/config"
	"github.com/adamf/jetway/pkg/store"
)

// recorder is a sender that fails until told to succeed, then records order.
type recorder struct {
	mu   sync.Mutex
	fail bool
	sent []string
}

func (r *recorder) Send(ctx context.Context, raw []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail {
		return errors.New("link down")
	}
	r.sent = append(r.sent, string(raw))
	return nil
}

func (r *recorder) Describe() string { return "recorder" }

func (r *recorder) order() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.sent...)
}

func typeB(priority, text string) []byte {
	return []byte(priority + " LHRRMBA\n.LONXX1A 121430\n" + text + "\n")
}

// A link that has been down comes back with a backlog, and the order that
// backlog goes out in is the whole point of the priority code.
func TestBacklogDrainsUrgentFirst(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := NewRouter(nil, log)
	rec := &recorder{fail: true}
	r.Register("BA", rec, config.Retry{MaxAttempts: 5, Initial: time.Millisecond}, store.FormatTypeB)

	// Queued in the least helpful order on purpose.
	for _, m := range []struct{ prio, text string }{
		{"QN", "bulk"}, {"QD", "deferred"}, {"QU", "urgent"},
		{"QK", "normal"}, {"QS", "service"},
	} {
		if err := r.Send(context.Background(), "BA", typeB(m.prio, m.text)); err == nil {
			t.Fatal("the link is down; Send should have reported it")
		}
	}
	if r.QueueDepth() != 5 {
		t.Fatalf("QueueDepth = %d, want 5", r.QueueDepth())
	}

	rec.mu.Lock()
	rec.fail = false
	rec.mu.Unlock()
	time.Sleep(20 * time.Millisecond)
	r.drain(context.Background())

	got := rec.order()
	if len(got) != 5 {
		t.Fatalf("delivered %d of 5: %v", len(got), got)
	}
	band := func(s string) int {
		switch {
		case contains(s, "urgent"), contains(s, "service"):
			return 0
		case contains(s, "deferred"), contains(s, "bulk"):
			return 2
		}
		return 1
	}
	for i := 1; i < len(got); i++ {
		if band(got[i-1]) > band(got[i]) {
			t.Fatalf("backlog drained out of priority order: %v", got)
		}
	}
}

// EDIFACT carries no priority line, so everything on such a link is normal and
// nothing may be reordered on the strength of bytes that happen to look like
// a priority code.
func TestNonTypeBIsAlwaysNormal(t *testing.T) {
	if classOf(store.FormatEDIFACT, typeB("QU", "x")) != PriorityNormal {
		t.Error("an EDIFACT link must not be read for Type B priorities")
	}
	if classOf(store.FormatTypeB, typeB("QU", "x")) != PriorityUrgent {
		t.Error("a Type B urgent message should be urgent")
	}
	// An unknown code must never jump the queue.
	if classOf(store.FormatTypeB, typeB("QZ", "x")) != PriorityNormal {
		t.Error("an unrecognised priority code must be normal")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
