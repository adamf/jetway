package gateway

import (
	"context"
	"fmt"

	"github.com/adamf/jetway/pkg/baggage"
	"github.com/adamf/jetway/pkg/dcs"
	"github.com/adamf/jetway/pkg/pnl"
	"github.com/adamf/jetway/pkg/store"
	"github.com/adamf/jetway/pkg/typeb"
)

// Ground is the seam where a departure control system, a sortation system,
// or an arrival station plugs in.
//
// The gateway files name lists, bag messages and departure control messages
// as applied whether or not anything consumes them -- they touch no booking,
// and that was the whole of the older behaviour. A node with a Ground is
// handed each one after capture, with the address it came from. A carrier's
// node points this at its pkg/dcs Station; a distribution system leaves it
// nil.
//
// An error from a consumer is an application refusal -- a PNL after
// acceptance began, a bag report for a flight nobody opened -- and is
// recorded against the message as rejected, with the bytes intact. It is
// never a reason to fail the ingest: the message is already durable, and the
// refusal is exactly what an operator wants to find in the ledger.
type Ground interface {
	NameList(ctx context.Context, m *pnl.Message, origin typeb.Address) error
	Baggage(ctx context.Context, m *baggage.Message, origin typeb.Address) error
	Departure(ctx context.Context, m *dcs.Message, origin typeb.Address) error
}

// GroundFuncs adapts three functions to Ground, for embedders that only care
// about some of the traffic. A nil function files the message and does
// nothing, which is the gateway's default.
type GroundFuncs struct {
	OnNameList  func(ctx context.Context, m *pnl.Message, origin typeb.Address) error
	OnBaggage   func(ctx context.Context, m *baggage.Message, origin typeb.Address) error
	OnDeparture func(ctx context.Context, m *dcs.Message, origin typeb.Address) error
}

// NameList implements Ground.
func (g GroundFuncs) NameList(ctx context.Context, m *pnl.Message, origin typeb.Address) error {
	if g.OnNameList == nil {
		return nil
	}
	return g.OnNameList(ctx, m, origin)
}

// Baggage implements Ground.
func (g GroundFuncs) Baggage(ctx context.Context, m *baggage.Message, origin typeb.Address) error {
	if g.OnBaggage == nil {
		return nil
	}
	return g.OnBaggage(ctx, m, origin)
}

// Departure implements Ground.
func (g GroundFuncs) Departure(ctx context.Context, m *dcs.Message, origin typeb.Address) error {
	if g.OnDeparture == nil {
		return nil
	}
	return g.OnDeparture(ctx, m, origin)
}

// toGround files a ground message as applied and hands it to the consumer,
// if there is one. A refusal marks the message rejected with the reason.
func (g *Gateway) toGround(ctx context.Context, msg *store.Message, res *Result, what string, hand func() error) error {
	msg.Status = store.StatusApplied
	res.Status = store.StatusApplied
	g.trace(msg.ID, what, msg.Kind)
	if g.Ground == nil || hand == nil {
		return nil
	}
	if err := hand(); err != nil {
		msg.Status = store.StatusRejected
		msg.Error = err.Error()
		res.Status = store.StatusRejected
		g.trace(msg.ID, what, "refused: "+err.Error())
		g.Log.Warn("ground refused a message", "id", msg.ID, "kind", msg.Kind, "err", err)
		return nil
	}
	g.trace(msg.ID, what, fmt.Sprintf("handed to %T", g.Ground))
	return nil
}
