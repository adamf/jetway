package transport

import (
	"errors"

	"github.com/adamf/jetway/pkg/metrics"
	"sync"
	"sync/atomic"
	"time"
)

// ErrCongested is what Send returns when a link's outbound queue has been
// full for the send timeout: the peer is not reading. The message is not
// on the wire; the caller decides whether that is undeliverable or a retry.
var ErrCongested = errors.New("transport: link congested: the peer is not reading")

// ErrLinkClosed is what Send returns once the link's writer has stopped.
var ErrLinkClosed = errors.New("transport: link is closed")

// OutboxDepth is how many frames a link queues before Send waits, and
// SendTimeout how long it waits before giving up with ErrCongested. Both
// are variables so a deployment with fatter bursts or thinner memory can
// move them; the defaults hold a departure bank's name lists for one link.
var (
	OutboxDepth = 512
	SendTimeout = 5 * time.Second
)

// Outbox is the outbound half of a link: a bounded queue drained by one
// writer goroutine, with the write deadline in the writer.
//
// Why it exists. Both ends of a link run their handler inside the read
// loop, and a handler often answers what it read -- a ticket-control reply,
// a relay back to the same link. With writes done inline under a mutex,
// a reader that answers waits for the socket, the socket waits for the
// peer to read, and the peer's reader is waiting for its own answer to
// leave. Two full TCP windows and nobody reading is a deadlock that only
// the write deadline breaks, thirty seconds and one torn-down link later.
// Filling a recorded day to a holiday load found it in the first departure
// bank. With the queue, a reader hands its answer over and reads on; only
// when the queue itself has been full for the timeout does a sender learn
// that the peer has stopped, and it learns it as an error rather than a
// stall.
type Outbox struct {
	// Peer labels the link's metrics: queue depth and congestion per peer.
	Peer string

	write func(raw []byte) error
	q     chan []byte
	stop  chan struct{}
	once  sync.Once
	mu    sync.Mutex
	err   error
	// congested is set when a Send has waited its whole timeout, and
	// cleared by the writer once the queue is half drained. While it is
	// set, Send fails at once: a peer that has stopped reading must not
	// cost every message five seconds, or the reader that answers into
	// this link is as stalled as it was before the queue existed.
	congested atomic.Bool
}

// NewOutbox starts the writer. write puts one frame on the wire, deadline
// included; when it fails, the outbox closes and onFail runs once, which
// is where the owner closes the connection so its reader ends too.
func NewOutbox(depth int, write func(raw []byte) error, onFail func(err error)) *Outbox {
	if depth <= 0 {
		depth = OutboxDepth
	}
	o := &Outbox{write: write, q: make(chan []byte, depth), stop: make(chan struct{})}
	go func() {
		for {
			select {
			case <-o.stop:
				return
			case raw := <-o.q:
				if err := o.write(raw); err != nil {
					o.fail(err)
					if onFail != nil {
						onFail(err)
					}
					return
				}
				if o.congested.Load() && len(o.q) <= cap(o.q)/2 {
					o.congested.Store(false)
				}
				metrics.Gauge("jetway_outbox_depth", "frames waiting to be written on a link",
					metrics.Labels{"peer": o.Peer}, float64(len(o.q)))
			}
		}
	}()
	return o
}

func (o *Outbox) fail(err error) {
	o.once.Do(func() {
		o.mu.Lock()
		o.err = err
		o.mu.Unlock()
		close(o.stop)
	})
}

// Send queues one frame, waiting up to SendTimeout for room.
func (o *Outbox) Send(raw []byte) error {
	select {
	case <-o.stop:
		return o.closedErr()
	default:
	}
	if o.congested.Load() {
		select {
		case o.q <- raw:
			return nil
		default:
			return ErrCongested
		}
	}
	timer := time.NewTimer(SendTimeout)
	defer timer.Stop()
	select {
	case o.q <- raw:
		return nil
	case <-o.stop:
		return o.closedErr()
	case <-timer.C:
		o.congested.Store(true)
		metrics.Counter("jetway_outbox_congested_total", "sends refused because the peer stopped reading",
			metrics.Labels{"peer": o.Peer})
		return ErrCongested
	}
}

func (o *Outbox) closedErr() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.err != nil {
		return o.err
	}
	return ErrLinkClosed
}

// Close stops the writer. Frames still queued are not written: the link is
// going away and the store's ledger says what was never sent.
func (o *Outbox) Close() { o.fail(ErrLinkClosed) }

// Congested reports whether the peer has stopped taking frames: a Send
// waited its whole timeout and the queue has not half drained since.
func (o *Outbox) Congested() bool { return o.congested.Load() }

// Depth reports how many frames are waiting, for instruments.
func (o *Outbox) Depth() int { return len(o.q) }
