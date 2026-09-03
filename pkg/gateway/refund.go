package gateway

import (
	"context"
	"errors"
	"fmt"

	"github.com/adamf/jetway/pkg/pnr"
	"github.com/adamf/jetway/pkg/store"
	"github.com/adamf/jetway/pkg/telemetry"
)

// RefundOptions say who refunds and why.
type RefundOptions struct {
	By     string
	Reason string
}

// ErrNothingToRefund is returned when the record's documents have no open
// coupon: never ticketed, already refunded, or every coupon used.
var ErrNothingToRefund = errors.New("gateway: record has no open coupon to refund")

// Refund returns the value of a record's unused flight coupons to the
// purchaser: every open coupon becomes refunded, the document is dated
// refunded, and the settlement plan then reports it as a refund
// transaction with the amounts reversed. Coupons already lifted, checked
// in or exchanged are not touched -- that value has been used -- so a
// document flown on the outbound and refunded on the return is a partial
// refund the plan can see coupon by coupon. Refunding twice changes
// nothing. The record itself is left as it stands: a refund is money, not
// an itinerary change; cancel the segments separately when they are to go.
func (g *Gateway) Refund(ctx context.Context, locator string, opts RefundOptions) (*pnr.PNR, error) {
	ctx, span := telemetry.Start(ctx, "jetway.ticket.refund", telemetry.AttrLocator.String(locator))
	defer span.End()
	const maxAttempts = 5
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		rec, err := g.Store.GetPNR(ctx, locator)
		if err != nil {
			telemetry.Fail(span, err)
			return nil, err
		}
		now := g.now()
		expected := rec.Version
		var events []store.Event
		for i := range rec.Tickets {
			t := &rec.Tickets[i]
			if t.Type != "" && t.Type != pnr.DocTicket {
				continue
			}
			n := 0
			for j := range t.Coupons {
				if t.Coupons[j].Status == pnr.CouponOpen {
					t.Coupons[j].Status = pnr.CouponRefunded
					n++
				}
			}
			if n == 0 {
				continue
			}
			at := now
			t.RefundedAt = &at
			events = append(events, store.Event{
				Type: "refund", At: now, Actor: opts.By,
				Detail: fmt.Sprintf("%s refunded over %d coupon(s): %s", t.Number, n, opts.Reason),
			})
		}
		if len(events) == 0 {
			return rec, ErrNothingToRefund
		}
		rec.UpdatedAt = now
		switch err := g.Store.UpdatePNR(ctx, rec, expected, events); {
		case err == nil:
			g.Bus.Publish(EvPNR, g.pnrView(rec))
			return rec, nil
		case errors.Is(err, store.ErrConflict):
			lastErr = err
			continue
		default:
			telemetry.Fail(span, err)
			return nil, err
		}
	}
	return nil, fmt.Errorf("gateway: refund %s: %w", locator, lastErr)
}
