package gateway

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/adamf/jetway/pkg/airimp"
	"github.com/adamf/jetway/pkg/edifact"
	"github.com/adamf/jetway/pkg/padis"
	"github.com/adamf/jetway/pkg/pnr"
	"github.com/adamf/jetway/pkg/store"
	"github.com/adamf/jetway/pkg/telemetry"
	"github.com/adamf/jetway/pkg/typeb"
)

// CancelOptions controls a cancellation.
type CancelOptions struct {
	// Segments names the segments to cancel by record position. Nil cancels
	// every live segment, which is what cancelling a booking means.
	Segments []int
	By       string
	Reason   string
}

// CancelResult reports what a cancellation actually achieved.
//
// Notified and Unreachable are separate because they are different facts and
// the difference matters: a carrier we could not tell is still holding seats
// for a booking this node now shows as cancelled.
type CancelResult struct {
	PNR         *pnr.PNR
	Notified    []string
	Unreachable []string
}

// ErrNothingToCancel is returned when a record has no live segment.
var ErrNothingToCancel = errors.New("gateway: record has no live segment to cancel")

// Cancel withdraws segments and tells the carriers holding them.
//
// The order is deliberate and matches the reply path: the decision is recorded
// before it is sent, so this node's state and the message it sends can never
// disagree. The failure that leaves behind is visible rather than silent -- a
// carrier that could not be told lands on the divergence queue, because the
// booking now looks settled on this side and is not.
//
// This is the operation whose absence blocked three separate things: NDC order
// cancellation, auto-cancel on a ticketing time limit, and cancelling from the
// console. None of them could be built while there was no way to say "off" to
// a carrier.
func (g *Gateway) Cancel(ctx context.Context, locator string, opts CancelOptions) (*CancelResult, error) {
	ctx, span := telemetry.Start(ctx, "jetway.cancel",
		telemetry.AttrLocator.String(locator))
	defer span.End()

	const maxAttempts = 5
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		rec, err := g.Store.GetPNR(ctx, locator)
		if err != nil {
			return nil, err
		}

		want := map[int]bool{}
		for _, r := range opts.Segments {
			want[r] = true
		}
		carriers := map[string][]int{}
		expected := rec.Version
		now := g.now()
		var events []store.Event

		// Two passes over the segments, and the order is the point. The
		// builders skip anything already at XX -- correctly, since telling a
		// carrier to drop a holding they no longer have is its own kind of
		// wrong -- so the messages have to be built while the segments are
		// still live, and the record marked afterwards.
		var doomed []*pnr.Segment
		for i := range rec.Segments {
			s := &rec.Segments[i]
			if s.Type != pnr.SegmentAir || s.Status == "XX" {
				continue
			}
			if len(want) > 0 && !want[s.Ref] {
				continue
			}
			carriers[s.Carrier] = append(carriers[s.Carrier], s.Ref)
			doomed = append(doomed, s)
		}
		if len(doomed) == 0 {
			return nil, ErrNothingToCancel
		}

		outbound, buildErrs := g.buildCancels(rec, carriers)

		for _, s := range doomed {
			events = append(events, store.Event{
				Type: "segment_cancelled", At: now, Actor: opts.By,
				Detail: s.Describe() + " cancelled" + reasonSuffix(opts.Reason),
			})
			s.Status = "XX"
		}

		if !hasLiveSegment(rec) {
			rec.Status = pnr.StatusCancelled
			events = append(events, store.Event{
				Type: "record_cancelled", At: now, Actor: opts.By,
				Detail: "no live segment remains" + reasonSuffix(opts.Reason),
			})
		}
		rec.UpdatedAt = now

		switch err := g.Store.UpdatePNR(ctx, rec, expected, events); {
		case err == nil:
		case errors.Is(err, store.ErrConflict):
			lastErr = err
			continue
		default:
			return nil, fmt.Errorf("gateway: persist cancellation: %w", err)
		}

		g.Bus.Publish(EvPNR, g.pnrView(rec))
		res := g.notifyCancel(ctx, rec, outbound, buildErrs, opts)
		// Unreachable is the number worth alerting on: it is how often this
		// node and a partner end up disagreeing about what is held.
		span.SetAttributes(
			telemetry.AttrRecordID.String(rec.ID),
			telemetry.AttrNotified.StringSlice(res.Notified),
			telemetry.AttrUnreachable.StringSlice(res.Unreachable),
			telemetry.AttrDivergence.Bool(len(res.Unreachable) > 0),
		)
		return res, nil
	}
	return nil, fmt.Errorf("gateway: gave up cancelling after %d attempts: %w", maxAttempts, lastErr)
}

func reasonSuffix(reason string) string {
	if reason == "" {
		return ""
	}
	return ": " + reason
}

func hasLiveSegment(rec *pnr.PNR) bool {
	for _, s := range rec.Segments {
		if s.Type == pnr.SegmentAir && s.Status != "XX" {
			return true
		}
	}
	return false
}

// cancelMessage is a built, unsent cancellation for one carrier.
type cancelMessage struct {
	peer *Peer
	raw  []byte
	kind string
	key  string
}

// buildCancels renders one cancellation per carrier from the live record.
func (g *Gateway) buildCancels(rec *pnr.PNR, carriers map[string][]int) (map[string]*cancelMessage, map[string]error) {
	out := map[string]*cancelMessage{}
	errs := map[string]error{}
	for carrier, refs := range carriers {
		m, err := g.buildCancel(rec, carrier, refs)
		if err != nil {
			errs[carrier] = err
			continue
		}
		out[carrier] = m
	}
	return out, errs
}

// notifyCancel sends the cancellations that were built, and records the
// carriers that could not be told.
func (g *Gateway) notifyCancel(ctx context.Context, rec *pnr.PNR,
	outbound map[string]*cancelMessage, buildErrs map[string]error, opts CancelOptions) *CancelResult {
	res := &CancelResult{PNR: rec}

	names := make([]string, 0, len(outbound)+len(buildErrs))
	for c := range outbound {
		names = append(names, c)
	}
	for c := range buildErrs {
		names = append(names, c)
	}
	sort.Strings(names)

	for _, carrier := range names {
		err := buildErrs[carrier]
		if err == nil {
			m := outbound[carrier]
			_, err = g.SendKeyed(ctx, m.peer, m.raw, m.kind, rec.ID, "", m.key)
		}
		if err != nil {
			res.Unreachable = append(res.Unreachable, carrier)
			g.Log.Error("could not tell a carrier about a cancellation",
				"locator", rec.RecordLocator, "carrier", carrier, "err", err)
			g.queueCancelDivergence(ctx, rec, carrier, err, opts.By)
			continue
		}
		res.Notified = append(res.Notified, carrier)
	}
	return res
}

func (g *Gateway) buildCancel(rec *pnr.PNR, carrier string, refs []int) (*cancelMessage, error) {
	peer := g.PeerForCarrier(carrier)
	if peer == nil {
		return nil, fmt.Errorf("no link configured for carrier %q", carrier)
	}
	switch peer.Format {
	case store.FormatEDIFACT:
		ref := g.nextControlRef()
		ic, err := padis.BuildCancel(rec, carrier, refs, padis.BuildOptions{
			Sender:     edifact.Party{ID: g.Identity.Designator, Qualifier: "ZZ"},
			Recipient:  edifact.Party{ID: carrier, Qualifier: "ZZ"},
			ControlRef: ref, MessageRef: "1",
			Charset: edifact.CharsetUNOA,
		})
		if err != nil {
			return nil, err
		}
		raw, err := ic.Encode(edifact.EncodeOptions{SegmentPerLine: true, Charset: edifact.CharsetUNOA})
		if err != nil {
			return nil, err
		}
		return &cancelMessage{peer: peer, raw: raw, kind: "PADIS/cancel", key: "unb:" + ref}, nil

	default:
		text := airimp.BuildCancel(rec, carrier, refs)
		if text == "" {
			return nil, fmt.Errorf("nothing to cancel with %s", carrier)
		}
		dest, err := typeb.ParseAddress(peer.TTYAddress)
		if err != nil {
			return nil, fmt.Errorf("peer %s has no usable teletype address: %w", peer.Name, err)
		}
		out := &typeb.Message{
			Priority:     "QU",
			Destinations: []typeb.Address{dest},
			Origin:       mustAddress(g.Identity.TTYAddress),
			OriginTime:   g.nowOriginTime(),
			Text:         text,
		}
		raw, err := out.Encode(typeb.EncodeOptions{Charset: typeb.CharsetITA2, CRLF: true})
		if err != nil {
			return nil, err
		}
		return &cancelMessage{peer: peer, raw: raw, kind: "AIRIMP/cancel"}, nil
	}
}

// queueCancelDivergence records that this node and a carrier now disagree.
func (g *Gateway) queueCancelDivergence(ctx context.Context, rec *pnr.PNR, carrier string, cause error, by string) {
	if g.Queues == nil {
		return
	}
	if _, err := g.Queues.Place(ctx, &store.QueueItem{
		Queue: store.QueueDivergence, PNRID: rec.ID, Locator: rec.RecordLocator,
		Code:     "cancel_not_sent_" + carrier,
		Reason:   fmt.Sprintf("%s was not told this booking is cancelled and may still hold the seats: %v", carrier, cause),
		PlacedBy: by,
	}); err != nil {
		g.Log.Error("could not queue an unsent cancellation",
			"locator", rec.RecordLocator, "carrier", carrier, "err", err)
	}
}

// CancelExpired cancels a booking whose ticketing time limit has passed.
//
// It satisfies queue.Canceller. The unreachable carriers are returned rather
// than swallowed: a sweeper that reported success while a carrier still held
// the seats would be quietly manufacturing the divergence this whole path
// exists to avoid.
func (g *Gateway) CancelExpired(ctx context.Context, locator, reason string) ([]string, error) {
	res, err := g.Cancel(ctx, locator, CancelOptions{By: "sweeper", Reason: reason})
	if err != nil {
		if errors.Is(err, ErrNothingToCancel) {
			return nil, nil
		}
		return nil, err
	}
	return res.Unreachable, nil
}
