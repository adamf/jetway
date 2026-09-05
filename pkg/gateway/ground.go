package gateway

import (
	"context"
	"fmt"

	"github.com/adamf/jetway/pkg/acars"
	"github.com/adamf/jetway/pkg/aftn"
	"github.com/adamf/jetway/pkg/atfm"
	"github.com/adamf/jetway/pkg/ats"
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
	// Datalink is an aircraft's OOOI report, forwarded by the datalink
	// service provider: what operations turns into a movement message.
	Datalink(ctx context.Context, m *acars.Message, origin typeb.Address) error
	// ATS is an air traffic services message -- a flight plan, a departure,
	// an arrival -- with the AFTN envelope it arrived in.
	ATS(ctx context.Context, m *ats.Message, envelope *aftn.Message) error
}

// ATFMReceiver is the optional half of Ground an operations centre adds to
// hear flow management: a slot allocated to one of its flights, revised,
// or cancelled, with the AFTN envelope it came in. A Ground that does not
// implement it has the messages filed and nothing more.
type ATFMReceiver interface {
	ATFM(ctx context.Context, m *atfm.Message, envelope *aftn.Message) error
}

// GroundFuncs adapts three functions to Ground, for embedders that only care
// about some of the traffic. A nil function files the message and does
// nothing, which is the gateway's default.
type GroundFuncs struct {
	OnNameList  func(ctx context.Context, m *pnl.Message, origin typeb.Address) error
	OnBaggage   func(ctx context.Context, m *baggage.Message, origin typeb.Address) error
	OnDeparture func(ctx context.Context, m *dcs.Message, origin typeb.Address) error
	OnDatalink  func(ctx context.Context, m *acars.Message, origin typeb.Address) error
	OnATS       func(ctx context.Context, m *ats.Message, envelope *aftn.Message) error
	OnATFM      func(ctx context.Context, m *atfm.Message, envelope *aftn.Message) error
}

// ATFM implements ATFMReceiver.
func (g GroundFuncs) ATFM(ctx context.Context, m *atfm.Message, envelope *aftn.Message) error {
	if g.OnATFM == nil {
		return nil
	}
	return g.OnATFM(ctx, m, envelope)
}

// Datalink implements Ground.
func (g GroundFuncs) Datalink(ctx context.Context, m *acars.Message, origin typeb.Address) error {
	if g.OnDatalink == nil {
		return nil
	}
	return g.OnDatalink(ctx, m, origin)
}

// ATS implements Ground.
func (g GroundFuncs) ATS(ctx context.Context, m *ats.Message, envelope *aftn.Message) error {
	if g.OnATS == nil {
		return nil
	}
	return g.OnATS(ctx, m, envelope)
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
