package gateway

import (
	"sync"
	"time"
)

// EventType names a bus event.
type EventType string

const (
	// EvMessage carries a message that entered or left the gateway.
	EvMessage EventType = "message"
	// EvPNR carries a record whose state changed.
	EvPNR EventType = "pnr"
	// EvLink reports a peer connecting or disconnecting.
	EvLink EventType = "link"
	// EvAvail reports that availability changed.
	EvAvail EventType = "avail"
	// EvQueue reports a record placed on a work queue.
	EvQueue EventType = "queue"
	// EvMovement reports an aircraft movement message: a departure, an
	// arrival, a delay, a diversion. It is what an operations display draws
	// planes from.
	EvMovement EventType = "movement"
	// EvTrace narrates a pipeline step, which is what makes the console show
	// the path a booking took rather than only its result.
	EvTrace EventType = "trace"
)

// Event is one thing worth telling an observer about.
type Event struct {
	Seq  int64     `json:"seq"`
	Type EventType `json:"type"`
	At   time.Time `json:"at"`
	Data any       `json:"data"`
}

// Bus fans events out to live observers and keeps a short backlog so a console
// opened mid-flight can render recent history instead of an empty screen.
//
// Subscribers get a buffered channel and are dropped from a send if they fall
// behind. A slow browser tab must never be able to stall message processing.
type Bus struct {
	// Now, when set, stamps events instead of the wall clock, so a driven
	// simulation's bus timeline matches its stores. Set it before Publish is
	// first called; it is read without a lock.
	Now func() time.Time

	mu      sync.RWMutex
	subs    map[int]chan Event
	nextSub int
	seq     int64

	history    []Event
	historyCap int
}

func (b *Bus) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now().UTC()
}

// NewBus returns a bus retaining the last n events.
func NewBus(n int) *Bus {
	if n <= 0 {
		n = 500
	}
	return &Bus{subs: map[int]chan Event{}, historyCap: n}
}

// Publish records an event and delivers it to current subscribers.
func (b *Bus) Publish(t EventType, data any) {
	b.mu.Lock()
	b.seq++
	ev := Event{Seq: b.seq, Type: t, At: b.now(), Data: data}
	b.history = append(b.history, ev)
	if len(b.history) > b.historyCap {
		b.history = b.history[len(b.history)-b.historyCap:]
	}
	b.mu.Unlock()

	// The sends happen under the read lock, and that is a correctness rule,
	// not a style choice: Unsubscribe closes the channel under the write
	// lock, and a send racing that close panics the process. Snapshotting
	// the subscriber list and sending unlocked -- the previous shape -- left
	// exactly that window, and a world simulator tearing down five hundred
	// subscribers found it. The sends are non-blocking, so holding the read
	// lock costs microseconds.
	b.mu.RLock()
	for _, c := range b.subs {
		select {
		case c <- ev:
		default:
			// Subscriber is behind. Dropping its copy is correct: the gateway's
			// job is moving airline traffic, not guaranteeing delivery to a UI.
		}
	}
	b.mu.RUnlock()
}

// Subscribe returns a channel of events and a function to release it.
func (b *Bus) Subscribe() (<-chan Event, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.nextSub
	b.nextSub++
	ch := make(chan Event, 256)
	b.subs[id] = ch
	return ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if c, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(c)
		}
	}
}

// History returns the retained backlog.
func (b *Bus) History() []Event {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return append([]Event(nil), b.history...)
}
