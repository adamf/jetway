package queue

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/adamf/jetway/pkg/pnr"
	"github.com/adamf/jetway/pkg/rescode"
	"github.com/adamf/jetway/pkg/store"
)

// Sweeper places records on queues for things that happen because time passed
// rather than because a message arrived.
//
// It is the half of queueing that nothing else can do. A partner that answers
// puts work on a queue by answering; a partner that never answers puts work on
// no queue at all unless something notices the silence. Same for a ticketing
// time limit: the deadline passing is not an event anyone sends.
//
// The due-date predicates are in the query, not in this loop, and that is a
// correctness property rather than an optimisation. A pass used to read the
// most recently updated records and look for stale ones among them, which is
// inverted -- the freshest records are by definition not the stale ones. Above
// a few hundred records a ticketing time limit could never fire and an
// unanswered segment could never be raised, with no error and no log line.
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
	// Limit bounds records handled per pass. The store returns the most
	// overdue first, so this drops the least urgent work rather than
	// concealing all of it. Zero uses DefaultSweepLimit.
	Limit int

	// Cancel, when set, cancels a booking whose ticketing time limit has
	// passed. Nil leaves the record alone and only raises it on a queue.
	//
	// It is an interface because the package that implements it imports this
	// one, and it is optional because auto-cancel is a real cancellation: it
	// gives seats back and tells the carriers. A deployment should have to ask
	// for that rather than discover it.
	Cancel Canceller

	Now func() time.Time
}

// Canceller cancels a booking whose time limit has passed.
type Canceller interface {
	// CancelExpired withdraws every live segment and notifies the carriers.
	// It returns the carriers that could not be told, which is the part the
	// caller must not treat as success.
	CancelExpired(ctx context.Context, locator, reason string) (unreachable []string, err error)
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
	now := s.now()
	limit := s.limit()

	// Two questions, because they are two different kinds of due and each has
	// its own index. Asking one question and filtering in Go is what produced
	// the inverted scan.
	stale, err := s.Records.FindPNRsStale(ctx, now.Add(-s.pendingAfter()), limit)
	if err != nil {
		return 0, fmt.Errorf("queue: sweep for stalled records: %w", err)
	}
	due, err := s.Records.FindPNRsDueBy(ctx, now.Add(s.ticketingLead()), limit)
	if err != nil {
		return 0, fmt.Errorf("queue: sweep for ticketing deadlines: %w", err)
	}

	// A record can be both, and sweeping it twice would place the same work
	// twice -- the queue would dedupe it, but the pass would report a count
	// nobody could reconcile.
	seen := make(map[string]bool, len(stale)+len(due))
	placed := 0
	for _, recs := range [][]*pnr.PNR{due, stale} {
		for _, rec := range recs {
			if seen[rec.ID] {
				continue
			}
			seen[rec.ID] = true
			n, err := s.sweepRecord(ctx, rec, now)
			if err != nil {
				// One bad record must not stop the pass; the rest still need doing.
				s.log().Error("sweep failed for a record", "locator", rec.RecordLocator, "err", err)
				continue
			}
			placed += n
		}
	}
	if len(stale) >= limit || len(due) >= limit {
		// The overflow is the least urgent work and the next pass will take it,
		// but saying nothing would let a permanent backlog look like a quiet
		// system.
		s.log().Warn("a sweep hit its limit and left work for the next pass",
			"limit", limit, "stale", len(stale), "due", len(due))
	}
	return placed, nil
}

func (s *Sweeper) sweepRecord(ctx context.Context, rec *pnr.PNR, now time.Time) (int, error) {
	placed := 0

	// A ticketed record owes nobody a ticketing deadline. Raising one anyway
	// would put a record already dealt with in front of somebody every pass.
	ticketed := rec.Ticketed()
	for _, tk := range rec.Ticketing {
		if tk.Deadline == nil || ticketed {
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
		// Raising it is the notification; cancelling is the consequence, and
		// only where a deployment has asked for it.
		if code == "tktl_expired" && s.Cancel != nil {
			unreachable, err := s.Cancel.CancelExpired(ctx, rec.RecordLocator,
				"ticketing time limit passed at "+tk.Deadline.Format(time.RFC3339))
			if err != nil {
				s.log().Error("could not cancel a record past its ticketing limit",
					"locator", rec.RecordLocator, "err", err)
			} else if len(unreachable) > 0 {
				s.log().Warn("cancelled a record past its ticketing limit, but not every carrier was told",
					"locator", rec.RecordLocator, "unreachable", unreachable)
			}
			// The record is cancelled now, so nothing further on it is owed.
			return placed, nil
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
