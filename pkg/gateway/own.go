package gateway

import (
	"context"
	"fmt"
	"strings"

	"github.com/adamf/jetway/pkg/airimp"
	"github.com/adamf/jetway/pkg/pnr"
	"github.com/adamf/jetway/pkg/store"
)

// A carrier's own sales.
//
// A node configured with a carrier's identity and a Responder is that
// carrier's reservations system: it answers the requests other systems
// send it. It also sells for itself. The carrier's own website, its call
// centre and its ticket desks book on this node, and a seat they ask for
// is a seat of the node's own inventory. Nothing goes on a wire for it:
// the node decides the segment the way it decides a request from a
// partner, with no link, and writes the answer on the record. A
// cancellation gives the seat back the same way. Segments of other
// carriers on the same record are requested from them as usual.

// Own reports whether a carrier's seats are this node's own to sell: the
// node carries the carrier's designator and has a Responder to decide
// with. A node that only distributes has no Responder and owns nothing.
func (g *Gateway) Own(carrier string) bool {
	return g.Responder != nil && carrier != "" && strings.EqualFold(carrier, g.Identity.Designator)
}

// sellOwn decides the segments of a stored record that are on this
// node's own carrier and await an answer, and writes the answers. only,
// when set, limits it to the segments it admits. It returns the record as
// it now stands, or nil when it had no such segment.
//
// It works on its own copy, read from the store. The record a caller
// holds has been published on the bus by then, and whoever listens there
// reads it while this would be writing.
func (g *Gateway) sellOwn(ctx context.Context, locator, actor string, only func(pnr.Segment) bool) (*pnr.PNR, error) {
	rec, err := g.Store.GetPNR(ctx, locator)
	if err != nil {
		return nil, fmt.Errorf("gateway: read record for own sale: %w", err)
	}
	waiting := false
	for _, s := range rec.Segments {
		if s.Type == pnr.SegmentAir && g.Own(s.Carrier) && s.Status == "HN" && (only == nil || only(s)) {
			waiting = true
		}
	}
	if !waiting {
		return nil, nil
	}
	// The Responder sees the record as it would see a partner's request,
	// narrowed to the segments asked, with no link.
	view := *rec
	if only != nil {
		view.Segments = nil
		for _, s := range rec.Segments {
			if g.Own(s.Carrier) && only(s) {
				view.Segments = append(view.Segments, s)
			}
		}
	}
	outcomes, err := g.Responder.Decide(ctx, &view, nil)
	if err != nil {
		return nil, fmt.Errorf("gateway: decide own sale: %w", err)
	}
	now := g.now()
	var events []store.Event
	for i := range rec.Segments {
		s := &rec.Segments[i]
		if !g.Own(s.Carrier) || s.Status != "HN" || (only != nil && !only(*s)) {
			continue
		}
		code, ok := outcomes[s.Key()]
		if !ok {
			continue
		}
		if h, isReply := airimp.ReplyTo(airimp.ActionCode(code)); isReply && h != "" {
			s.Status = string(h)
		} else {
			s.Status = code
		}
		events = append(events, store.Event{Type: "decided", Detail: s.Describe() + " -> " + code + " (own sale)", Actor: actor, At: now})
	}
	if len(events) == 0 {
		return nil, nil
	}
	expected := rec.Version
	rec.Recompute()
	rec.UpdatedAt = now
	if err := g.Store.UpdatePNR(ctx, rec, expected, events); err != nil {
		return nil, fmt.Errorf("gateway: record own sale: %w", err)
	}
	g.Bus.Publish(EvPNR, g.pnrView(rec))
	return rec, nil
}

// releaseOwn gives back the seats of this node's own carrier that a
// cancellation took from a holding, each with the status it held.
func (g *Gateway) releaseOwn(ctx context.Context, held []pnr.Segment) {
	r, ok := g.Responder.(Releaser)
	if !ok {
		return
	}
	for _, s := range held {
		if g.Own(s.Carrier) && s.Status != "XX" && s.Status != "" {
			r.Release(ctx, s, s.Status)
		}
	}
}
