package queue

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/adamf/jetway/internal/store"
	"github.com/adamf/jetway/pkg/pnr"
	"github.com/adamf/jetway/pkg/rescode"
)

// Sweeper places records on queues for things that happen because time passed
// rather than because a message arrived.
//
// It is the half of queueing that nothing else can do. A partner that answers
// puts work on a queue by answering; a partner that never answers puts work on
// no queue at all unless something notices the silence. Same for a ticketing
// time limit: the deadline passing is not an event anyone sends.
//
// Scanning is a full pass over the record list. That is honest for the volumes
// this runs at today and wrong for a large deployment, which wants the due-date
// predicates pushed into an indexed query instead; Limit is here to bound the
// damage in the meantime.
type Sweeper struct {
	Records store.Store
	Queues  *Manager
	Log     *slog.Logger

	// PendingAfter is how long a requested segment may go unanswered before it
	// becomes someone's problem. Zero uses DefaultPendingAfter.
	PendingAfter time.Duration
	// TicketingLead is how far ahead of a ticketing deadline to raise it. Zero
	// uses DefaultTicketingLead.
	TicketingLead time.Duration
	// Limit bounds records examined per pass. Zero uses DefaultSweepLimit.
	Limit int

	Now func() time.Time
}

// Sweep defaults.
const (
	DefaultPendingAfter  = 6 * time.Hour
	DefaultTicketingLead = 24 * time.Hour
	DefaultSweepLimit    = 500
)

func (s *Sweeper) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}

func (s *Sweeper) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func (s *Sweeper) pendingAfter() time.Duration {
	if s.PendingAfter > 0 {
		return s.PendingAfter
	}
	return DefaultPendingAfter
}

func (s *Sweeper) ticketingLead() time.Duration {
	if s.TicketingLead > 0 {
		return s.TicketingLead
	}
	return DefaultTicketingLead
}

func (s *Sweeper) limit() int {
	if s.Limit > 0 {
		return s.Limit
	}
	return DefaultSweepLimit
}

// Sweep makes one pass and returns how many new placements it made.
func (s *Sweeper) Sweep(ctx context.Context) (int, error) {
	if s.Records == nil || s.Queues == nil {
		return 0, nil
	}
	recs, err := s.Records.ListPNRs(ctx, s.limit())
	if err != nil {
		return 0, fmt.Errorf("queue: sweep: %w", err)
	}
	now := s.now()
	placed := 0
	for _, rec := range recs {
		if rec.Status == pnr.StatusCancelled {
			continue
		}
		n, err := s.sweepRecord(ctx, rec, now)
		if err != nil {
			// One bad record must not stop the pass; the rest still need doing.
			s.log().Error("sweep failed for a record", "locator", rec.RecordLocator, "err", err)
			continue
		}
		placed += n
	}
	return placed, nil
}

func (s *Sweeper) sweepRecord(ctx context.Context, rec *pnr.PNR, now time.Time) (int, error) {
	placed := 0

	for _, tk := range rec.Ticketing {
		if tk.Deadline == nil {
			continue
		}
		var code, reason string
		switch {
		case now.After(*tk.Deadline):
			code = "tktl_expired"
			reason = "ticketing time limit passed at " + tk.Deadline.Format(time.RFC3339)
		case tk.Deadline.Sub(now) <= s.ticketingLead():
			code = "tktl_near"
			reason = "ticketing time limit at " + tk.Deadline.Format(time.RFC3339)
		default:
			continue
		}
		ok, err := s.Queues.Place(ctx, &store.QueueItem{
			Queue: store.QueueTicketing, PNRID: rec.ID, Locator: rec.RecordLocator,
			Code: code, Reason: reason, PlacedBy: "sweeper",
		})
		if err != nil {
			return placed, err
		}
		if ok {
			placed++
		}
	}

	// A request that has gone unanswered for longer than the agreed time is a
	// stalled conversation, not a settled record.
	if now.Sub(rec.UpdatedAt) >= s.pendingAfter() {
		for i := range rec.Segments {
			seg := &rec.Segments[i]
			if !awaitingReply(seg.Status) {
				continue
			}
			ok, err := s.Queues.PlaceForSegment(ctx, rec, seg, store.QueuePending,
				"unanswered_"+seg.Status,
				fmt.Sprintf("segment %d has sat at %s since %s with no reply",
					seg.Ref, seg.Status, rec.UpdatedAt.Format(time.RFC3339)),
				"")
			if err != nil {
				return placed, err
			}
			if ok {
				placed++
			}
		}
	}
	return placed, nil
}

// awaitingReply reports whether a segment status means we are still waiting on
// a partner.
func awaitingReply(status string) bool {
	c := rescode.ActionCode(status)
	info, ok := c.Info()
	if !ok {
		return false
	}
	switch info.Category {
	case rescode.CatRequest:
		// A sold-from-availability segment is not waiting on anyone.
		return !info.Confirmed
	case rescode.CatHolding:
		// Holding-need and pending are unfinished; holding-confirmed is not.
		return !info.Confirmed && !info.Waitlisted
	}
	return false
}

// Run sweeps on a ticker until the context is cancelled.
func (s *Sweeper) Run(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = time.Minute
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		n, err := s.Sweep(ctx)
		if err != nil {
			s.log().Error("queue sweep failed", "err", err)
			continue
		}
		if n > 0 {
			s.log().Info("queue sweep placed records", "count", n)
		}
	}
}
