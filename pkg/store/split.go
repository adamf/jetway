package store

import (
	"context"
	"time"

	"github.com/adamf/jetway/pkg/pnr"
)

// Split keeps the message log in one store and the book of record in
// another.
//
// It is the shape a hosted carrier platform takes when hundreds of systems
// share one database: the ledger of wire bytes is hot, enormous and bounded
// -- kept in memory with a cap, or in a partitioned log of its own -- while
// the records, their events and their queues are what must survive a
// restart and be found by locator months later. Every method forwards to
// the store that owns that kind of data; Close closes both.
type Split struct {
	Messages Store
	Records  Store
}

func (s Split) AppendMessage(ctx context.Context, m *Message) error {
	return s.Messages.AppendMessage(ctx, m)
}
func (s Split) UpdateMessage(ctx context.Context, m *Message) error {
	return s.Messages.UpdateMessage(ctx, m)
}
func (s Split) GetMessage(ctx context.Context, id string) (*Message, error) {
	return s.Messages.GetMessage(ctx, id)
}
func (s Split) ListMessages(ctx context.Context, f MessageFilter) ([]*Message, error) {
	return s.Messages.ListMessages(ctx, f)
}
func (s Split) FindByDedupKey(ctx context.Context, peer, key string) (string, bool, error) {
	return s.Messages.FindByDedupKey(ctx, peer, key)
}
func (s Split) FindOutboundByKey(ctx context.Context, peer, key string) (string, bool, error) {
	return s.Messages.FindOutboundByKey(ctx, peer, key)
}

func (s Split) CreatePNR(ctx context.Context, p *pnr.PNR, events []Event) error {
	return s.Records.CreatePNR(ctx, p, events)
}
func (s Split) UpdatePNR(ctx context.Context, p *pnr.PNR, expected int64, events []Event) error {
	return s.Records.UpdatePNR(ctx, p, expected, events)
}
func (s Split) GetPNR(ctx context.Context, locator string) (*pnr.PNR, error) {
	return s.Records.GetPNR(ctx, locator)
}
func (s Split) GetPNRByID(ctx context.Context, id string) (*pnr.PNR, error) {
	return s.Records.GetPNRByID(ctx, id)
}
func (s Split) ListPNRs(ctx context.Context, limit int) ([]*pnr.PNR, error) {
	return s.Records.ListPNRs(ctx, limit)
}
func (s Split) Events(ctx context.Context, pnrID string) ([]Event, error) {
	return s.Records.Events(ctx, pnrID)
}
func (s Split) NextLocatorCounter(ctx context.Context) (uint64, error) {
	return s.Records.NextLocatorCounter(ctx)
}
func (s Split) DividePNR(ctx context.Context, parent *pnr.PNR, expected int64, child *pnr.PNR,
	parentEvents, childEvents []Event) error {
	return s.Records.DividePNR(ctx, parent, expected, child, parentEvents, childEvents)
}

func (s Split) FindPNRByDocument(ctx context.Context, compactNumber string) (*pnr.PNR, error) {
	return s.Records.FindPNRByDocument(ctx, compactNumber)
}
func (s Split) FindPNRByExternalLocator(ctx context.Context, owner, value string) (*pnr.PNR, error) {
	return s.Records.FindPNRByExternalLocator(ctx, owner, value)
}
func (s Split) RevenueByLeg(ctx context.Context, wireDate string) ([]LegRevenue, error) {
	return s.Records.RevenueByLeg(ctx, wireDate)
}
func (s Split) SoldSeats(ctx context.Context, carrier, wireDate string) ([]SoldSeats, error) {
	return s.Records.SoldSeats(ctx, carrier, wireDate)
}
func (s Split) LoadPNRs(ctx context.Context, recs []*pnr.PNR, actor string) error {
	return s.Records.LoadPNRs(ctx, recs, actor)
}
func (s Split) FindPNRsByFlight(ctx context.Context, flightKey, wireDate string, limit int) ([]*pnr.PNR, error) {
	return s.Records.FindPNRsByFlight(ctx, flightKey, wireDate, limit)
}
func (s Split) FindPNRsEverOnFlight(ctx context.Context, flightKey, wireDate string, limit int) ([]*pnr.PNR, error) {
	return s.Records.FindPNRsEverOnFlight(ctx, flightKey, wireDate, limit)
}
func (s Split) FindPNRsStale(ctx context.Context, before time.Time, limit int) ([]*pnr.PNR, error) {
	return s.Records.FindPNRsStale(ctx, before, limit)
}
func (s Split) FindPNRsDueBy(ctx context.Context, deadline time.Time, limit int) ([]*pnr.PNR, error) {
	return s.Records.FindPNRsDueBy(ctx, deadline, limit)
}

func (s Split) Enqueue(ctx context.Context, item *QueueItem) error {
	return s.Records.Enqueue(ctx, item)
}
func (s Split) WorkQueueItem(ctx context.Context, id, by, note string) error {
	return s.Records.WorkQueueItem(ctx, id, by, note)
}
func (s Split) ListQueue(ctx context.Context, f QueueFilter) ([]*QueueItem, error) {
	return s.Records.ListQueue(ctx, f)
}
func (s Split) QueueCounts(ctx context.Context) (map[string]int, error) {
	return s.Records.QueueCounts(ctx)
}

// Purge purges both halves and sums what went.
func (s Split) Purge(ctx context.Context, before time.Time) (Purged, error) {
	a, err := s.Messages.Purge(ctx, before)
	if err != nil {
		return a, err
	}
	b, err := s.Records.Purge(ctx, before)
	return Purged{Messages: a.Messages + b.Messages, Records: a.Records + b.Records,
		QueueItems: a.QueueItems + b.QueueItems}, err
}

// Close closes both stores. A shared records store -- one Postgres serving
// many nodes -- must not be closed by one of them, so callers with that
// shape close the shared store themselves and give Split a records store
// whose Close is harmless.
func (s Split) Close() error {
	err := s.Messages.Close()
	if err2 := s.Records.Close(); err == nil {
		err = err2
	}
	return err
}
