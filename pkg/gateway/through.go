package gateway

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/adamf/jetway/pkg/dcs"
	"github.com/adamf/jetway/pkg/edifact"
	"github.com/adamf/jetway/pkg/iatci"
	"github.com/adamf/jetway/pkg/padis"
	"github.com/adamf/jetway/pkg/store"
)

// ThroughCheckInHandler is a departure-control system asked to check
// another carrier's connecting passengers in. dcs.Station implements it.
type ThroughCheckInHandler interface {
	ThroughCheckIn(ctx context.Context, req dcs.ThroughRequest) (*dcs.ThroughResult, error)
}

// applyThroughCheckIn answers a DCQCKI: the request is mapped onto the
// station's own terms, the station accepts whom it can, and the DCRCKA
// says seat by seat what happened.
func (g *Gateway) applyThroughCheckIn(ctx context.Context, peer *Peer, msg *store.Message,
	dec *decoded, res *Result) error {
	req := dec.ThroughCheckIn
	msg.Kind = iatci.MsgDCQCKI
	g.trace(msg.ID, "through-check-in", req.Describe())

	var out *iatci.CheckInResponse
	if g.ThroughCheckIn == nil {
		out = &iatci.CheckInResponse{Flight: req.Outbound, Status: "X",
			Errors: []iatci.Error{{Level: "1", Code: iatci.ErrNotSupported, Text: "THROUGH CHECK-IN NOT SUPPORTED HERE"}}}
	} else {
		r, err := g.ThroughCheckIn.ThroughCheckIn(ctx, throughRequestOf(req))
		if err != nil {
			return fmt.Errorf("gateway: through check-in: %w", err)
		}
		out = throughResponseOf(req, r)
	}
	msg.Status = store.StatusApplied
	res.Status = store.StatusApplied
	return g.answerThroughCheckIn(ctx, peer, msg, dec, out)
}

// applyThroughCheckInResponse hears the answer to a request this node made
// and hands it to whoever asked.
func (g *Gateway) applyThroughCheckInResponse(ctx context.Context, peer *Peer, msg *store.Message,
	dec *decoded, res *Result) error {
	msg.Kind = iatci.MsgDCRCKA
	msg.Status = store.StatusApplied
	res.Status = store.StatusApplied
	if g.ThroughCheckInResponses != nil {
		g.ThroughCheckInResponses(ctx, peer, dec.ThroughCheckInResponse)
	}
	return nil
}

func (g *Gateway) answerThroughCheckIn(ctx context.Context, peer *Peer, msg *store.Message, dec *decoded, out *iatci.CheckInResponse) error {
	ref := ""
	if dec.Interchange != nil {
		ref = dec.Interchange.ControlRef()
	}
	// The answer goes back to whoever asked, by the UNB sender of the
	// request: through a switch the link peer is the network, not the
	// carrier, and the recipient is what the switch routes on.
	to := dec.EdifactSender
	if to == "" {
		to = peer.Carrier
	}
	ic, err := iatci.BuildDCRCKA(out, padis.BuildOptions{
		Sender:     edifact.Party{ID: g.Identity.Designator, Qualifier: "ZZ"},
		Recipient:  edifact.Party{ID: to, Qualifier: "ZZ"},
		ControlRef: g.nextControlRef(), MessageRef: "1", Now: g.now(),
	})
	if err != nil {
		return fmt.Errorf("gateway: build through check-in response: %w", err)
	}
	raw, err := ic.Encode(edifact.EncodeOptions{SegmentPerLine: true, Charset: edifact.CharsetUNOA})
	if err != nil {
		return fmt.Errorf("gateway: encode through check-in response: %w", err)
	}
	_, err = g.SendKeyed(ctx, peer, raw, iatci.MsgDCRCKA, "", msg.ID, "unb:"+ref)
	return err
}

// RequestThroughCheckIn asks peer's departure-control system to check
// connecting passengers in on its flight. The answer arrives as a DCRCKA
// and reaches ThroughCheckInResponses.
func (g *Gateway) RequestThroughCheckIn(ctx context.Context, peer *Peer, req *iatci.CheckInRequest) error {
	if req.Requestor == "" {
		req.Requestor = g.Identity.Designator
	}
	// Addressed to the receiving carrier, whatever link carries it there.
	to := peer.Carrier
	if to == "" {
		to = req.Outbound.Carrier
	}
	ic, err := iatci.BuildDCQCKI(req, padis.BuildOptions{
		Sender:     edifact.Party{ID: g.Identity.Designator, Qualifier: "ZZ"},
		Recipient:  edifact.Party{ID: to, Qualifier: "ZZ"},
		ControlRef: g.nextControlRef(), MessageRef: "1", Now: g.now(),
	})
	if err != nil {
		return err
	}
	raw, err := ic.Encode(edifact.EncodeOptions{SegmentPerLine: true, Charset: edifact.CharsetUNOA})
	if err != nil {
		return fmt.Errorf("gateway: encode through check-in request: %w", err)
	}
	_, err = g.SendKeyed(ctx, peer, raw, iatci.MsgDCQCKI, "", "", "")
	return err
}

// throughRequestOf maps the wire onto the station's terms. The flight key is
// the outbound flight as the station files it: carrier and number, the
// departure date as DDMMM, the board point.
func throughRequestOf(req *iatci.CheckInRequest) dcs.ThroughRequest {
	out := dcs.ThroughRequest{
		Requestor: req.Requestor, Station: req.RequestorStation,
		Key: dcs.Key{Flight: req.Outbound.Carrier + req.Outbound.Number, Date: wireDate(req.Outbound.Date), Board: req.Outbound.Board},
		Inbound: dcs.Connection{Flight: req.Inbound.Carrier + req.Inbound.Number, Date: wireDate(req.Inbound.Date),
			Station: req.Inbound.Off, Dest: req.Outbound.Off},
	}
	for _, p := range req.Passengers {
		out.Passengers = append(out.Passengers, dcs.ThroughPassenger{
			Ref: p.Ref, Surname: p.Surname, Given: p.Given, Locator: p.Locator, SeatWant: p.SeatWant,
			BagPieces: p.Pieces, BagWeight: p.Weight, SSRs: p.SSRs,
		})
	}
	return out
}

// throughResponseOf maps the station's answer back onto the wire.
func throughResponseOf(req *iatci.CheckInRequest, r *dcs.ThroughResult) *iatci.CheckInResponse {
	out := &iatci.CheckInResponse{Flight: req.Outbound, Status: "O"}
	if !r.Granted {
		out.Status = "I"
		granted := 0
		for _, o := range r.Outcomes {
			if o.Accepted {
				granted++
			}
		}
		if granted > 0 {
			out.Status = "O" // partly: the passengers say who
		}
	}
	for _, o := range r.Outcomes {
		pr := iatci.Result{Ref: o.Ref, Surname: o.Surname, Given: o.Given, Status: "H",
			Seat: o.Seat, Cabin: o.Cabin, Sequence: o.Sequence, BoardingPass: o.Accepted}
		if !o.Accepted {
			pr.Status = "I"
			pr.Errors = []iatci.Error{{Level: "1", Code: o.Reason, Text: strings.ToUpper(o.Text)}}
		}
		out.Passengers = append(out.Passengers, pr)
	}
	return out
}

func wireDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return strings.ToUpper(t.Format("02Jan"))
}
