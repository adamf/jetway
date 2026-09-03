package gateway

import (
	"context"
	"errors"
	"fmt"

	"github.com/adamf/jetway/pkg/pnr"
	"github.com/adamf/jetway/pkg/store"
	"github.com/adamf/jetway/pkg/telemetry"
)

// ExchangeOptions say who reissues, why, and on whose document stock.
type ExchangeOptions struct {
	// AirlineCode is the three-digit stock the new document is issued on;
	// empty reissues on the exchanged document's own.
	AirlineCode string
	By, Reason  string
}

// ErrNothingToExchange is returned when the record has no ticket with an
// open coupon to exchange, or no live segment to reissue over.
var ErrNothingToExchange = errors.New("gateway: record has nothing to exchange")

// Exchange reissues a record's tickets over its live itinerary: every open
// coupon of the standing documents is marked exchanged, and each passenger
// gets a new document covering the segments now held, naming the one it
// replaces. This is what follows a schedule change once the passenger is
// reprotected -- the involuntary reissue -- and a change of plans the
// passenger asked for. Coupons already lifted keep their value used and
// stay on the old document. Settlement then reports the new document as
// a sale with the original issue behind it and the old document's value
// as the form of payment.
func (g *Gateway) Exchange(ctx context.Context, locator string, opts ExchangeOptions) (*pnr.PNR, error) {
	ctx, span := telemetry.Start(ctx, "jetway.ticket.exchange", telemetry.AttrLocator.String(locator))
	defer span.End()
	const maxAttempts = 5
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		rec, err := g.Store.GetPNR(ctx, locator)
		if err != nil {
			telemetry.Fail(span, err)
			return nil, err
		}
		segs := ticketableSegments(rec)
		if len(segs) == 0 {
			return nil, ErrNothingToExchange
		}
		now := g.now()
		expected := rec.Version
		var events []store.Event
		// Which passengers hold a document with an open coupon: those are
		// exchanged; the rest are left as they are.
		exchanged := map[int]pnr.TicketNumber{}
		for i := range rec.Tickets {
			t := &rec.Tickets[i]
			if t.Type != "" && t.Type != pnr.DocTicket || t.ExchangedFrom != nil && allExchanged(*t) {
				continue
			}
			n := 0
			for j := range t.Coupons {
				if t.Coupons[j].Status == pnr.CouponOpen {
					t.Coupons[j].Status = pnr.CouponExchanged
					n++
				}
			}
			if n == 0 {
				continue
			}
			exchanged[t.PaxRef] = t.Number
			events = append(events, store.Event{Type: "exchange", At: now, Actor: opts.By,
				Detail: fmt.Sprintf("%s exchanged over %d coupon(s): %s", t.Number, n, opts.Reason)})
		}
		if len(exchanged) == 0 {
			return nil, ErrNothingToExchange
		}
		for _, pax := range rec.Passengers {
			old, ok := exchanged[pax.Ref]
			if !ok {
				continue
			}
			stock := opts.AirlineCode
			if stock == "" {
				stock = old.AirlineCode
			}
			tickets, err := g.issueFor(ctx, rec, pax, segs, IssueOptions{AirlineCode: stock, IssuedBy: opts.By}, now)
			if err != nil {
				return nil, err
			}
			for i := range tickets {
				from := old
				tickets[i].ExchangedFrom = &from
				events = append(events, store.Event{Type: "ticket_issued", At: now, Actor: opts.By,
					Detail: fmt.Sprintf("%s to %s/%s in exchange for %s over %d coupon(s)", tickets[i].Number, pax.Surname, pax.Given, old, len(tickets[i].Coupons))})
			}
			rec.Tickets = append(rec.Tickets, tickets...)
		}
		rec.UpdatedAt = now
		rec.Status = pnr.StatusTicketed
		switch err := g.Store.UpdatePNR(ctx, rec, expected, events); {
		case err == nil:
			g.Bus.Publish(EvPNR, g.pnrView(rec))
			g.notifyTicketed(ctx, rec, opts.By)
			return rec, nil
		case errors.Is(err, store.ErrConflict):
			lastErr = err
			continue
		default:
			telemetry.Fail(span, err)
			return nil, err
		}
	}
	return nil, fmt.Errorf("gateway: exchange %s: %w", locator, lastErr)
}

func allExchanged(t pnr.Ticket) bool {
	for _, c := range t.Coupons {
		if c.Status != pnr.CouponExchanged {
			return false
		}
	}
	return true
}
