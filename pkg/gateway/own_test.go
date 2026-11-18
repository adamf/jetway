package gateway

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/adamf/jetway/pkg/pnr"
	"github.com/adamf/jetway/pkg/queue"
	"github.com/adamf/jetway/pkg/store"
)

func ownBooking(carrier, flight, class string, seats int) *BookingRequest {
	return &BookingRequest{
		Passengers: []BookingPassenger{{Surname: "direct", Given: "ann", Title: "ms"}, {Surname: "direct", Given: "tom", Title: "mr"}}[:seats],
		Segments: []BookingSegment{{Carrier: carrier, FlightNum: flight, Class: class,
			Date: pnr.FormatDate(time.Now().UTC().AddDate(0, 0, 30)), Board: "LHR", Off: "JFK", Seats: seats}},
		Agent: "web", Channel: "direct",
	}
}

// releasing is an inventory that takes seats back, and says what it was
// given back.
type releasing struct {
	*Inventory
	back []pnr.Segment
}

func (r *releasing) Release(ctx context.Context, s pnr.Segment, was string) {
	r.back = append(r.back, s)
	r.mu.Lock()
	r.sold[flightKey(s)] -= s.Seats
	r.mu.Unlock()
}

// A carrier's own node sells the carrier's own seats. Its website books
// on it, and the seat is held at once from the node's own inventory, with
// nothing on a wire: there is nobody to ask. Before this the node wrote
// the record, found no link to itself, and left the segment at HN for
// ever.
func TestACarrierSellsItsOwnSeats(t *testing.T) {
	ctx := context.Background()
	gds, air := wire(t, "BA", store.FormatTypeB)
	air.gw.Queues = &queue.Manager{Store: air.st, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	inv := &releasing{Inventory: air.gw.Responder.(*Inventory)}
	air.gw.Responder = inv
	// Everything the carrier's node sends is counted, and still delivered.
	sent := 0
	deliver := air.gw.Sender
	air.gw.Sender = SenderFunc(func(ctx context.Context, peer string, raw []byte) error {
		sent++
		return deliver.Send(ctx, peer, raw)
	})

	res, err := air.gw.Book(ctx, ownBooking("BA", "0175", "Y", 2))
	if err != nil {
		t.Fatal(err)
	}
	if got := res.PNR.Segments[0].Status; got != "HK" {
		t.Fatalf("the carrier's own sale is at %s, want HK", got)
	}
	if len(res.Sent) != 0 || sent != 0 {
		t.Errorf("an own sale put %d messages on a wire", sent)
	}
	stored, err := air.st.GetPNR(ctx, res.PNR.RecordLocator)
	if err != nil || stored.Segments[0].Status != "HK" || stored.Origin.Channel != "direct" {
		t.Fatalf("the stored record: %+v, %v", stored, err)
	}
	if recs, _ := gds.st.ListPNRs(ctx, 10); len(recs) != 0 {
		t.Error("the distribution system heard about a sale it had no part in")
	}
	sent = 0
	// The seats are the same seats a partner asks for: four on the flight,
	// two sold here, so a distribution system can have two and no more.
	if r, err := gds.gw.Book(ctx, booking("Y", 2)); err != nil {
		t.Fatal(err)
	} else if rec, _ := gds.st.GetPNR(ctx, r.PNR.RecordLocator); rec.Segments[0].Status != "HK" {
		t.Errorf("the last two seats went to the distribution system at %s", rec.Segments[0].Status)
	}
	if sent != 1 {
		t.Errorf("the carrier answered the distribution system's sell with %d messages", sent)
	}
	sent = 0
	full, err := air.gw.Book(ctx, ownBooking("BA", "0175", "Y", 1))
	if err != nil {
		t.Fatal(err)
	}
	if got := full.PNR.Segments[0].Status; got == "HK" || got == "HN" {
		t.Errorf("a fifth seat on a flight of four was sold direct at %s", got)
	}

	// Ticketed, there is no carrier to tell and nothing to disagree about.
	if _, err := air.gw.IssueTickets(ctx, res.PNR.RecordLocator, IssueOptions{AirlineCode: "125", IssuedBy: "web"}); err != nil {
		t.Fatal(err)
	}
	// Cancelled, the seats go back: nobody is unreachable, nothing is
	// queued as a divergence, and the two seats sell again.
	out, err := air.gw.Cancel(ctx, res.PNR.RecordLocator, CancelOptions{By: "web", Reason: "changed plans"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Unreachable) != 0 || out.PNR.Status != pnr.StatusCancelled {
		t.Errorf("cancelling an own sale: unreachable %v, status %s", out.Unreachable, out.PNR.Status)
	}
	if items, _ := air.st.ListQueue(ctx, store.QueueFilter{Queue: store.QueueDivergence}); len(items) != 0 {
		t.Errorf("an own sale left %d divergences on the queue: %+v", len(items), items[0])
	}
	if sent != 0 {
		t.Errorf("ticketing and cancelling an own sale sent %d messages", sent)
	}
	if len(inv.back) != 1 || inv.back[0].Seats != 2 || inv.back[0].Status != "HK" || inv.back[0].FlightNum != "0175" {
		t.Fatalf("the inventory was given back %+v, want the two seats as they were held", inv.back)
	}
	again, err := air.gw.Book(ctx, ownBooking("BA", "0175", "Y", 2))
	if err != nil || again.PNR.Segments[0].Status != "HK" {
		t.Fatalf("the seats a cancellation gave back did not sell again: %v, %s", err, again.PNR.Segments[0].Status)
	}
}

// A record with the node's own flight and another carrier's: the node's
// own seat is decided here and the other is asked for over its link.
func TestAnOwnSaleWithAPartnersLeg(t *testing.T) {
	ctx := context.Background()
	_, air := wire(t, "BA", store.FormatTypeB)
	var to []string
	air.gw.AddPeer(&Peer{Name: "AA", Carrier: "AA", Format: store.FormatTypeB, TTYAddress: "DFWRMAA"})
	air.gw.Sender = SenderFunc(func(ctx context.Context, peer string, raw []byte) error { to = append(to, peer); return nil })
	req := ownBooking("BA", "0175", "Y", 1)
	req.Segments = append(req.Segments, BookingSegment{Carrier: "AA", FlightNum: "0100", Class: "Y",
		Date: req.Segments[0].Date, Board: "JFK", Off: "DFW", Seats: 1})
	res, err := air.gw.Book(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.PNR.Segments[0].Status != "HK" || res.PNR.Segments[1].Status != "HN" {
		t.Errorf("own leg %s, partner's leg %s", res.PNR.Segments[0].Status, res.PNR.Segments[1].Status)
	}
	if len(to) != 1 || to[0] != "AA" {
		t.Errorf("messages went to %v, want one to AA", to)
	}
	// Cancelled: American is told, the node's own seat is released, and
	// the node is not counted as a carrier it could not reach.
	to = nil
	out, err := air.gw.Cancel(ctx, res.PNR.RecordLocator, CancelOptions{By: "web"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Unreachable) != 0 || len(out.Notified) != 1 || out.Notified[0] != "AA" || len(to) != 1 {
		t.Errorf("notified %v, unreachable %v, sent to %v", out.Notified, out.Unreachable, to)
	}
}

// A node that distributes and answers for no seats owns nothing, whatever
// its designator says.
func TestADistributionNodeOwnsNothing(t *testing.T) {
	gds, air := wire(t, "BA", store.FormatTypeB)
	if gds.gw.Own("1J") || gds.gw.Own("BA") {
		t.Error("a node with no responder owns seats")
	}
	if !air.gw.Own("BA") || !air.gw.Own("ba") || air.gw.Own("AA") || air.gw.Own("") {
		t.Error("a carrier's node owns its own carrier's seats and nobody else's")
	}
}

// The record a booking publishes is not written to afterwards. A
// listener on the bus reads each record it is handed, and the own-sale
// step once changed the very record the booking had just published.
// Run under the race detector, a listener that reads while own sales are
// made finds any such write.
func TestAnOwnSaleDoesNotWriteToAPublishedRecord(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, air := wire(t, "BA", store.FormatTypeB)
	air.gw.Responder.(*Inventory).SetCapacity(200)
	events, stop := air.gw.Bus.Subscribe()
	defer stop()
	read := make(chan int)
	go func() {
		n := 0
		for {
			select {
			case <-ctx.Done():
				read <- n
				return
			case ev := <-events:
				if ev.Type != EvPNR {
					continue
				}
				if p, ok := ev.Data.(map[string]any); ok {
					if rec, ok := p["record"].(*pnr.PNR); ok {
						for _, s := range rec.Segments {
							n += len(s.Status)
						}
						n += int(rec.Version) + len(rec.Status)
					}
				}
			}
		}
	}()
	for i := 0; i < 60; i++ {
		res, err := air.gw.Book(ctx, ownBooking("BA", "0175", "Y", 1))
		if err != nil || res.PNR.Segments[0].Status != "HK" {
			t.Fatalf("own sale %d: %v", i, err)
		}
	}
	time.Sleep(50 * time.Millisecond)
	cancel()
	if <-read == 0 {
		t.Error("the listener read no record")
	}
}
