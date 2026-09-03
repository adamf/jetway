package store

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/adamf/jetway/pkg/pnr"
	"github.com/adamf/jetway/pkg/ulid"
)

// Mem is an in-memory Store.
//
// It exists so the gateway runs with no external dependency -- for the demo,
// for tests, and for a carrier evaluating the message flow before provisioning
// a database. It is not a production backend: nothing survives a restart.
type Mem struct {
	leases map[string]memLease

	// MaxMessages and MaxRecords bound what is retained, oldest discarded
	// first. Zero means unbounded, which is right for a test and wrong for
	// anything reachable from the internet: without a bound, a public demo is
	// a memory leak with a submit button.
	MaxMessages int
	MaxRecords  int

	// Now, when set, stamps defaults instead of the wall clock. Set before
	// use; read without a lock.
	Now func() time.Time

	mu sync.RWMutex

	messages   map[string]*Message
	messageIDs []string // ULIDs, kept sorted, which is receive order

	pnrs      map[string]*pnr.PNR // by id
	byLocator map[string]string   // locator -> id
	events    map[string][]Event

	// dedup is keyed by direction, peer and application key. Direction is part
	// of the key because an acknowledgement we sent and a request a partner
	// sent can legitimately carry the same reference, and because the Postgres
	// backend has always scoped this lookup by direction.
	dedup   map[string]string // direction|peer|key -> message id
	counter uint64

	queue        map[string]*QueueItem // by id
	queueIDs     []string              // ULIDs, kept sorted
	queuePending map[string]string     // queue|pnr|code -> id, pending items only
}

func (s *Mem) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}

// NewMem returns an empty in-memory store.
func NewMem() *Mem {
	return &Mem{
		messages:  map[string]*Message{},
		pnrs:      map[string]*pnr.PNR{},
		byLocator: map[string]string{},
		events:    map[string][]Event{},
		dedup:     map[string]string{},

		queue:        map[string]*QueueItem{},
		queuePending: map[string]string{},
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
	c.Tickets = append([]pnr.Ticket(nil), p.Tickets...)
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
		k := dedupKey(m.Direction, m.Peer, m.DedupKey)
		if _, exists := s.dedup[k]; !exists {
			s.dedup[k] = m.ID
		}
	}
	s.trimMessagesLocked()
	return nil
}

// Purge implements Store.
func (s *Mem) Purge(ctx context.Context, before time.Time) (Purged, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out Purged
	keep := s.messageIDs[:0]
	for _, id := range s.messageIDs {
		m := s.messages[id]
		if m != nil && m.At.Before(before) {
			if m.DedupKey != "" {
				k := dedupKey(m.Direction, m.Peer, m.DedupKey)
				if s.dedup[k] == id {
					delete(s.dedup, k)
				}
			}
			delete(s.messages, id)
			out.Messages++
			continue
		}
		keep = append(keep, id)
	}
	s.messageIDs = append([]string(nil), keep...)
	for id, p := range s.pnrs {
		if !p.UpdatedAt.Before(before) {
			continue
		}
		delete(s.byLocator, p.RecordLocator)
		delete(s.pnrs, id)
		delete(s.events, id)
		queued := len(s.queueIDs)
		s.dropQueueForRecordLocked(id)
		out.QueueItems += queued - len(s.queueIDs)
		out.Records++
	}
	return out, nil
}

// trimMessagesLocked discards the oldest messages once the bound is exceeded.
func (s *Mem) trimMessagesLocked() {
	if s.MaxMessages <= 0 || len(s.messageIDs) <= s.MaxMessages {
		return
	}
	drop := s.messageIDs[:len(s.messageIDs)-s.MaxMessages]
	s.messageIDs = append([]string(nil), s.messageIDs[len(drop):]...)
	for _, id := range drop {
		if m, ok := s.messages[id]; ok {
			if m.DedupKey != "" {
				k := dedupKey(m.Direction, m.Peer, m.DedupKey)
				if s.dedup[k] == id {
					delete(s.dedup, k)
				}
			}
			delete(s.messages, id)
		}
	}
}

// trimRecordsLocked discards the oldest records once the bound is exceeded.
// Ids are time-ordered, so the oldest sort first.
func (s *Mem) trimRecordsLocked() {
	if s.MaxRecords <= 0 || len(s.pnrs) <= s.MaxRecords {
		return
	}
	ids := make([]string, 0, len(s.pnrs))
	for id := range s.pnrs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids[:len(ids)-s.MaxRecords] {
		if p := s.pnrs[id]; p != nil {
			delete(s.byLocator, p.RecordLocator)
		}
		delete(s.pnrs, id)
		delete(s.events, id)
		s.dropQueueForRecordLocked(id)
	}
}

// dropQueueForRecordLocked removes queue items belonging to a discarded record.
// A queue item that outlives its record is a task nobody can action.
func (s *Mem) dropQueueForRecordLocked(pnrID string) {
	keep := s.queueIDs[:0]
	for _, id := range s.queueIDs {
		it := s.queue[id]
		if it == nil || it.PNRID != pnrID {
			keep = append(keep, id)
			continue
		}
		if it.Pending() {
			delete(s.queuePending, pendingKey(it.Queue, it.PNRID, it.Code, it.SegmentRef))
		}
		delete(s.queue, id)
	}
	s.queueIDs = append([]string(nil), keep...)
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
		k := dedupKey(m.Direction, m.Peer, m.DedupKey)
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

func dedupKey(dir Direction, peer, key string) string {
	return string(dir) + "|" + peer + "|" + key
}

func (s *Mem) FindByDedupKey(ctx context.Context, peer, key string) (string, bool, error) {
	return s.findKey(Inbound, peer, key)
}

func (s *Mem) FindOutboundByKey(ctx context.Context, peer, key string) (string, bool, error) {
	return s.findKey(Outbound, peer, key)
}

func (s *Mem) findKey(dir Direction, peer, key string) (string, bool, error) {
	if key == "" {
		return "", false, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.dedup[dedupKey(dir, peer, key)]
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
	s.trimRecordsLocked()
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

func (s *Mem) DividePNR(ctx context.Context, parent *pnr.PNR, expected int64,
	child *pnr.PNR, parentEvents, childEvents []Event) error {

	s.mu.Lock()
	defer s.mu.Unlock()

	// Everything is checked before anything is written, so a rejected division
	// leaves the store exactly as it found it. Holding the lock across both is
	// what makes this the same promise the Postgres transaction makes.
	cur, ok := s.pnrs[parent.ID]
	if !ok {
		return ErrNotFound
	}
	if cur.Version != expected {
		return ErrConflict
	}
	if child.ID == "" {
		child.ID = ulid.New()
	}
	if _, taken := s.byLocator[child.RecordLocator]; taken {
		return ErrDuplicate
	}
	if _, taken := s.pnrs[child.ID]; taken {
		return ErrDuplicate
	}

	parent.Version = expected + 1
	s.pnrs[parent.ID] = clonePNR(parent)
	s.byLocator[parent.RecordLocator] = parent.ID
	s.appendEventsLocked(parent.ID, parentEvents)

	child.Version = 1
	s.pnrs[child.ID] = clonePNR(child)
	s.byLocator[child.RecordLocator] = child.ID
	s.appendEventsLocked(child.ID, childEvents)
	s.trimRecordsLocked()
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

func pendingKey(queue, pnrID, code string, segRef int) string {
	return queue + "|" + pnrID + "|" + code + "|" + strconv.Itoa(segRef)
}

func cloneQueueItem(q *QueueItem) *QueueItem {
	c := *q
	if q.WorkedAt != nil {
		t := *q.WorkedAt
		c.WorkedAt = &t
	}
	return &c
}

func (s *Mem) Enqueue(ctx context.Context, item *QueueItem) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if item.ID == "" {
		item.ID = ulid.New()
	}
	if item.PlacedAt.IsZero() {
		item.PlacedAt = s.now()
	}
	k := pendingKey(item.Queue, item.PNRID, item.Code, item.SegmentRef)
	if _, dup := s.queuePending[k]; dup {
		return ErrDuplicate
	}
	s.queue[item.ID] = cloneQueueItem(item)
	s.queueIDs = append(s.queueIDs, item.ID)
	if n := len(s.queueIDs); n > 1 && s.queueIDs[n-1] < s.queueIDs[n-2] {
		sort.Strings(s.queueIDs)
	}
	s.queuePending[k] = item.ID
	return nil
}

func (s *Mem) WorkQueueItem(ctx context.Context, id, by, note string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	it, ok := s.queue[id]
	if !ok {
		return ErrNotFound
	}
	if !it.Pending() {
		return ErrConflict
	}
	now := s.now()
	it.WorkedAt = &now
	it.WorkedBy = by
	it.Note = note
	delete(s.queuePending, pendingKey(it.Queue, it.PNRID, it.Code, it.SegmentRef))
	return nil
}

func (s *Mem) ListQueue(ctx context.Context, f QueueFilter) ([]*QueueItem, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	limit := f.Limit
	if limit <= 0 {
		limit = 200
	}
	out := make([]*QueueItem, 0, limit)
	for i := len(s.queueIDs) - 1; i >= 0 && len(out) < limit; i-- {
		it := s.queue[s.queueIDs[i]]
		if it == nil {
			continue
		}
		if f.Queue != "" && it.Queue != f.Queue {
			continue
		}
		if f.PNRID != "" && it.PNRID != f.PNRID {
			continue
		}
		if !f.IncludeWorked && !it.Pending() {
			continue
		}
		out = append(out, cloneQueueItem(it))
	}
	return out, nil
}

func (s *Mem) QueueCounts(ctx context.Context) (map[string]int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[string]int{}
	for _, it := range s.queue {
		if it.Pending() {
			out[it.Queue]++
		}
	}
	return out, nil
}

func (s *Mem) FindPNRByDocument(ctx context.Context, compactNumber string) (*pnr.PNR, error) {
	if compactNumber == "" {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	// Every record, not a recent prefix: a document issued months ago is still
	// a document, and answering "not found" for it would be a lie.
	for _, p := range s.pnrs {
		for _, t := range p.Tickets {
			if t.Number.Compact() == compactNumber {
				return clonePNR(p), nil
			}
		}
	}
	return nil, nil
}

func (s *Mem) FindPNRByExternalLocator(ctx context.Context, owner, value string) (*pnr.PNR, error) {
	if value == "" {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.pnrs {
		for _, l := range p.Locators {
			if l.Value == value && (owner == "" || l.Owner == owner) {
				return clonePNR(p), nil
			}
		}
	}
	return nil, nil
}

// RevenueByLeg implements Store.
func (s *Mem) RevenueByLeg(ctx context.Context, wireDate string) ([]LegRevenue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	acc := map[LegRevenue]int64{}
	for _, p := range s.pnrs {
		if p.Status == pnr.StatusCancelled || p.Pricing == nil || p.Pricing.Total == 0 {
			continue
		}
		var air []pnr.Segment
		for _, sg := range p.Segments {
			if sg.Type == pnr.SegmentAir && sg.Status != "XX" {
				air = append(air, sg)
			}
		}
		if len(air) == 0 {
			continue
		}
		share := p.Pricing.Total / int64(len(air))
		for _, sg := range air {
			if wireDate != "" && !strings.EqualFold(sg.WireDate, wireDate) {
				continue
			}
			carrier := sg.Carrier
			if sg.OperatingCarrier != "" {
				carrier = sg.OperatingCarrier
			}
			k := LegRevenue{Carrier: strings.ToUpper(carrier), FlightNum: strings.TrimLeft(sg.FlightNum, "0"), WireDate: strings.ToUpper(sg.WireDate), Board: strings.ToUpper(sg.Board)}
			acc[k] += share
		}
	}
	out := make([]LegRevenue, 0, len(acc))
	for k, c := range acc {
		k.Cents = c
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Carrier != b.Carrier {
			return a.Carrier < b.Carrier
		}
		if a.FlightNum != b.FlightNum {
			return a.FlightNum < b.FlightNum
		}
		return a.Board < b.Board
	})
	return out, nil
}

type memLease struct {
	holder  string
	expires time.Time
}

// Acquire implements Leaser. A memory store's leases only order the
// processes sharing that store, which is the test harness's case.
func (s *Mem) Acquire(ctx context.Context, system, holder string, ttl time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.leases == nil {
		s.leases = map[string]memLease{}
	}
	now := s.now()
	if l, ok := s.leases[system]; ok && l.holder != holder && l.expires.After(now) {
		return false, nil
	}
	s.leases[system] = memLease{holder: holder, expires: now.Add(ttl)}
	return true, nil
}

// Renew implements Leaser.
func (s *Mem) Renew(ctx context.Context, system, holder string, ttl time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.leases[system]
	if !ok || l.holder != holder || !l.expires.After(s.now()) {
		return false, nil
	}
	s.leases[system] = memLease{holder: holder, expires: s.now().Add(ttl)}
	return true, nil
}

// Release implements Leaser.
func (s *Mem) Release(ctx context.Context, system, holder string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l, ok := s.leases[system]; ok && l.holder == holder {
		delete(s.leases, system)
	}
	return nil
}

// Ping implements Pinger: memory is always here.
func (s *Mem) Ping(ctx context.Context) error { return nil }

// SoldSeats implements Store.
func (s *Mem) SoldSeats(ctx context.Context, carrier, wireDate string) ([]SoldSeats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	acc := map[SoldSeats]int{}
	for _, p := range s.pnrs {
		if p.Status == pnr.StatusCancelled {
			continue
		}
		for _, sg := range p.Segments {
			if sg.Type != pnr.SegmentAir || sg.Carrier != carrier || sg.Status == "XX" {
				continue
			}
			if wireDate != "" && !strings.EqualFold(sg.WireDate, wireDate) {
				continue
			}
			k := SoldSeats{Carrier: sg.Carrier, FlightNum: strings.TrimLeft(sg.FlightNum, "0"), WireDate: strings.ToUpper(sg.WireDate), Board: sg.Board, Class: sg.Class, Status: sg.Status}
			acc[k] += sg.Seats
		}
	}
	out := make([]SoldSeats, 0, len(acc))
	for k, n := range acc {
		k.Seats = n
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.FlightNum != b.FlightNum {
			return a.FlightNum < b.FlightNum
		}
		if a.WireDate != b.WireDate {
			return a.WireDate < b.WireDate
		}
		if a.Board != b.Board {
			return a.Board < b.Board
		}
		if a.Class != b.Class {
			return a.Class < b.Class
		}
		return a.Status < b.Status
	})
	return out, nil
}

// LoadPNRs implements Store.
func (s *Mem) LoadPNRs(ctx context.Context, recs []*pnr.PNR, actor string) error {
	for _, p := range recs {
		if p.RecordLocator != "" {
			if _, err := s.GetPNR(ctx, p.RecordLocator); err == nil {
				return ErrDuplicate
			}
		}
	}
	for _, p := range recs {
		if err := s.CreatePNR(ctx, p, []Event{{Type: "loaded", Actor: actor, At: p.CreatedAt}}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Mem) FindPNRsByFlight(ctx context.Context, flightKey, wireDate string, limit int) ([]*pnr.PNR, error) {
	return s.findOnFlight(flightKey, wireDate, limit, pnrOnFlight)
}

// FindPNRsEverOnFlight implements Store.
func (s *Mem) FindPNRsEverOnFlight(ctx context.Context, flightKey, wireDate string, limit int) ([]*pnr.PNR, error) {
	return s.findOnFlight(flightKey, wireDate, limit, pnrEverOnFlight)
}

func (s *Mem) findOnFlight(flightKey, wireDate string, limit int, match func(*pnr.PNR, string, string) bool) ([]*pnr.PNR, error) {
	if flightKey == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10000
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*pnr.PNR
	for _, p := range s.pnrs {
		if !match(p, flightKey, wireDate) {
			continue
		}
		out = append(out, clonePNR(p))
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *Mem) FindPNRsStale(ctx context.Context, before time.Time, limit int) ([]*pnr.PNR, error) {
	if limit <= 0 {
		limit = 10000
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*pnr.PNR
	for _, p := range s.pnrs {
		if p.Status == pnr.StatusCancelled || !p.UpdatedAt.Before(before) {
			continue
		}
		out = append(out, clonePNR(p))
	}
	// Most overdue first, so a limit drops the least urgent work.
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.Before(out[j].UpdatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *Mem) FindPNRsDueBy(ctx context.Context, deadline time.Time, limit int) ([]*pnr.PNR, error) {
	if limit <= 0 {
		limit = 10000
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	type due struct {
		p  *pnr.PNR
		at time.Time
	}
	var found []due
	for _, p := range s.pnrs {
		d := p.NextDeadline()
		if d == nil || !d.Before(deadline) {
			continue
		}
		found = append(found, due{clonePNR(p), *d})
	}
	sort.Slice(found, func(i, j int) bool { return found[i].at.Before(found[j].at) })
	out := make([]*pnr.PNR, 0, len(found))
	for _, f := range found {
		out = append(out, f.p)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// ExportPNRs implements Exporter: every record, by id.
func (s *Mem) ExportPNRs(ctx context.Context, fn func(*pnr.PNR) error) error {
	s.mu.RLock()
	all := make([]*pnr.PNR, 0, len(s.pnrs))
	for _, p := range s.pnrs {
		all = append(all, clonePNR(p))
	}
	s.mu.RUnlock()
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	for _, p := range all {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := fn(p); err != nil {
			return err
		}
	}
	return nil
}
