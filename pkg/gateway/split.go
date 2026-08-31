package gateway

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/adamf/jetway/pkg/edifact"
	"github.com/adamf/jetway/pkg/padis"
	"github.com/adamf/jetway/pkg/pnr"
	"github.com/adamf/jetway/pkg/store"
	"github.com/adamf/jetway/pkg/telemetry"
)

// SplitRequest asks for passengers to be divided onto their own record.
type SplitRequest struct {
	Locator string
	// Passengers names who moves, by their reference on the parent.
	Passengers []int
	By         string
	Reason     string
}

// SplitResult is what a division produced.
type SplitResult struct {
	Parent *pnr.PNR
	Child  *pnr.PNR
	// Advised lists carriers told about the division.
	Advised []string
	// Unadvised lists carriers that still hold a single record covering both
	// halves. It is not an error; it is the state of the world until the
	// teletype divide message exists.
	Unadvised []string
}

// Split divides passengers onto a new record.
//
// # Why both records keep the same carrier locators
//
// A carrier holds one booking. Dividing it here does not divide it there, so
// until the carrier splits too, both of our records refer to the same record of
// theirs. Giving the child no carrier reference would be tidier and false: an
// agent ringing the carrier about the child would find nothing.
//
// # Why the child is written first
//
// There is no transaction across two records. Whichever write happens first,
// the other can fail. Writing the child first means a failure leaves the
// passengers on both records -- visible, wrong, and recoverable. Writing the
// parent first would mean a failure loses them from one record without putting
// them on another, and nothing recovers that.
func (g *Gateway) Split(ctx context.Context, req SplitRequest) (*SplitResult, error) {
	ctx, span := telemetry.Start(ctx, "jetway.split",
		telemetry.AttrLocator.String(req.Locator))
	defer span.End()

	if len(req.Passengers) == 0 {
		return nil, fmt.Errorf("gateway: a split needs at least one passenger")
	}

	// The same retry Cancel uses, and for the same reason: a carrier reply
	// landing between the read and the write is ordinary traffic, not an
	// error. What is different here is that the write touches two records, so
	// it goes through DividePNR and either both land or neither does. Creating
	// the child and then failing to update the parent left both records
	// holding the same passengers -- a torn booking that could not be advised
	// to anyone coherently.
	const maxAttempts = 5
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		parent, err := g.Store.GetPNR(ctx, req.Locator)
		if err != nil {
			return nil, err
		}
		if parent.Status == pnr.StatusCancelled {
			return nil, fmt.Errorf("gateway: %s is cancelled", parent.RecordLocator)
		}
		expected := parent.Version

		locator, err := g.newLocator(ctx)
		if err != nil {
			return nil, err
		}
		child, err := parent.Divide(req.Passengers, locator)
		if err != nil {
			return nil, fmt.Errorf("gateway: %w", err)
		}
		// Both halves point at the carrier's single record until it splits too.
		child.Locators = append(child.Locators, parent.Locators...)

		now := time.Now().UTC()
		child.CreatedAt, child.UpdatedAt = now, now
		parent.UpdatedAt = now

		moved := make([]string, 0, len(child.Passengers))
		for _, p := range child.Passengers {
			moved = append(moved, p.Surname+"/"+p.Given)
		}
		sort.Strings(moved)
		detail := fmt.Sprintf("%s divided from %s: %v", child.RecordLocator, parent.RecordLocator, moved)
		if req.Reason != "" {
			detail += " (" + req.Reason + ")"
		}
		ev := []store.Event{{Type: "split", At: now, Actor: req.By, Detail: detail}}
		childEv := []store.Event{{Type: "split_created", At: now, Actor: req.By, Detail: detail}}

		switch err := g.Store.DividePNR(ctx, parent, expected, child, ev, childEv); {
		case err == nil:
		case errors.Is(err, store.ErrConflict):
			// Nothing was written, so the next attempt starts from a clean
			// read. The locator allocated for this attempt is simply not used.
			lastErr = err
			continue
		default:
			return nil, fmt.Errorf("gateway: record the division: %w", err)
		}

		g.Bus.Publish(EvPNR, g.pnrView(parent))
		g.Bus.Publish(EvPNR, g.pnrView(child))

		res := &SplitResult{Parent: parent, Child: child}
		res.Advised, res.Unadvised = g.adviseSplit(ctx, parent, child, req.By)
		span.SetAttributes(
			telemetry.AttrRecordID.String(child.ID),
			telemetry.AttrPaxCount.Int(len(child.Passengers)),
			telemetry.AttrNotified.StringSlice(res.Advised),
			telemetry.AttrUnreachable.StringSlice(res.Unadvised),
			telemetry.AttrDivergence.Bool(len(res.Unadvised) > 0),
		)
		return res, nil
	}
	return nil, fmt.Errorf("gateway: gave up dividing %s after %d attempts: %w",
		req.Locator, maxAttempts, lastErr)
}

// adviseSplit tells the carriers it can, and records the ones it cannot.
//
// The EDIFACT half is buildable and is built. The teletype half is not: the
// divide message is in AIRIMP, which is paid and unbought, and the two
// available substitutes are both wrong -- selling the child again would
// double-book, and cancelling then reselling risks losing the seats. So a
// teletype partner is left holding one record and the gap is named rather than
// guessed at.
func (g *Gateway) adviseSplit(ctx context.Context, parent, child *pnr.PNR, by string) (advised, unadvised []string) {
	carriers := map[string]bool{}
	for _, s := range child.Segments {
		if s.Type == pnr.SegmentAir && s.Status != "XX" {
			carriers[s.Carrier] = true
		}
	}
	names := make([]string, 0, len(carriers))
	for c := range carriers {
		names = append(names, c)
	}
	sort.Strings(names)

	for _, carrier := range names {
		err := g.sendDivide(ctx, parent, child, carrier)
		if err == nil {
			advised = append(advised, carrier)
			continue
		}
		unadvised = append(unadvised, carrier)
		g.Log.Warn("a carrier was not told about a division",
			"parent", parent.RecordLocator, "child", child.RecordLocator,
			"carrier", carrier, "err", err)
		if g.Queues == nil {
			continue
		}
		if _, qerr := g.Queues.Place(ctx, &store.QueueItem{
			Queue: store.QueueDivergence, PNRID: child.ID, Locator: child.RecordLocator,
			Code: "split_not_advised_" + carrier,
			Reason: fmt.Sprintf(
				"%s still holds one record covering both %s and %s: %v",
				carrier, parent.RecordLocator, child.RecordLocator, err),
			PlacedBy: by,
		}); qerr != nil {
			g.Log.Error("could not queue an unadvised division",
				"locator", child.RecordLocator, "err", qerr)
		}
	}
	return advised, unadvised
}

// sendDivide advises one carrier that a booking has been divided.
func (g *Gateway) sendDivide(ctx context.Context, parent, child *pnr.PNR, carrier string) error {
	peer := g.PeerForCarrier(carrier)
	if peer == nil {
		return fmt.Errorf("no link configured for carrier %q", carrier)
	}
	if peer.Format != store.FormatEDIFACT {
		return fmt.Errorf("peer %s is a teletype link and the AIRIMP divide message is not implemented", peer.Name)
	}
	ref := nextControlRef()
	ic, err := padis.BuildDivide(parent, child, carrier, padis.BuildOptions{
		Sender:     edifact.Party{ID: g.Identity.Designator, Qualifier: "ZZ"},
		Recipient:  edifact.Party{ID: carrier, Qualifier: "ZZ"},
		ControlRef: ref, MessageRef: "1",
		Charset: edifact.CharsetUNOA,
	})
	if err != nil {
		return err
	}
	raw, err := ic.Encode(edifact.EncodeOptions{SegmentPerLine: true, Charset: edifact.CharsetUNOA})
	if err != nil {
		return err
	}
	_, err = g.SendKeyed(ctx, peer, raw, "PADIS/divide", child.ID, "", "unb:"+ref)
	return err
}

// ErrNotDivided is returned when a locator names no divided record.
var ErrNotDivided = errors.New("gateway: record is not part of a division")
