package store

import (
	"context"
	"time"
)

// A queue is a list of records waiting for someone to do something about them.
//
// It is the mechanism a reservations system uses to turn an asynchronous
// partner conversation into work: a carrier confirms a segment overnight, a
// ticketing deadline passes, a request goes unanswered, and each of those has
// to surface somewhere a person or a robot will look. Without queues those
// events are visible only to whoever happens to re-read the record.
//
// Queue names here are Jetway's own vocabulary, not an IATA standard. Real
// systems number their queues and the numbering is house-specific, so a
// deployment that has to match an existing convention should map these names
// onto its own numbers at the edge rather than expect the numbers to be
// meaningful across systems.
const (
	// QueueConfirmation holds records a partner has confirmed, where the
	// holding side has to be told or the itinerary reissued.
	QueueConfirmation = "confirmation"
	// QueueUnable holds records a partner refused. Someone has to rebook.
	QueueUnable = "unable"
	// QueueWaitlist holds records with waitlisted segments awaiting clearance.
	QueueWaitlist = "waitlist"
	// QueuePending holds requests a partner has not answered inside the agreed
	// time. Nothing is wrong with the record; the conversation has stalled.
	QueuePending = "pending"
	// QueueTicketing holds records whose ticketing time limit is near or past.
	QueueTicketing = "ticketing"
	// QueueScheduleChange holds records touched by a schedule message: the
	// flight they are booked on has moved, changed equipment or been
	// cancelled, and the passenger has not been told.
	QueueScheduleChange = "schedule-change"
	// QueueDivergence holds records where our state and a partner's disagree,
	// which is the case a human must always see.
	QueueDivergence = "divergence"
	// QueueGeneral is the fallback for placements with no better home.
	QueueGeneral = "general"
)

// Queues lists the known queue names in the order a console should show them.
var Queues = []string{
	QueueConfirmation,
	QueueUnable,
	QueueWaitlist,
	QueuePending,
	QueueTicketing,
	QueueScheduleChange,
	QueueDivergence,
	QueueGeneral,
}

// QueueItem is one record placed on one queue for one reason.
//
// An item is evidence as much as it is a task: it says what put the record
// here, which message caused it, and when. Working an item does not delete it,
// because "who cleared this and when" is exactly the question asked after an
// interline dispute.
type QueueItem struct {
	ID    string `json:"id"`
	Queue string `json:"queue"`

	PNRID   string `json:"pnr_id"`
	Locator string `json:"locator,omitempty"`

	// Code is the stable machine-readable reason, e.g. "tktl_expired". Together
	// with Queue, PNRID and SegmentRef it is the idempotency key: the same
	// segment is on a queue once per reason until the item is worked, so a
	// sweeper may run as often as it likes.
	Code string `json:"code"`
	// Reason is the human-readable form of the same thing.
	Reason string `json:"reason"`

	// MessageID is the message that caused the placement, where there was one.
	MessageID string `json:"message_id,omitempty"`
	// SegmentRef narrows the placement to one segment, where it applies.
	SegmentRef int `json:"segment_ref,omitempty"`

	PlacedAt time.Time `json:"placed_at"`
	PlacedBy string    `json:"placed_by"`

	// WorkedAt is nil while the item is pending.
	WorkedAt *time.Time `json:"worked_at,omitempty"`
	WorkedBy string     `json:"worked_by,omitempty"`
	// Note is what the worker said when clearing it.
	Note string `json:"note,omitempty"`
}

// Pending reports whether the item is still outstanding.
func (q *QueueItem) Pending() bool { return q.WorkedAt == nil }

// QueueFilter narrows a queue listing.
type QueueFilter struct {
	// Queue restricts to one queue name. Empty means every queue.
	Queue string
	// PNRID restricts to one record.
	PNRID string
	// IncludeWorked returns cleared items as well as pending ones. The default
	// is the working view: what still needs doing.
	IncludeWorked bool
	Limit         int
}

// QueueStore is the queue half of the persistence contract. It is separate from
// Store only for readability; every backend implements both.
type QueueStore interface {
	// Enqueue places a record on a queue. It returns ErrDuplicate when the same
	// segment of the same record is already pending on the same queue for the
	// same code, which is what makes a repeatedly-running sweeper harmless.
	Enqueue(ctx context.Context, item *QueueItem) error
	// WorkQueueItem marks an item done. Working an already-worked item returns
	// ErrConflict rather than silently overwriting who cleared it.
	WorkQueueItem(ctx context.Context, id, by, note string) error
	// ListQueue returns items newest first.
	ListQueue(ctx context.Context, f QueueFilter) ([]*QueueItem, error)
	// QueueCounts returns the number of pending items per queue name.
	QueueCounts(ctx context.Context) (map[string]int, error)
}
