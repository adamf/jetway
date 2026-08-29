package gateway

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/adamf/jetway/internal/store"
	"github.com/adamf/jetway/pkg/pnr"
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
	// Unadvised lists carriers that still hold a single record covering both
	// halves. It is not an error; it is the state of the world until they are
	// told.
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
	if len(req.Passengers) == 0 {
		return nil, fmt.Errorf("gateway: a split needs at least one passenger")
	}
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

	if err := g.Store.CreatePNR(ctx, child, []store.Event{{
		Type: "split_created", At: now, Actor: req.By, Detail: detail,
	}}); err != nil {
		return nil, fmt.Errorf("gateway: create the divided record: %w", err)
	}

	if err := g.Store.UpdatePNR(ctx, parent, expected, []store.Event{{
		Type: "split", At: now, Actor: req.By, Detail: detail,
	}}); err != nil {
		// The child exists and holds the passengers. Both records list them
		// now, which is wrong and recoverable; losing them would not be.
		g.Log.Error("a division left both records holding the same passengers",
			"parent", parent.RecordLocator, "child", child.RecordLocator, "err", err)
		g.queueSplitDivergence(ctx, child, parent.RecordLocator, req.By, err)
		return nil, fmt.Errorf("gateway: %s was created but %s was not updated: %w",
			child.RecordLocator, parent.RecordLocator, err)
	}

	g.Bus.Publish(EvPNR, g.pnrView(parent))
	g.Bus.Publish(EvPNR, g.pnrView(child))

	res := &SplitResult{Parent: parent, Child: child}
	res.Unadvised = g.noteUnadvisedSplit(ctx, parent, child, req.By)
	return res, nil
}

// noteUnadvisedSplit records that each carrier still holds one record.
//
// Advising a carrier of a division needs the divide message, which is not
// implemented: the message exists in AIRIMP and its shape is in a manual this
// build does not have. Selling the child again would double-book, and
// cancelling and reselling risks losing the seats altogether, so nothing is
// sent and the gap is made visible instead of guessed at.
func (g *Gateway) noteUnadvisedSplit(ctx context.Context, parent, child *pnr.PNR, by string) []string {
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

	if g.Queues == nil {
		return names
	}
	for _, carrier := range names {
		if _, err := g.Queues.Place(ctx, &store.QueueItem{
			Queue: store.QueueDivergence, PNRID: child.ID, Locator: child.RecordLocator,
			Code: "split_not_advised_" + carrier,
			Reason: fmt.Sprintf(
				"%s still holds one record covering both %s and %s; the division has not been advised",
				carrier, parent.RecordLocator, child.RecordLocator),
			PlacedBy: by,
		}); err != nil {
			g.Log.Error("could not queue an unadvised division",
				"locator", child.RecordLocator, "err", err)
		}
	}
	return names
}

func (g *Gateway) queueSplitDivergence(ctx context.Context, child *pnr.PNR, parent, by string, cause error) {
	if g.Queues == nil {
		return
	}
	if _, err := g.Queues.Place(ctx, &store.QueueItem{
		Queue: store.QueueDivergence, PNRID: child.ID, Locator: child.RecordLocator,
		Code:     "split_incomplete",
		Reason:   fmt.Sprintf("%s was created but %s still lists the same passengers: %v", child.RecordLocator, parent, cause),
		PlacedBy: by,
	}); err != nil {
		g.Log.Error("could not queue an incomplete division", "err", err)
	}
}

// ErrNotDivided is returned when a locator names no divided record.
var ErrNotDivided = errors.New("gateway: record is not part of a division")
