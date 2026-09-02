package gateway

import (
	"context"
	"errors"
	"fmt"

	"github.com/adamf/jetway/pkg/avail"
	"github.com/adamf/jetway/pkg/pnr"
	"github.com/adamf/jetway/pkg/store"
	"github.com/adamf/jetway/pkg/telemetry"
)

// AddSegment sells one more segment on a record that already exists: the
// operation behind a rebooking, a reroute, or a passenger adding a leg.
//
// The new segment is decided against availability like any sale -- free
// sale holds it at once, otherwise it is requested -- and the carrier is
// asked for that segment alone. Asking for the whole itinerary again, as
// the first request does, would have the carrier sell the confirmed legs
// twice; RequestFromCarrier exists for a record nobody holds yet.
func (g *Gateway) AddSegment(ctx context.Context, locator string, s BookingSegment, by, reason string) (*pnr.PNR, error) {
	ctx, span := telemetry.Start(ctx, "jetway.add_segment", telemetry.AttrLocator.String(locator))
	defer span.End()

	const maxAttempts = 5
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		rec, err := g.Store.GetPNR(ctx, locator)
		if err != nil {
			return nil, err
		}
		if rec.Status == pnr.StatusCancelled {
			return nil, errors.New("gateway: the record is cancelled; a new leg wants a new booking")
		}
		now := g.now()
		depart, err := pnr.ResolveDate(s.Date, now)
		if err != nil {
			return nil, err
		}
		seats := s.Seats
		if seats <= 0 {
			seats = len(rec.Passengers)
		}
		seg := pnr.Segment{
			Type: pnr.SegmentAir, Carrier: upper(s.Carrier), FlightNum: s.FlightNum,
			Class: upper(s.Class), Depart: depart, WireDate: pnr.FormatDate(depart),
			Board: upper(s.Board), Off: upper(s.Off),
			DepartTime: s.DepartTime, ArriveTime: s.ArriveTime,
			Status: "HN", Seats: seats,
		}
		if seg.Carrier == "" || seg.FlightNum == "" || seg.Board == "" || seg.Off == "" {
			return nil, errors.New("gateway: a segment needs carrier, flight, board and off points")
		}
		for _, have := range rec.Segments {
			if have.Type == pnr.SegmentAir && have.Status != "XX" && have.Key() == seg.Key() {
				return nil, fmt.Errorf("gateway: %s is already on the record", seg.Describe())
			}
		}
		decision, why := g.decide(seg)
		freeSold := false
		switch decision {
		case avail.Refuse:
			return nil, fmt.Errorf("gateway: %s%s %s %s-%s is not available: %s",
				seg.Carrier, seg.FlightNum, seg.Class, seg.Board, seg.Off, why)
		case avail.FreeSale:
			seg.Status = "HK"
			freeSold = true
		}
		expected := rec.Version
		rec.Segments = append(rec.Segments, seg)
		rec.Recompute()
		newRef := rec.Segments[len(rec.Segments)-1].Ref
		rec.UpdatedAt = now
		detail := seg.Describe() + " added" + reasonSuffix(reason)
		events := []store.Event{{Type: "add_segment", Detail: detail, Actor: by, At: now}}

		switch err := g.Store.UpdatePNR(ctx, rec, expected, events); {
		case err == nil:
		case errors.Is(err, store.ErrConflict):
			lastErr = err
			continue
		default:
			return nil, fmt.Errorf("gateway: persist added segment: %w", err)
		}
		if freeSold && g.Avail != nil {
			g.Avail.Sold(availKey(seg), seg.Seats)
		}
		g.Bus.Publish(EvPNR, g.pnrView(rec))
		g.Log.Info("segment added", "locator", locator, "segment", seg.Describe(), "by", by)

		if _, err := g.requestSegments(ctx, rec, seg.Carrier, func(x pnr.Segment) bool { return x.Ref == newRef }); err != nil {
			// The record holds the leg; the carrier does not know yet. That
			// is exactly the divergence the queue exists to surface, and the
			// sweeper will notice the HN that never turns into an answer.
			g.Log.Warn("could not request the added segment from the carrier",
				"locator", locator, "carrier", seg.Carrier, "err", err)
		}
		return rec, nil
	}
	return nil, fmt.Errorf("gateway: add segment: %w", lastErr)
}
