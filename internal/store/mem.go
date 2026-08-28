package store

import (
	"context"
	"sort"
	"sync"

	"github.com/adamf/jetway/internal/ulid"
	"github.com/adamf/jetway/pkg/pnr"
)

// Mem is an in-memory Store.
//
// It exists so the gateway runs with no external dependency -- for the demo,
// for tests, and for a carrier evaluating the message flow before provisioning
// a database. It is not a production backend: nothing survives a restart, and
// memory grows with traffic.
type Mem struct {
	mu sync.RWMutex

	messages   map[string]*Message
	messageIDs []string // ULIDs, kept sorted, which is receive order

	pnrs      map[string]*pnr.PNR // by id
	byLocator map[string]string   // locator -> id
	events    map[string][]Event

	dedup   map[string]string // peer|key -> message id
	counter uint64
}

// NewMem returns an empty in-memory store.
func NewMem() *Mem {
	return &Mem{
		messages:  map[string]*Message{},
		pnrs:      map[string]*pnr.PNR{},
		byLocator: map[string]string{},
		events:    map[string][]Event{},
		dedup:     map[string]string{},
	}
}

func cloneMessage(m *Message) *Message {
	c := *m
	c.Raw = append([]byte(nil), m.Raw...)
	c.Diagnostics = append([]Diagnostic(nil), m.Diagnostics...)
	return &c
}

func clonePNR(p *pnr.PNR) *pnr.PNR {
	c := *p
	c.Passengers = append([]pnr.Passenger(nil), p.Passengers...)
	c.Segments = append([]pnr.Segment(nil), p.Segments...)
	c.SSRs = append([]pnr.SSR(nil), p.SSRs...)
	c.OSIs = append([]pnr.OSI(nil), p.OSIs...)
	c.Contacts = append([]pnr.Contact(nil), p.Contacts...)
	c.Ticketing = append([]pnr.Ticketing(nil), p.Ticketing...)
	c.Remarks = append([]pnr.Remark(nil), p.Remarks...)
	c.Locators = append([]pnr.ExternalLocator(nil), p.Locators...)
	c.Unparsed = append([]pnr.Fragment(nil), p.Unparsed...)
	return &c
}

func (s *Mem) AppendMessage(ctx context.Context, m *Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m.ID == "" {
		m.ID = ulid.New()
	}
	s.messages[m.ID] = cloneMessage(m)
	// Ids are time-ordered, so appending keeps the slice sorted in the common
	// case; the sort covers ingest racing across goroutines.
	s.messageIDs = append(s.messageIDs, m.ID)
	if n := len(s.messageIDs); n > 1 && s.messageIDs[n-1] < s.messageIDs[n-2] {
		sort.Strings(s.messageIDs)
	}
	if m.DedupKey != "" {
		k := m.Peer + "|" + m.DedupKey
		if _, exists := s.dedup[k]; !exists {
			s.dedup[k] = m.ID
		}
	}
	return nil
}

func (s *Mem) UpdateMessage(ctx context.Context, m *Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.messages[m.ID]; !ok {
		return ErrNotFound
	}
	s.messages[m.ID] = cloneMessage(m)
	// Capture happens before decode, so a message's application-level dedup key
	// is only known by the time of this update. Index it here or a
	// retransmission will never be recognised.
	if m.DedupKey != "" {
		k := m.Peer + "|" + m.DedupKey
		if _, exists := s.dedup[k]; !exists {
			s.dedup[k] = m.ID
		}
	}
	return nil
}

func (s *Mem) GetMessage(ctx context.Context, id string) (*Message, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.messages[id]
	if !ok {
		return nil, ErrNotFound
	}
	return cloneMessage(m), nil
}

func (s *Mem) ListMessages(ctx context.Context, f MessageFilter) ([]*Message, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	var out []*Message
	// Walk newest first so a limit keeps the most recent traffic, then reverse
	// so callers receive chronological order.
	for i := len(s.messageIDs) - 1; i >= 0 && len(out) < limit; i-- {
		id := s.messageIDs[i]
		if f.SinceID != "" && id <= f.SinceID {
			break
		}
		m := s.messages[id]
		if f.Peer != "" && m.Peer != f.Peer {
			continue
		}
		if f.PNRID != "" && m.PNRID != f.PNRID {
			continue
		}
		if f.Status != "" && m.Status != f.Status {
			continue
		}
		out = append(out, cloneMessage(m))
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

func (s *Mem) FindByDedupKey(ctx context.Context, peer, key string) (string, bool, error) {
	if key == "" {
		return "", false, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.dedup[peer+"|"+key]
	return id, ok, nil
}

func (s *Mem) CreatePNR(ctx context.Context, p *pnr.PNR, events []Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.ID == "" {
		p.ID = ulid.New()
	}
	if _, taken := s.byLocator[p.RecordLocator]; taken {
		return ErrDuplicate
	}
	p.Version = 1
	s.pnrs[p.ID] = clonePNR(p)
	s.byLocator[p.RecordLocator] = p.ID
	s.appendEventsLocked(p.ID, events)
	return nil
}

func (s *Mem) UpdatePNR(ctx context.Context, p *pnr.PNR, expected int64, events []Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.pnrs[p.ID]
	if !ok {
		return ErrNotFound
	}
	if cur.Version != expected {
		return ErrConflict
	}
	p.Version = expected + 1
	s.pnrs[p.ID] = clonePNR(p)
	s.byLocator[p.RecordLocator] = p.ID
	s.appendEventsLocked(p.ID, events)
	return nil
}

func (s *Mem) appendEventsLocked(pnrID string, events []Event) {
	seq := int64(len(s.events[pnrID]))
	for i := range events {
		seq++
		e := events[i]
		if e.ID == "" {
			e.ID = ulid.New()
		}
		e.PNRID = pnrID
		e.Seq = seq
		s.events[pnrID] = append(s.events[pnrID], e)
	}
}

func (s *Mem) GetPNR(ctx context.Context, locator string) (*pnr.PNR, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.byLocator[locator]
	if !ok {
		return nil, ErrNotFound
	}
	return clonePNR(s.pnrs[id]), nil
}

func (s *Mem) GetPNRByID(ctx context.Context, id string) (*pnr.PNR, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.pnrs[id]
	if !ok {
		return nil, ErrNotFound
	}
	return clonePNR(p), nil
}

func (s *Mem) ListPNRs(ctx context.Context, limit int) ([]*pnr.PNR, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 {
		limit = 100
	}
	out := make([]*pnr.PNR, 0, len(s.pnrs))
	for _, p := range s.pnrs {
		out = append(out, clonePNR(p))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *Mem) Events(ctx context.Context, pnrID string) ([]Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Event(nil), s.events[pnrID]...), nil
}

func (s *Mem) NextLocatorCounter(ctx context.Context) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counter++
	return s.counter, nil
}

func (s *Mem) Close() error { return nil }
