// Package irops re-accommodates passengers when the schedule breaks under
// them: irregular operations, the day the airline earns its keep.
//
// A schedule change arrives as an SSM or ASM and the gateway queues every
// booking it touches. That is where the work stops without this package:
// the record sits on the schedule-change queue for a person. The Engine is
// the person, for the common case -- a cancelled flight -- and does what
// they would do: find the next flight that can carry the passenger over the
// same city pair, sell it on the record, drop the dead leg, and work the
// queue item with a note saying what it did. What it cannot do -- no seat
// anywhere, no schedule to look in -- it leaves on the queue, for a person.
//
// The schedule is a seam. A distribution system knows the schedule from
// SSIM, from its own flight tables, from a partner's; this package asks for
// alternatives and does not care where they came from. Availability is the
// gateway's cache: the same one that decides free sale for a new booking
// decides whether an alternative is worth asking for.
package irops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/adamf/jetway/pkg/avail"
	"github.com/adamf/jetway/pkg/gateway"
	"github.com/adamf/jetway/pkg/pnr"
	"github.com/adamf/jetway/pkg/queue"
	"github.com/adamf/jetway/pkg/rescode"
	"github.com/adamf/jetway/pkg/store"
)

// Candidate is a flight that could carry a passenger instead.
type Candidate struct {
	Carrier   string
	FlightNum string
	Board     string
	Off       string
	// Depart is the departure date; the time of day is optional and, when
	// present, orders candidates.
	Depart     time.Time
	DepartTime string // HHMM local, optional
	ArriveTime string // HHMM local, optional
}

// Describe renders the candidate for notes and logs.
func (c Candidate) Describe() string {
	return fmt.Sprintf("%s%s %s %s-%s", c.Carrier, c.FlightNum, pnr.FormatDate(c.Depart), c.Board, c.Off)
}

// Schedule is where alternatives come from.
type Schedule interface {
	// Alternatives lists flights over the dead segment's city pair that
	// depart after it, nearest first: the same day, then the next. The
	// dead flight itself must not be among them.
	Alternatives(ctx context.Context, dead pnr.Segment) ([]Candidate, error)
}

// ScheduleFunc adapts a function to Schedule.
type ScheduleFunc func(ctx context.Context, dead pnr.Segment) ([]Candidate, error)

// Alternatives implements Schedule.
func (f ScheduleFunc) Alternatives(ctx context.Context, dead pnr.Segment) ([]Candidate, error) {
	return f(ctx, dead)
}

// Outcome is what one rebooking did.
type Outcome struct {
	Locator string `json:"locator"`
	From    string `json:"from"` // the dead segment
	To      string `json:"to"`   // the new one
	Class   string `json:"class"`
	Tried   int    `json:"tried"` // candidates asked before one had a seat
	// Waitlisted names the alternatives the passenger now holds a waitlist
	// on, when no seat could be confirmed: the item stays for a person, but
	// the passenger is in the queue for the seats that may free up.
	Waitlisted []string `json:"waitlisted,omitempty"`
}

// ErrNoAlternative is returned when nothing could carry the passenger. The
// queue item stays where it is.
var ErrNoAlternative = errors.New("irops: no alternative with a seat")

// ErrWaitlisted is what Rebook returns when nothing confirmed but the
// passenger was waitlisted on one or more alternatives: not solved, not
// hopeless, and a person should know which.
var ErrWaitlisted = errors.New("irops: waitlisted, no seat confirmed")

// Engine works the schedule-change queue.
type Engine struct {
	Gateway  *gateway.Gateway
	Store    store.Store
	Queues   *queue.Manager
	Schedule Schedule
	// By is the actor recorded on everything the engine does.
	By string
	// MaxCandidates bounds how many alternatives are tried per booking.
	// Zero means five.
	MaxCandidates int
	// Classes is the order of booking classes to try when the passenger's own
	// is closed on an alternative; Y is always tried last. A downgrade is a
	// rebooking too; an upgrade to protect the journey is what a real desk
	// does when the cabin is full, and is left to the person.
	Classes []string
	// AskCarriers lets the engine fall back to requesting a seat the cache
	// does not show open -- a closed or unknown flight -- when nothing is
	// on free sale. The passenger then holds an HN awaiting the carrier's
	// answer rather than a confirmed seat. Off by default: a desk confirms
	// or hands over, it does not leave people hoping.
	AskCarriers bool
	// OnRebooked, when set, is told about each success.
	OnRebooked func(ctx context.Context, item *store.QueueItem, out Outcome)
	// ReplyTimeout is how long the engine waits for a carrier to answer a
	// seat it asked for before it gives that alternative up and tries the
	// next. A sell the engine makes is a request until the carrier says
	// otherwise, and dropping the dead leg on the strength of a request is
	// how a passenger ends up holding nothing. Zero means ten seconds.
	ReplyTimeout time.Duration
	// RetryAfter is how long an item nothing could be found for is left
	// before it is looked at again. Zero means ten minutes.
	RetryAfter time.Duration
	Log        *slog.Logger

	mu    sync.Mutex
	tried map[string]time.Time
}

func (e *Engine) log() *slog.Logger {
	if e.Log == nil {
		return slog.Default()
	}
	return e.Log
}

func (e *Engine) max() int {
	if e.MaxCandidates > 0 {
		return e.MaxCandidates
	}
	return 5
}

// codeCancelled is the queue code applySchedule uses for a cancellation.
const codeCancelled = "schedule_cnl"

// Work makes one pass over the pending schedule-change queue and rebooks
// what it can. It returns how many bookings it moved.
func (e *Engine) Work(ctx context.Context) (int, error) {
	items, err := e.Store.ListQueue(ctx, store.QueueFilter{Queue: store.QueueScheduleChange, Limit: 500})
	if err != nil {
		return 0, err
	}
	moved := 0
	for _, it := range items {
		if ctx.Err() != nil {
			return moved, ctx.Err()
		}
		if it.Code != codeCancelled || !it.Pending() {
			continue
		}
		if e.recentlyTried(it.ID) {
			continue
		}
		out, err := e.Rebook(ctx, it)
		switch {
		case err == nil:
			moved++
			if e.OnRebooked != nil {
				e.OnRebooked(ctx, it, *out)
			}
		case errors.Is(err, ErrWaitlisted):
			e.markTried(it.ID)
			e.log().Info("irops: waitlisted, no seat confirmed; left for a person", "locator", it.Locator, "waitlisted", out.Waitlisted)
		case errors.Is(err, ErrNoAlternative):
			e.markTried(it.ID)
			e.log().Info("irops: nothing to rebook onto; left for a person", "locator", it.Locator, "reason", it.Reason)
		default:
			e.markTried(it.ID)
			e.log().Warn("irops: rebooking failed", "locator", it.Locator, "err", err)
		}
	}
	return moved, nil
}

func (e *Engine) recentlyTried(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	at, ok := e.tried[id]
	if !ok {
		return false
	}
	after := e.RetryAfter
	if after <= 0 {
		after = 10 * time.Minute
	}
	if time.Since(at) > after {
		delete(e.tried, id)
		return false
	}
	return true
}

func (e *Engine) markTried(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.tried == nil {
		e.tried = map[string]time.Time{}
	}
	e.tried[id] = time.Now()
}

// Rebook handles one queue item: the booking it names, on the segment it
// names, onto the first alternative with a seat.
func (e *Engine) Rebook(ctx context.Context, it *store.QueueItem) (*Outcome, error) {
	rec, err := e.Store.GetPNRByID(ctx, it.PNRID)
	if err != nil {
		return nil, err
	}
	dead := rec.SegmentByRef(it.SegmentRef)
	if dead == nil {
		return nil, fmt.Errorf("irops: %s has no segment %d", rec.RecordLocator, it.SegmentRef)
	}
	if dead.Status == "XX" {
		// Somebody already dealt with it; the item is stale.
		if err := e.Queues.Work(ctx, it.ID, e.By, "segment already cancelled"); err != nil {
			return nil, err
		}
		return &Outcome{Locator: rec.RecordLocator, From: dead.Describe()}, nil
	}
	cands, err := e.Schedule.Alternatives(ctx, *dead)
	if err != nil {
		return nil, err
	}
	deadKey := dead.Key()
	tried := 0
	// What this pass has put on the record and not confirmed: waitlists to
	// keep if nothing confirms, and to drop if something does.
	var waitlistRefs []int
	var waitlisted []string
	cancelRefs := func(refs []int, reason string) {
		if len(refs) == 0 {
			return
		}
		if _, err := e.Gateway.Cancel(ctx, rec.RecordLocator, gateway.CancelOptions{Segments: refs, By: e.By, Reason: reason}); err != nil && !errors.Is(err, gateway.ErrNothingToCancel) {
			e.log().Warn("irops: could not cancel", "locator", rec.RecordLocator, "refs", refs, "err", err)
		}
	}
	// try asks for one alternative and waits for the answer. confirmed is
	// the passenger seated; otherwise the segment was waitlisted, refused
	// or unanswered and the search goes on.
	try := func(c Candidate, class string) (out *Outcome, confirmed bool) {
		wire := pnr.FormatDate(c.Depart)
		probe := pnr.Segment{Carrier: c.Carrier, FlightNum: c.FlightNum, Class: class, WireDate: wire, Board: c.Board, Off: c.Off}
		if probe.Key() == deadKey {
			return nil, false
		}
		tried++
		seg := gateway.BookingSegment{
			Carrier: c.Carrier, FlightNum: c.FlightNum, Class: class, Date: wire,
			Board: c.Board, Off: c.Off, Seats: dead.Seats,
			DepartTime: c.DepartTime, ArriveTime: c.ArriveTime,
		}
		reason := "rebooked from " + dead.Describe() + ": " + it.Reason
		updated, err := e.Gateway.AddSegment(ctx, rec.RecordLocator, seg, e.By, reason)
		if err != nil {
			e.log().Debug("irops: alternative refused", "locator", rec.RecordLocator, "candidate", c.Describe(), "err", err)
			return nil, false
		}
		ref := 0
		for _, s := range updated.Segments {
			if s.Carrier == c.Carrier && s.FlightNum == c.FlightNum && s.Board == c.Board && s.Off == c.Off && s.Status != "XX" {
				ref = s.Ref
			}
		}
		if ref == 0 {
			return nil, false
		}
		status, describe := e.awaitAnswer(ctx, rec.RecordLocator, ref)
		code := rescode.ActionCode(status)
		switch {
		case code.Confirmed():
			// The dead leg goes, and so does any waitlist this pass took
			// out on the way here.
			cancelRefs(append([]int{it.SegmentRef}, waitlistRefs...), "flight cancelled; rebooked onto "+c.Describe())
			note := fmt.Sprintf("rebooked %s -> %s", dead.Describe(), describe)
			if err := e.Queues.Work(ctx, it.ID, e.By, note); err != nil {
				e.log().Warn("irops: rebooked but could not work the queue item", "locator", rec.RecordLocator, "err", err)
			}
			e.log().Info("irops: rebooked", "locator", rec.RecordLocator, "from", dead.Describe(), "to", describe, "tried", tried)
			return &Outcome{Locator: rec.RecordLocator, From: dead.Describe(), To: describe, Class: class, Tried: tried}, true
		case code.Waitlisted():
			waitlistRefs = append(waitlistRefs, ref)
			waitlisted = append(waitlisted, describe)
			e.log().Info("irops: waitlisted", "locator", rec.RecordLocator, "on", describe)
			return nil, false
		default:
			// Refused, or no answer in time: the request comes off the
			// record. A late confirmation is re-cancelled by the gateway.
			why := "refused " + status
			if status == "" || code.NeedsReply() {
				why = "no answer from the carrier"
			}
			e.log().Debug("irops: alternative not held", "locator", rec.RecordLocator, "candidate", c.Describe(), "status", status)
			cancelRefs([]int{ref}, why)
			return nil, false
		}
	}
	decide := func(c Candidate, class string) avail.Decision {
		if e.Gateway.Avail == nil {
			return avail.Ask
		}
		d, _ := e.Gateway.Avail.Decide(avail.NewKey(c.Carrier, c.FlightNum, c.Depart, c.Board, c.Off, class), dead.Seats)
		return d
	}
	for _, c := range cands {
		if tried >= e.max() {
			break
		}
		for _, class := range e.classesFor(dead.Class) {
			if decide(c, class) != avail.FreeSale {
				continue
			}
			if out, ok := try(c, class); ok {
				return out, nil
			}
		}
	}
	if e.AskCarriers {
		for _, c := range cands {
			if tried >= e.max() {
				break
			}
			for _, class := range e.classesFor(dead.Class) {
				if d := decide(c, class); d != avail.Ask && d != avail.AskWaitlist {
					continue
				}
				if out, ok := try(c, class); ok {
					return out, nil
				}
			}
		}
	}
	if len(waitlisted) > 0 {
		return &Outcome{Locator: rec.RecordLocator, From: dead.Describe(), Tried: tried, Waitlisted: waitlisted}, ErrWaitlisted
	}
	return nil, ErrNoAlternative
}

// awaitAnswer waits for the carrier's answer to a segment the engine
// requested: the status once it is no longer a request, or the request
// status itself when ReplyTimeout passes first.
func (e *Engine) awaitAnswer(ctx context.Context, locator string, ref int) (status, describe string) {
	timeout := e.ReplyTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		rec, err := e.Store.GetPNR(ctx, locator)
		if err == nil {
			if s := rec.SegmentByRef(ref); s != nil {
				status, describe = s.Status, s.Describe()
				if !rescode.ActionCode(s.Status).NeedsReply() && s.Status != "HN" && s.Status != "PN" {
					return status, describe
				}
			}
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return status, describe
		}
		select {
		case <-ctx.Done():
			return status, describe
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// classesFor is the order of classes to try: the passenger's own, then the
// configured fallbacks, then Y.
func (e *Engine) classesFor(own string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(c string) {
		if c != "" && !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	add(own)
	for _, c := range e.Classes {
		add(c)
	}
	add("Y")
	return out
}

// Run works the queue every interval until the context ends.
func (e *Engine) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n, err := e.Work(ctx); err != nil && ctx.Err() == nil {
				e.log().Warn("irops: pass failed", "err", err)
			} else if n > 0 {
				e.log().Info("irops: pass", "rebooked", n)
			}
		}
	}
}
