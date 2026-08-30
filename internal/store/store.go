// Package store persists the message log and the PNR state.
//
// The two are deliberately separate concerns with different guarantees.
//
// The message log is append-only and holds the exact bytes received or sent. It
// is written before anything is interpreted, so a parser bug costs a
// reprocessing run rather than a lost booking, and so "what did we actually
// receive at 14:32" has an answer that does not depend on the parser that was
// deployed at the time.
//
// PNR state is derived. Every change is recorded as an event carrying the id of
// the message that caused it, and the current record is a projection of those
// events. That is what makes an interline dispute answerable: the record shows
// what it holds now, and the events show which partner message put it there.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/adamf/jetway/pkg/pnr"
)

// Errors returned by every implementation.
var (
	// ErrNotFound is returned when a record or message does not exist.
	ErrNotFound = errors.New("store: not found")
	// ErrConflict is returned when an update's expected version does not match
	// the stored version. The caller must re-read and retry: a blind overwrite
	// loses whichever concurrent change it did not see.
	ErrConflict = errors.New("store: version conflict")
	// ErrDuplicate is returned when a record locator is already taken.
	ErrDuplicate = errors.New("store: duplicate")
)

// Direction distinguishes traffic we received from traffic we sent.
type Direction string

const (
	Inbound  Direction = "in"
	Outbound Direction = "out"
)

// Status tracks a message through the pipeline.
type Status string

const (
	// StatusReceived means the bytes are durable but nothing has read them.
	StatusReceived Status = "received"
	// StatusDecoded means the envelope and body parsed.
	StatusDecoded Status = "decoded"
	// StatusApplied means the message changed a record, or was correctly
	// determined to require no change.
	StatusApplied Status = "applied"
	// StatusRejected means the message was understood and refused, for example
	// as a duplicate or as test traffic on a production link.
	StatusRejected Status = "rejected"
	// StatusDLQ means the pipeline could not process it and a human must look.
	// Messages never leave the system on this path; they wait to be replayed.
	StatusDLQ Status = "dlq"
	// StatusSent applies to outbound traffic handed to a transport.
	StatusSent Status = "sent"
	// StatusUndeliverable applies to outbound traffic no transport accepted.
	StatusUndeliverable Status = "undeliverable"
	// StatusAcknowledged means a partner confirmed receipt of outbound traffic,
	// which on an EDIFACT link is a CONTRL saying so. Delivery and
	// acknowledgement are different facts: a transport that took the bytes
	// proves nothing about whether the partner could read them.
	StatusAcknowledged Status = "acknowledged"
	// StatusRefused means a partner received outbound traffic and rejected it.
	StatusRefused Status = "refused"
)

// Format names the wire encoding.
type Format string

const (
	FormatTypeB   Format = "typeb"
	FormatEDIFACT Format = "edifact"
	FormatUnknown Format = "unknown"
)

// Diagnostic is a decoder observation, flattened across codec packages so the
// console and the API present one shape.
type Diagnostic struct {
	Layer    string `json:"layer"` // "typeb", "edifact", "airimp", "padis"
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Detail   string `json:"detail"`
	Line     int    `json:"line,omitempty"`
}

// Message is one unit of traffic, inbound or outbound.
type Message struct {
	ID        string    `json:"id"`
	Direction Direction `json:"direction"`
	At        time.Time `json:"at"`

	// Transport and Peer say how it arrived and from whom. Peer is the link
	// name, which is what routing and per-partner policy key off.
	Transport string `json:"transport"`
	Peer      string `json:"peer"`

	Format Format `json:"format"`
	// Kind is the decoded message type, e.g. "AIRIMP/sell" or "PAORES".
	Kind string `json:"kind,omitempty"`

	// Raw is the exact bytes on the wire. Never regenerate it from a parse.
	Raw []byte `json:"-"`
	// SHA256 is the hex digest of Raw, used for content-based deduplication
	// where an application-level reference is unavailable.
	SHA256 string `json:"sha256"`
	Size   int    `json:"size"`

	Status Status `json:"status"`
	Error  string `json:"error,omitempty"`

	// DedupKey is the application-level idempotency key: an EDIFACT interchange
	// control reference, or a Type B origin and time group. Empty when the
	// message class carries none.
	DedupKey string `json:"dedup_key,omitempty"`

	// TraceID and SpanID tie this message to the trace that handled it. They
	// turn "what happened to this message" from a search into a link, and they
	// survive in the log after the trace itself has been sampled away.
	TraceID string `json:"trace_id,omitempty"`
	SpanID  string `json:"span_id,omitempty"`

	// PossibleDuplicate records the Type B PDM indicator. Inbound it means the
	// sender said this may be a retransmission; outbound it means we marked it
	// as one on redelivery. A duplicate that arrives flagged is the protocol
	// working; one that arrives unflagged is worth an operator's attention.
	PossibleDuplicate bool `json:"possible_duplicate,omitempty"`

	// PNRID links the message to the record it touched.
	PNRID string `json:"pnr_id,omitempty"`
	// CorrelationID ties a response back to the request that provoked it, and
	// is what turns a message list into a conversation.
	CorrelationID string `json:"correlation_id,omitempty"`

	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

// RawString returns the message body as text, for display.
func (m *Message) RawString() string { return string(m.Raw) }

// Event is one applied change to a record.
type Event struct {
	ID        string          `json:"id"`
	PNRID     string          `json:"pnr_id"`
	Seq       int64           `json:"seq"`
	Type      string          `json:"type"`
	Detail    string          `json:"detail"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	MessageID string          `json:"message_id,omitempty"`
	Actor     string          `json:"actor,omitempty"`
	At        time.Time       `json:"at"`
}

// MessageFilter narrows a message listing.
type MessageFilter struct {
	Peer   string
	PNRID  string
	Status Status
	Limit  int
	// SinceID returns only messages with an id greater than this one, which is
	// how the console tails the log without re-fetching.
	SinceID string
}

// Store is the persistence contract.
//
// Implementations must be safe for concurrent use: a gateway processes many
// links at once, and two of them can touch the same record.
type Store interface {
	// AppendMessage writes a message. The raw bytes must be durable before it
	// returns, because the caller acknowledges the peer on success.
	AppendMessage(ctx context.Context, m *Message) error
	// UpdateMessage records a status transition and any diagnostics.
	UpdateMessage(ctx context.Context, m *Message) error
	GetMessage(ctx context.Context, id string) (*Message, error)
	ListMessages(ctx context.Context, f MessageFilter) ([]*Message, error)

	// FindByDedupKey returns the id of an earlier inbound message from the same
	// peer with the same application-level key, which is how a retransmission
	// is recognised without re-applying it.
	FindByDedupKey(ctx context.Context, peer, key string) (string, bool, error)

	// FindOutboundByKey returns the id of a message sent to a peer carrying the
	// given application-level key. It is the other direction of the same
	// question, and is how an acknowledgement is matched to what it
	// acknowledges.
	FindOutboundByKey(ctx context.Context, peer, key string) (string, bool, error)

	// CreatePNR stores a new record at version 1 along with the events that
	// created it.
	CreatePNR(ctx context.Context, p *pnr.PNR, events []Event) error
	// UpdatePNR stores a record, failing with ErrConflict unless the stored
	// version still equals expectedVersion.
	UpdatePNR(ctx context.Context, p *pnr.PNR, expectedVersion int64, events []Event) error
	GetPNR(ctx context.Context, locator string) (*pnr.PNR, error)
	GetPNRByID(ctx context.Context, id string) (*pnr.PNR, error)
	ListPNRs(ctx context.Context, limit int) ([]*pnr.PNR, error)
	Events(ctx context.Context, pnrID string) ([]Event, error)

	// NextLocatorCounter returns a value that has never been returned before.
	// It is the uniqueness source behind record locator allocation.
	NextLocatorCounter(ctx context.Context) (uint64, error)

	// QueueStore holds the work queues records are placed on.
	QueueStore

	Close() error
}
