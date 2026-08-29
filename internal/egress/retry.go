package egress

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/adamf/jetway/internal/config"
	"github.com/adamf/jetway/internal/metrics"
	"github.com/adamf/jetway/internal/store"
	"github.com/adamf/jetway/pkg/typeb"
)

// Router picks the sender for a peer and retries what does not go through.
//
// Retrying is durable in the sense that matters: every outbound message is
// already in the message log before a send is attempted, so a restart recovers
// the backlog by scanning for undeliverable messages rather than by trusting an
// in-memory queue to have survived.
type Router struct {
	store store.Store
	log   *slog.Logger

	mu      sync.RWMutex
	senders map[string]Sender
	policy  map[string]config.Retry
	formats map[string]store.Format

	// queue holds attempts waiting for their backoff to elapse.
	qmu   sync.Mutex
	queue []*attempt
	wake  chan struct{}
}

type attempt struct {
	messageID string
	peer      string
	format    store.Format
	raw       []byte
	tries     int
	next      time.Time
}

// NewRouter builds an empty router.
func NewRouter(st store.Store, log *slog.Logger) *Router {
	return &Router{
		store: st, log: log,
		senders: map[string]Sender{},
		policy:  map[string]config.Retry{},
		formats: map[string]store.Format{},
		wake:    make(chan struct{}, 1),
	}
}

// Register adds a peer's sender, retry policy and wire format.
//
// The format is here because a redelivery has to be marked according to the
// format's own duplicate convention, and only the format says which one
// applies: Type B carries PDM in the envelope, while EDIFACT relies on the
// interchange control reference the receiver already holds.
func (r *Router) Register(peer string, s Sender, policy config.Retry, format store.Format) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.senders[peer] = s
	r.policy[peer] = policy
	r.formats[peer] = format
}

// format returns the registered wire format for a peer.
func (r *Router) format(peer string) store.Format {
	r.mu.RLock()
	defer r.mu.RUnlock()
	f, ok := r.formats[peer]
	if !ok {
		return store.FormatUnknown
	}
	return f
}

// redeliveryBytes returns what should go on the wire for a retry.
//
// Every attempt after the first is a retransmission, and the receiver is
// entitled to be told so rather than left to infer it from content. Marking is
// a textual edit to the envelope; the captured bytes in the log are not
// touched, so the audit trail still shows what was originally built.
func redeliveryBytes(a *attempt) ([]byte, bool) {
	if a.format != store.FormatTypeB {
		return a.raw, false
	}
	return typeb.MarkPossibleDuplicate(a.raw)
}

// Sender returns the sender for a peer.
func (r *Router) Sender(peer string) (Sender, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.senders[peer]
	return s, ok
}

// Peers lists peers with a configured egress.
func (r *Router) Peers() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.senders))
	for p := range r.senders {
		out = append(out, p)
	}
	return out
}

// ErrNoRoute means nothing is configured to reach a peer.
var ErrNoRoute = errors.New("egress: no route to peer")

// Send attempts delivery once. On failure the message is queued for retry and
// the error is returned, so the caller can record it, but the message is not
// abandoned.
func (r *Router) Send(ctx context.Context, peer string, raw []byte) error {
	return r.SendMessage(ctx, "", peer, raw)
}

// SendMessage delivers raw bytes, tracking them against a stored message id so
// that retries can update its status.
func (r *Router) SendMessage(ctx context.Context, messageID, peer string, raw []byte) error {
	s, ok := r.Sender(peer)
	if !ok {
		return fmt.Errorf("%w: %s", ErrNoRoute, peer)
	}
	err := s.Send(ctx, raw)
	recordAttempt(peer, err)
	if err == nil {
		return nil
	}
	r.enqueue(&attempt{
		messageID: messageID, peer: peer, format: r.format(peer), raw: raw, tries: 1,
		next: time.Now().Add(r.backoff(peer, 1)),
	})
	return err
}

func (r *Router) backoff(peer string, tries int) time.Duration {
	r.mu.RLock()
	p := r.policy[peer]
	r.mu.RUnlock()
	if p.Initial <= 0 {
		p.Initial = 2 * time.Second
	}
	if p.Max <= 0 {
		p.Max = 5 * time.Minute
	}
	d := p.Initial
	for i := 1; i < tries; i++ {
		d *= 2
		if d >= p.Max {
			return p.Max
		}
	}
	return d
}

func (r *Router) maxAttempts(peer string) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if n := r.policy[peer].MaxAttempts; n > 0 {
		return n
	}
	return 8
}

func (r *Router) enqueue(a *attempt) {
	r.qmu.Lock()
	r.queue = append(r.queue, a)
	depth := len(r.queue)
	r.qmu.Unlock()
	metrics.Gauge("jetway_egress_retry_queue", "messages awaiting redelivery", nil, float64(depth))
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// QueueDepth reports how many messages are waiting for redelivery. A depth that
// stops falling means a partner is unreachable, and is the number to alert on.
func (r *Router) QueueDepth() int {
	r.qmu.Lock()
	defer r.qmu.Unlock()
	return len(r.queue)
}

// Run drives redelivery until ctx is cancelled.
func (r *Router) Run(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-r.wake:
		}
		r.drain(ctx)
	}
}

func (r *Router) drain(ctx context.Context) {
	now := time.Now()
	r.qmu.Lock()
	var due, keep []*attempt
	for _, a := range r.queue {
		if a.next.After(now) {
			keep = append(keep, a)
			continue
		}
		due = append(due, a)
	}
	r.queue = keep
	r.qmu.Unlock()

	for _, a := range due {
		if ctx.Err() != nil {
			return
		}
		s, ok := r.Sender(a.peer)
		if !ok {
			r.fail(ctx, a, ErrNoRoute)
			continue
		}
		raw, marked := redeliveryBytes(a)
		err := s.Send(ctx, raw)
		recordAttempt(a.peer, err)
		if err == nil {
			r.log.Info("redelivered", "peer", a.peer, "message", a.messageID,
				"attempt", a.tries+1, "marked_pdm", marked)
			r.mark(ctx, a.messageID, store.StatusSent, "", marked)
			continue
		}
		a.tries++
		if a.tries >= r.maxAttempts(a.peer) {
			r.fail(ctx, a, err)
			continue
		}
		a.next = time.Now().Add(r.backoff(a.peer, a.tries))
		r.log.Warn("redelivery failed, will retry",
			"peer", a.peer, "message", a.messageID, "attempt", a.tries,
			"next_in", time.Until(a.next).Round(time.Second), "err", err)
		r.enqueue(a)
	}
	metrics.Gauge("jetway_egress_retry_queue", "messages awaiting redelivery", nil, float64(r.QueueDepth()))
}

// fail gives up on a message. It stays in the log as undeliverable so an
// operator can see it and replay it deliberately; it is never silently dropped.
func (r *Router) fail(ctx context.Context, a *attempt, err error) {
	r.log.Error("giving up on delivery after the configured attempts",
		"peer", a.peer, "message", a.messageID, "attempts", a.tries, "err", err)
	metrics.Counter("jetway_egress_abandoned_total", "messages no longer being retried",
		metrics.Labels{"peer": a.peer})
	r.mark(ctx, a.messageID, store.StatusUndeliverable, err.Error(), false)
}

func (r *Router) mark(ctx context.Context, messageID string, st store.Status, detail string, pdm bool) {
	if messageID == "" || r.store == nil {
		return
	}
	m, err := r.store.GetMessage(ctx, messageID)
	if err != nil {
		return
	}
	m.Status = st
	m.Error = detail
	if pdm {
		m.PossibleDuplicate = true
	}
	if err := r.store.UpdateMessage(ctx, m); err != nil {
		r.log.Error("could not record delivery outcome", "message", messageID, "err", err)
	}
}

// Recover re-queues messages left undeliverable by a previous run.
//
// Without this a restart quietly abandons everything that was mid-retry, which
// is exactly the traffic most likely to matter: the confirmations a partner is
// still waiting on.
func (r *Router) Recover(ctx context.Context) (int, error) {
	if r.store == nil {
		return 0, nil
	}
	msgs, err := r.store.ListMessages(ctx, store.MessageFilter{
		Status: store.StatusUndeliverable, Limit: 1000,
	})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, m := range msgs {
		if m.Direction != store.Outbound {
			continue
		}
		if _, ok := r.Sender(m.Peer); !ok {
			continue
		}
		r.enqueue(&attempt{
			messageID: m.ID, peer: m.Peer, format: m.Format, raw: m.Raw,
			tries: 0, next: time.Now(),
		})
		n++
	}
	if n > 0 {
		r.log.Info("recovered undelivered messages from the log", "count", n)
	}
	return n, nil
}
