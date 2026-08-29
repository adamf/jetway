package gateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/adamf/jetway/internal/store"
	"github.com/adamf/jetway/pkg/pnr"
)

// IssueOptions controls ticket issuance.
type IssueOptions struct {
	// AirlineCode is the three-digit numeric code whose stock is being issued
	// against, e.g. 125 for British Airways. It is not the two-letter
	// designator, and there is no reliable mapping between the two, so it has
	// to be supplied.
	AirlineCode string
	// IssuedBy names who issued, for the audit trail.
	IssuedBy string
}

// ErrNothingToTicket is returned when a record has no segment a ticket could
// cover.
var ErrNothingToTicket = errors.New("gateway: record has no live air segment to ticket")

// IssueTickets issues documents against a record, one set per passenger.
//
// Issuing is what a ticketing time limit is waiting for, so it is also what
// satisfies it: the deadline is cleared and any ticketing task on the queue is
// worked. Leaving the limit standing after issuance would have the sweeper
// raise a record that has already been dealt with, every pass, forever.
func (g *Gateway) IssueTickets(ctx context.Context, locator string, opts IssueOptions) (*pnr.PNR, error) {
	if len(opts.AirlineCode) != 3 {
		return nil, fmt.Errorf("gateway: airline code must be three digits, got %q", opts.AirlineCode)
	}
	const maxAttempts = 5
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		rec, err := g.Store.GetPNR(ctx, locator)
		if err != nil {
			return nil, err
		}
		if rec.Status == pnr.StatusCancelled {
			return nil, fmt.Errorf("gateway: %s is cancelled", rec.RecordLocator)
		}
		if rec.Ticketed() {
			// Issuing twice would put two live documents against the same
			// coupon, which is a refund problem rather than a booking one.
			return rec, nil
		}

		segs := ticketableSegments(rec)
		if len(segs) == 0 {
			return nil, ErrNothingToTicket
		}

		now := time.Now().UTC()
		expected := rec.Version
		var events []store.Event

		for _, pax := range rec.Passengers {
			if _, done := rec.TicketFor(pax.Ref); done {
				continue
			}
			tickets, err := g.issueFor(ctx, pax, segs, opts, now)
			if err != nil {
				return nil, err
			}
			rec.Tickets = append(rec.Tickets, tickets...)
			for _, t := range tickets {
				events = append(events, store.Event{
					Type: "ticket_issued", At: now, Actor: opts.IssuedBy,
					Detail: fmt.Sprintf("%s to %s/%s over %d coupon(s)",
						t.Number, pax.Surname, pax.Given, len(t.Coupons)),
				})
			}
		}
		if len(events) == 0 {
			return rec, nil
		}

		// The limit has been met, so it stops being a limit. The arrangement
		// text stays: it says how the booking was to be ticketed, which is
		// still true.
		for i := range rec.Ticketing {
			if rec.Ticketing[i].Deadline != nil {
				rec.Ticketing[i].Deadline = nil
				events = append(events, store.Event{
					Type: "tktl_satisfied", At: now, Actor: opts.IssuedBy,
					Detail: "ticketing time limit cleared by issuance",
				})
			}
		}
		rec.UpdatedAt = now
		rec.Status = pnr.StatusTicketed

		switch err := g.Store.UpdatePNR(ctx, rec, expected, events); {
		case err == nil:
			g.Bus.Publish(EvPNR, g.pnrView(rec))
			g.workTicketingQueue(ctx, rec, opts.IssuedBy)
			return rec, nil
		case errors.Is(err, store.ErrConflict):
			lastErr = err
			continue
		default:
			return nil, fmt.Errorf("gateway: persist tickets: %w", err)
		}
	}
	return nil, fmt.Errorf("gateway: gave up issuing after %d attempts: %w", maxAttempts, lastErr)
}

// ticketableSegments returns the air segments a coupon can be written against.
func ticketableSegments(rec *pnr.PNR) []*pnr.Segment {
	var out []*pnr.Segment
	for i := range rec.Segments {
		s := &rec.Segments[i]
		if s.Type != pnr.SegmentAir || s.Status == "XX" {
			continue
		}
		out = append(out, s)
	}
	return out
}

// issueFor builds the document set covering one passenger's itinerary.
//
// A ticket carries four flight coupons. An itinerary longer than that spills
// onto conjunction documents, and each of them names the others so a partner
// holding one can find the rest.
func (g *Gateway) issueFor(ctx context.Context, pax pnr.Passenger, segs []*pnr.Segment,
	opts IssueOptions, now time.Time) ([]pnr.Ticket, error) {
	var chunks [][]*pnr.Segment
	for i := 0; i < len(segs); i += pnr.MaxCoupons {
		end := min(i+pnr.MaxCoupons, len(segs))
		chunks = append(chunks, segs[i:end])
	}

	numbers := make([]pnr.TicketNumber, len(chunks))
	for i := range chunks {
		n, err := g.nextTicketNumber(ctx, opts.AirlineCode)
		if err != nil {
			return nil, err
		}
		numbers[i] = n
	}

	out := make([]pnr.Ticket, 0, len(chunks))
	for i, chunk := range chunks {
		t := pnr.Ticket{
			Number: numbers[i], PaxRef: pax.Ref,
			IssuedAt: now, IssuedBy: opts.IssuedBy,
		}
		for j, s := range chunk {
			t.Coupons = append(t.Coupons, pnr.Coupon{
				Number: j + 1, SegmentRef: s.Ref, Status: pnr.CouponOpen,
			})
		}
		for j, n := range numbers {
			if j != i {
				t.Conjunction = append(t.Conjunction, n)
			}
		}
		out = append(out, t)
	}
	return out, nil
}

// nextTicketNumber allocates a document number.
//
// The serial comes from the same counter that backs record locators. That
// counter's only contract is that it never returns a value twice, which is
// exactly what a document number needs; giving ticketing its own sequence would
// be a second thing to keep unique for no gain.
func (g *Gateway) nextTicketNumber(ctx context.Context, airlineCode string) (pnr.TicketNumber, error) {
	n, err := g.Store.NextLocatorCounter(ctx)
	if err != nil {
		return pnr.TicketNumber{}, fmt.Errorf("gateway: allocate a ticket serial: %w", err)
	}
	serial := fmt.Sprintf("%09d", n%1_000_000_000)
	return pnr.NewTicketNumber(airlineCode, serial)
}

// workTicketingQueue clears any ticketing task now that the record is ticketed.
func (g *Gateway) workTicketingQueue(ctx context.Context, rec *pnr.PNR, by string) {
	if g.Queues == nil {
		return
	}
	items, err := g.Store.ListQueue(ctx, store.QueueFilter{
		Queue: store.QueueTicketing, PNRID: rec.ID,
	})
	if err != nil {
		g.Log.Error("could not read the ticketing queue", "locator", rec.RecordLocator, "err", err)
		return
	}
	if by == "" {
		by = "issuance"
	}
	for _, it := range items {
		if err := g.Queues.Work(ctx, it.ID, by, "ticketed"); err != nil {
			g.Log.Error("could not clear a ticketing task",
				"locator", rec.RecordLocator, "item", it.ID, "err", err)
		}
	}
}

// TicketSummary renders a record's ticketing state for display.
func TicketSummary(rec *pnr.PNR) string {
	if len(rec.Tickets) == 0 {
		return "not ticketed"
	}
	var b strings.Builder
	for i, t := range rec.Tickets {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(t.Number.String())
	}
	return b.String()
}
