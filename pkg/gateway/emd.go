package gateway

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/adamf/jetway/pkg/padis"
	"github.com/adamf/jetway/pkg/pnr"
	"github.com/adamf/jetway/pkg/store"
	"github.com/adamf/jetway/pkg/telemetry"
)

// EMDCoupon is one value coupon to issue.
type EMDCoupon struct {
	// RFISC is what specifically was bought. Mandatory: a coupon with no
	// sub-code says a fee was charged without saying what for.
	RFISC string
	// SegmentRef is the segment whose flight coupon this value coupon is
	// associated to. Required for an EMD-A, meaningless on an EMD-S.
	SegmentRef int
	Amount     string
	Currency   string
	// ConsumedAtIssuance puts the coupon straight to a final status, for a
	// service delivered at the counter rather than in the air.
	ConsumedAtIssuance bool
}

// EMDRequest asks for a miscellaneous document.
type EMDRequest struct {
	Locator string
	PaxRef  int
	Type    pnr.DocumentType
	RFIC    pnr.RFIC
	// AirlineCode is the three-digit numeric stock code, as for a ticket.
	AirlineCode string
	IssuedBy    string
	Coupons     []EMDCoupon
}

// ErrNotAssociable is returned when an EMD-A names a segment that has no flight
// coupon to associate to.
var ErrNotAssociable = errors.New("gateway: cannot associate a value coupon to a segment with no flight coupon")

// IssueEMD issues an electronic miscellaneous document.
//
// An EMD-A is stapled to flight coupons and lifted with them, so every value
// coupon has to name a segment that is actually ticketed. An EMD-S is
// standalone and names none. Refusing the mismatch here is what stops a
// document existing that says two contradictory things about itself.
func (g *Gateway) IssueEMD(ctx context.Context, req EMDRequest) (*pnr.PNR, pnr.Ticket, error) {
	var zero pnr.Ticket
	if len(req.AirlineCode) != 3 {
		return nil, zero, fmt.Errorf("gateway: airline code must be three digits, got %q", req.AirlineCode)
	}
	if !req.Type.IsEMD() {
		return nil, zero, fmt.Errorf("gateway: %q is not an EMD type; use A or S", req.Type)
	}
	if len(req.Coupons) == 0 {
		return nil, zero, fmt.Errorf("gateway: an EMD needs at least one value coupon")
	}
	if len(req.Coupons) > pnr.MaxCoupons {
		return nil, zero, fmt.Errorf("gateway: %d coupons exceeds the %d a document carries",
			len(req.Coupons), pnr.MaxCoupons)
	}

	// The ancillary revenue span. RFIC is the revenue category and the amount
	// is what was charged, which together are what the commercial side means
	// when it asks what ancillaries are selling.
	ctx, span := telemetry.Start(ctx, "jetway.emd.issue",
		telemetry.AttrLocator.String(req.Locator),
		telemetry.AttrDocumentType.String(string(req.Type)),
		telemetry.AttrRFIC.String(string(req.RFIC)),
		telemetry.AttrCouponCount.Int(len(req.Coupons)),
	)
	defer span.End()
	if len(req.Coupons) > 0 {
		span.SetAttributes(
			telemetry.AttrRFISC.String(req.Coupons[0].RFISC),
			telemetry.AttrAmount.String(req.Coupons[0].Amount),
			telemetry.AttrCurrency.String(req.Coupons[0].Currency),
		)
	}

	const maxAttempts = 5
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		rec, err := g.Store.GetPNR(ctx, req.Locator)
		if err != nil {
			return nil, zero, err
		}
		if rec.Status == pnr.StatusCancelled {
			return nil, zero, fmt.Errorf("gateway: %s is cancelled", rec.RecordLocator)
		}
		if !hasPassenger(rec, req.PaxRef) {
			return nil, zero, fmt.Errorf("gateway: %s has no passenger %d", rec.RecordLocator, req.PaxRef)
		}

		now := time.Now().UTC()
		number, err := g.nextTicketNumber(ctx, req.AirlineCode)
		if err != nil {
			return nil, zero, err
		}
		doc := pnr.Ticket{
			Number: number, Type: req.Type, RFIC: req.RFIC,
			PaxRef: req.PaxRef, IssuedAt: now, IssuedBy: req.IssuedBy,
		}

		for i, c := range req.Coupons {
			status := pnr.CouponOpen
			if c.ConsumedAtIssuance {
				// Delivered at issuance: there is no later event to close it,
				// so it closes now rather than sitting open forever.
				status = pnr.CouponFlown
			}
			coup := pnr.Coupon{
				Number: i + 1, SegmentRef: c.SegmentRef, Status: status,
				RFISC: c.RFISC, Amount: c.Amount, Currency: c.Currency,
			}
			if req.Type == pnr.DocEMDA {
				assoc, err := flightCouponFor(rec, req.PaxRef, c.SegmentRef)
				if err != nil {
					return nil, zero, err
				}
				coup.Association = assoc
			}
			doc.Coupons = append(doc.Coupons, coup)
		}

		if err := doc.Validate(); err != nil {
			return nil, zero, err
		}

		expected := rec.Version
		rec.Tickets = append(rec.Tickets, doc)
		rec.UpdatedAt = now
		events := []store.Event{{
			Type: "emd_issued", At: now, Actor: req.IssuedBy,
			Detail: fmt.Sprintf("%s %s (%s) over %d coupon(s)",
				req.Type, number, req.RFIC.Meaning(), len(doc.Coupons)),
		}}

		switch err := g.Store.UpdatePNR(ctx, rec, expected, events); {
		case err == nil:
			g.Bus.Publish(EvPNR, g.pnrView(rec))
			// The carrier providing the service has to know the document
			// exists, for the same reason a flight ticket does.
			g.notifyDocument(ctx, rec, doc, req.IssuedBy)
			span.SetAttributes(
				telemetry.AttrRecordID.String(rec.ID),
				telemetry.AttrDocumentNumber.String(doc.Number.String()),
			)
			return rec, doc, nil
		case errors.Is(err, store.ErrConflict):
			lastErr = err
			continue
		default:
			return nil, zero, fmt.Errorf("gateway: persist document: %w", err)
		}
	}
	return nil, zero, fmt.Errorf("gateway: gave up issuing after %d attempts: %w", maxAttempts, lastErr)
}

func hasPassenger(rec *pnr.PNR, ref int) bool {
	for _, p := range rec.Passengers {
		if p.Ref == ref {
			return true
		}
	}
	return false
}

// flightCouponFor finds the flight coupon covering a segment for a passenger,
// which is what an EMD-A value coupon associates to.
func flightCouponFor(rec *pnr.PNR, paxRef, segmentRef int) (pnr.Association, error) {
	for _, t := range rec.FlightTickets() {
		if t.PaxRef != paxRef {
			continue
		}
		for _, c := range t.Coupons {
			if c.SegmentRef == segmentRef {
				return pnr.Association{
					Document: t.Number, Coupon: c.Number, SegmentRef: segmentRef,
				}, nil
			}
		}
	}
	return pnr.Association{}, fmt.Errorf("%w: segment %d for passenger %d",
		ErrNotAssociable, segmentRef, paxRef)
}

// notifyDocument tells the carrier providing the service that a document exists.
func (g *Gateway) notifyDocument(ctx context.Context, rec *pnr.PNR, doc pnr.Ticket, by string) {
	byCarrier := map[string][]padis.CouponRef{}
	for _, c := range doc.Coupons {
		seg := segmentByRef(rec, c.SegmentRef)
		if seg == nil {
			continue
		}
		carrier := seg.OperatingCarrier
		if carrier == "" {
			carrier = seg.Carrier
		}
		byCarrier[carrier] = append(byCarrier[carrier], padis.CouponRef{Number: c.Number, Status: c.Status})
	}
	for carrier, coupons := range byCarrier {
		if err := g.sendTicketControl(ctx, rec, doc, carrier, coupons); err != nil {
			g.Log.Error("could not tell a carrier about a document",
				"locator", rec.RecordLocator, "carrier", carrier,
				"document", doc.Number.String(), "err", err)
			g.queueTicketDivergence(ctx, rec, carrier, doc.Number, err, by)
		}
	}
}

// AssociateEMD links a value coupon to a flight coupon, or breaks the link.
//
// Association is per coupon, not per document, because the reasons for it come
// and go per coupon: a passenger checks in without the excess baggage they paid
// for, and that one coupon needs unstapling while the rest of the document
// stands.
func (g *Gateway) AssociateEMD(ctx context.Context, locator string, number pnr.TicketNumber,
	coupon int, segmentRef int, by string) (*pnr.PNR, error) {
	rec, err := g.Store.GetPNR(ctx, locator)
	if err != nil {
		return nil, err
	}
	idx := -1
	for i := range rec.Tickets {
		if rec.Tickets[i].Number.Compact() == number.Compact() {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil, fmt.Errorf("gateway: %s holds no document %s", locator, number)
	}
	doc := &rec.Tickets[idx]
	if doc.Type != pnr.DocEMDA {
		return nil, fmt.Errorf("gateway: %s is %s; only an EMD-A associates", number, doc.Type)
	}

	var target *pnr.Coupon
	for i := range doc.Coupons {
		if doc.Coupons[i].Number == coupon {
			target = &doc.Coupons[i]
			break
		}
	}
	if target == nil {
		return nil, fmt.Errorf("gateway: %s has no coupon %d", number, coupon)
	}
	if target.Status.Final() {
		return nil, fmt.Errorf("gateway: coupon %d is %s (%s) and cannot be re-stapled",
			coupon, target.Status, target.Status.Meaning())
	}

	now := time.Now().UTC()
	expected := rec.Version
	var detail string
	if segmentRef == 0 {
		if target.Association.IsZero() {
			return rec, nil
		}
		detail = fmt.Sprintf("coupon %d of %s disassociated from %s",
			coupon, number, target.Association)
		target.Association = pnr.Association{}
		target.SegmentRef = 0
	} else {
		assoc, err := flightCouponFor(rec, doc.PaxRef, segmentRef)
		if err != nil {
			return nil, err
		}
		target.Association = assoc
		target.SegmentRef = segmentRef
		detail = fmt.Sprintf("coupon %d of %s associated to %s", coupon, number, assoc)
	}

	rec.UpdatedAt = now
	if err := g.Store.UpdatePNR(ctx, rec, expected, []store.Event{{
		Type: "emd_association", At: now, Actor: by, Detail: detail,
	}}); err != nil {
		return nil, fmt.Errorf("gateway: persist association: %w", err)
	}
	g.Bus.Publish(EvPNR, g.pnrView(rec))
	return rec, nil
}

// liftAssociated moves EMD-A value coupons to the status their flight coupon
// just reached.
//
// This is the whole point of associating them. A value coupon stapled to a
// flight coupon is lifted with it, so a passenger who flies has used the meal
// they paid for, and a document left open behind a flown flight is revenue
// nobody accounts for.
func liftAssociated(rec *pnr.PNR, flightDoc pnr.TicketNumber, flightCoupon int,
	status pnr.CouponStatus, at time.Time, actor string) []store.Event {
	if !status.Final() {
		return nil
	}
	var events []store.Event
	for i := range rec.Tickets {
		doc := &rec.Tickets[i]
		if doc.Type != pnr.DocEMDA {
			continue
		}
		for j := range doc.Coupons {
			c := &doc.Coupons[j]
			if c.Association.Coupon != flightCoupon ||
				c.Association.Document.Compact() != flightDoc.Compact() {
				continue
			}
			if c.Status.Final() {
				continue
			}
			events = append(events, store.Event{
				Type: "coupon_lifted", At: at, Actor: actor,
				Detail: fmt.Sprintf("coupon %d of %s lifted with %s: %s -> %s",
					c.Number, doc.Number, c.Association, c.Status, status),
			})
			c.Status = status
		}
	}
	return events
}
