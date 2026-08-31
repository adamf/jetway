// Package queue turns things that happened into work someone has to do.
//
// # Why the state lives in the store
//
// A reservations queue is not a message broker topic, and the difference
// decides the architecture. A broker queue is a transport: a message is
// delivered, acknowledged and gone. A reservations queue is a worklist that has
// to be listed, counted, filtered and re-read, and whose items survive being
// worked because "who cleared this and when" is the question asked after an
// interline dispute. Those are database semantics, not transport semantics, so
// queue state is held in store.QueueStore alongside the records it refers to.
//
// # Where an external system plugs in
//
// What an external queueing system is genuinely good at is the other half:
// telling something that work has arrived. Publisher is that seam. A placement
// is written to the store first and published second, in the same order and for
// the same reason as capture-before-parse on the inbound path: if the publish
// fails the work still exists and will be found by the next reader, whereas a
// publish that succeeded before the write could announce work nobody can look
// up.
//
// So: Postgres today, another store.QueueStore tomorrow if the worklist wants
// to live somewhere else, and a Publisher whenever a robot or an external
// broker needs to be woken rather than to poll.
package queue

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/adamf/jetway/pkg/metrics"
	"github.com/adamf/jetway/pkg/pnr"
	"github.com/adamf/jetway/pkg/rescode"
	"github.com/adamf/jetway/pkg/store"
	"github.com/adamf/jetway/pkg/telemetry"

	"go.opentelemetry.io/otel/trace"
)

// Publisher is an optional sink notified after a placement is durable.
//
// Implementations may be an external broker, a webhook, or an in-process
// channel feeding a robot. A Publisher must not be relied on for durability:
// the item is already stored before it is called, and an error from it is
// logged rather than propagated, because failing the placement would discard
// work that has already been recorded.
type Publisher interface {
	Publish(ctx context.Context, item *store.QueueItem) error
}

// PublisherFunc adapts a function to Publisher.
type PublisherFunc func(ctx context.Context, item *store.QueueItem) error

// Publish calls f.
func (f PublisherFunc) Publish(ctx context.Context, item *store.QueueItem) error {
	return f(ctx, item)
}

// Manager places records on queues.
type Manager struct {
	Store store.QueueStore
	// Publish is optional. Nil means nothing external is notified.
	Publish Publisher
	Log     *slog.Logger
	// Now is overridable for tests.
	Now func() time.Time
	// Notify is called after a placement lands, for the console event bus.
	Notify func(item *store.QueueItem)
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now().UTC()
}

func (m *Manager) log() *slog.Logger {
	if m.Log != nil {
		return m.Log
	}
	return slog.Default()
}

// Place puts a record on a queue, reporting whether it was newly placed.
//
// A repeat placement for the same reason while the item is still pending is not
// an error: it is a sweeper doing its job on a schedule. It reports false so a
// caller can tell "I created work" from "the work was already there".
func (m *Manager) Place(ctx context.Context, item *store.QueueItem) (bool, error) {
	if m.Store == nil {
		return false, errors.New("queue: no store configured")
	}
	if item.Queue == "" {
		item.Queue = store.QueueGeneral
	}
	if item.PlacedAt.IsZero() {
		item.PlacedAt = m.now()
	}
	if item.PlacedBy == "" {
		item.PlacedBy = "gateway"
	}
	err := m.Store.Enqueue(ctx, item)
	switch {
	case errors.Is(err, store.ErrDuplicate):
		return false, nil
	case err != nil:
		return false, err
	}
	m.log().Info("queued", "queue", item.Queue, "locator", item.Locator,
		"code", item.Code, "reason", item.Reason)
	// Work created, by kind. Backlog per queue is the operational number;
	// which queues fill is the commercial one, since a rising unable or
	// divergence count is a partner problem rather than a capacity problem.
	metrics.Counter("jetway_queue_placed_total", "records placed on a work queue",
		metrics.Labels{"queue": item.Queue})
	if span := trace.SpanFromContext(ctx); span.IsRecording() {
		span.AddEvent("queued", trace.WithAttributes(
			telemetry.AttrQueue.String(item.Queue),
			telemetry.AttrQueueCode.String(item.Code),
			telemetry.AttrLocator.String(item.Locator),
		))
	}
	if m.Notify != nil {
		m.Notify(item)
	}
	if m.Publish != nil {
		if err := m.Publish.Publish(ctx, item); err != nil {
			// The work is already durable; an unreachable broker must not undo
			// it. Whoever polls the queue will still find the item.
			m.log().Error("queue placement stored but not published",
				"queue", item.Queue, "item", item.ID, "err", err)
		}
	}
	return true, nil
}

// Work marks an item done.
func (m *Manager) Work(ctx context.Context, id, by, note string) error {
	if m.Store == nil {
		return errors.New("queue: no store configured")
	}
	return m.Store.WorkQueueItem(ctx, id, by, note)
}

// PlaceForSegment is the common case: a segment on a record needs attention.
func (m *Manager) PlaceForSegment(ctx context.Context, rec *pnr.PNR, seg *pnr.Segment,
	queueName, code, reason, messageID string) (bool, error) {
	item := &store.QueueItem{
		Queue: queueName, PNRID: rec.ID, Locator: rec.RecordLocator,
		Code: code, Reason: reason, MessageID: messageID,
	}
	if seg != nil {
		item.SegmentRef = seg.Ref
	}
	return m.Place(ctx, item)
}

// ForStatus returns the queue a segment status belongs on and a stable reason
// code, or ok false when the status needs no attention.
//
// This is where a partner's answer becomes work. A confirmation has to reach
// whoever holds the other end of the itinerary; a refusal has to be rebooked by
// someone; a waitlist has to be watched. Requests we ourselves originated are
// silent, because waiting for an answer is not yet a problem -- that is the
// Sweeper's job once enough time has passed.
//
// Statuses are read after a message has been applied, so a partner's reply code
// has usually already been folded into the holding code it implies: KK becomes
// HK, US becomes HL. Refusals keep the reply code, since nothing is held.
func ForStatus(status string) (queueName, code, reason string, ok bool) {
	if status == "" {
		return "", "", "", false
	}
	c := rescode.ActionCode(status)
	info, known := c.Info()
	if !known {
		// A private bilateral code is not something this node can interpret, so
		// it goes in front of a human rather than being assumed benign.
		return store.QueueDivergence, "unknown_status_" + status,
			"partner used status " + status + ", which is not in the interline vocabulary", true
	}
	switch {
	case info.Category == rescode.CatRequest:
		// Our own outstanding request. Not work until it goes unanswered.
		return "", "", "", false
	case info.Category == rescode.CatCancel:
		return store.QueueUnable, "cancelled_" + status,
			"partner cancelled the segment (" + info.Meaning + ")", true
	case info.Waitlisted:
		return store.QueueWaitlist, "waitlisted_" + status,
			"segment waitlisted (" + info.Meaning + ")", true
	case info.Confirmed:
		return store.QueueConfirmation, "confirmed_" + status,
			"partner confirmed (" + info.Meaning + ")", true
	case info.Category == rescode.CatReply:
		return store.QueueUnable, "unable_" + status,
			"partner could not confirm (" + info.Meaning + ")", true
	}
	return "", "", "", false
}
