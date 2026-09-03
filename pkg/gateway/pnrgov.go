package gateway

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/adamf/jetway/pkg/dcs"
	"github.com/adamf/jetway/pkg/edifact"
	"github.com/adamf/jetway/pkg/padis"
	"github.com/adamf/jetway/pkg/pnr"
	"github.com/adamf/jetway/pkg/store"
)

// PNRGOVOptions shape the passenger name record push a flight produces for
// a state.
type PNRGOVOptions struct {
	// Departs and Arrives are the leg's local date and time.
	Departs, Arrives time.Time
	// Station is the pushing office named in ORG; the board point when
	// empty.
	Station string
	// Limit caps the records gathered; zero means the schedule scan limit.
	Limit int
}

// PNRGOVFor gathers the push for a flight under departure control: every
// record this node holds with a segment on the flight, and for each
// traveller departure control has accepted, the seat, sequence and bags.
// A flight before check-in pushes the reservations alone, which is what
// the earlier pushes of a state's schedule carry; the one at the door
// carries the check-in group too.
func (g *Gateway) PNRGOVFor(ctx context.Context, fl *dcs.Flight, opts PNRGOVOptions) (*padis.GovPush, error) {
	if fl == nil {
		return nil, fmt.Errorf("gateway: no flight to push")
	}
	carrier := fl.Carrier
	if carrier == "" && len(fl.Flight) >= 2 {
		carrier = fl.Flight[:2]
	}
	number := strings.TrimPrefix(fl.Flight, carrier)
	limit := opts.Limit
	if limit <= 0 {
		limit = g.ScheduleScanLimit
	}
	if limit <= 0 {
		limit = defaultScheduleScanLimit
	}
	recs, err := g.Store.FindPNRsByFlight(ctx, carrier+strings.TrimLeft(number, "0"), fl.Date, limit)
	if err != nil {
		return nil, fmt.Errorf("gateway: gather a flight's records: %w", err)
	}
	station := opts.Station
	if station == "" {
		station = fl.Board
	}
	push := &padis.GovPush{
		Sender: g.Identity.Designator, Station: station,
		Flight: padis.GovFlight{Carrier: carrier, Number: number, Board: fl.Board, Off: fl.Dest,
			Departs: opts.Departs, Arrives: opts.Arrives},
	}
	// Departure control's passengers by locator and surname, for the
	// check-in group: the record is the reservation's view, the flight's
	// list is the door's.
	accepted := map[string][]*dcs.Passenger{}
	for _, p := range fl.Passengers {
		if p.Status != dcs.StatusAccepted && p.Status != dcs.StatusBoarded {
			continue
		}
		k := p.Locator + "|" + strings.ToUpper(p.Surname)
		accepted[k] = append(accepted[k], p)
	}
	for _, rec := range recs {
		// The store matches on flight and date; a multi-leg flight number
		// is several departures, and the push is one of them.
		if len(rec.Passengers) == 0 || !pnrOnFlight(rec, carrier, number, fl.Board) {
			continue
		}
		gr := padis.GovRecord{PNR: rec}
		for _, pax := range rec.Passengers {
			k := rec.RecordLocator + "|" + strings.ToUpper(pax.Surname)
			list := accepted[k]
			if len(list) == 0 {
				continue
			}
			p := list[0]
			accepted[k] = list[1:]
			ci := padis.GovCheckIn{PaxRef: pax.Ref, Station: fl.Board, Sequence: p.Sequence, Seat: p.Seat, Cabin: p.Compartment}
			if ci.Cabin == "" {
				ci.Cabin = "Y"
			}
			for i, b := range p.Bags {
				if b.Offloaded {
					continue
				}
				dest := p.Dest
				if dest == "" {
					dest = fl.Dest
				}
				ci.Bags = append(ci.Bags, padis.GovBag{Tag: b.Tag, Piece: i + 1, Destination: dest})
				ci.BagWeightKg += b.Weight
			}
			gr.CheckIn = append(gr.CheckIn, ci)
		}
		push.Records = append(push.Records, gr)
	}
	return push, nil
}

// SendPNRGOV transmits a push to a state's peer over EDIFACT. The message
// reference is the guide's common access reference -- push time, carrier
// and flight -- so the state can tie a resend to its first attempt.
func (g *Gateway) SendPNRGOV(ctx context.Context, peer *Peer, push *padis.GovPush) error {
	to := peer.Carrier
	if to == "" {
		to = peer.Name
	}
	now := g.now()
	ic, err := padis.BuildPNRGOV(push, padis.BuildOptions{
		Sender: edifact.Party{ID: g.Identity.Designator}, Recipient: edifact.Party{ID: to},
		ControlRef: g.nextControlRef(), Now: now, MessageRef: padis.CommonAccessRef(push.Flight, now),
	})
	if err != nil {
		return fmt.Errorf("gateway: build PNRGOV: %w", err)
	}
	raw, err := ic.Encode(edifact.EncodeOptions{SegmentPerLine: true, Charset: edifact.CharsetUNOA})
	if err != nil {
		return fmt.Errorf("gateway: encode PNRGOV: %w", err)
	}
	_, err = g.SendKeyed(ctx, peer, raw, padis.MsgPNRGOV, "", "", "")
	return err
}

// applyPNRGOV is the state's side: a carrier pushed a flight's records.
// The records are not applied to this node's store -- a state's copy of a
// carrier's reservation is not a reservation -- they are handed to whoever
// runs the node as an agency.
func (g *Gateway) applyPNRGOV(ctx context.Context, peer *Peer, msg *store.Message, dec *decoded, res *Result) error {
	msg.Kind = padis.MsgPNRGOV
	msg.Status = store.StatusApplied
	res.Status = store.StatusApplied
	g.trace(msg.ID, "pnrgov", dec.PNRGOV.Describe())
	if dec.PNRGOV.Count != len(dec.PNRGOV.Records) {
		g.Log.Warn("a PNR push counted more records than it carried",
			"flight", dec.PNRGOV.Flight.Carrier+dec.PNRGOV.Flight.Number, "eqn", dec.PNRGOV.Count, "records", len(dec.PNRGOV.Records))
	}
	if g.PNRGOV != nil {
		g.PNRGOV(ctx, peer, dec.PNRGOV)
	}
	return nil
}

// pnrOnFlight reports whether a record holds a live segment on the flight
// named by carrier, number and board point.
func pnrOnFlight(rec *pnr.PNR, carrier, number, board string) bool {
	for _, s := range rec.Segments {
		if s.Type != pnr.SegmentAir || s.Carrier != carrier && s.OperatingCarrier != carrier {
			continue
		}
		if strings.TrimLeft(s.FlightNum, "0") != strings.TrimLeft(number, "0") {
			continue
		}
		if board != "" && s.Board != board {
			continue
		}
		return true
	}
	return false
}
