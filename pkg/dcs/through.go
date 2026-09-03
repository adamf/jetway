package dcs

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ThroughRequest is another carrier's departure-control system asking this
// station to check its connecting passengers in on one of our flights: the
// inter-airline through check-in the IATCI dialogue carries. The types are
// this package's own so the station knows nothing of the wire; the gateway
// maps the EDIFACT onto them.
type ThroughRequest struct {
	// Requestor is the carrier asking and the station it asks from.
	Requestor string
	Station   string
	// Key names our flight; Inbound is the flight the passengers arrive on.
	Key     Key
	Inbound Connection
	// Passengers are the party, each matched onto the name list by locator
	// and surname the way an agent would.
	Passengers []ThroughPassenger
}

// ThroughPassenger is one passenger in a through check-in request.
type ThroughPassenger struct {
	Ref      string // the requestor's reference, echoed back
	Surname  string
	Given    string
	Locator  string
	SeatWant string
	// Bags are the pieces the delivering station has already tagged through
	// to our destination, with their total weight; we accept them as
	// connecting bags rather than issuing new tags.
	BagPieces int
	BagWeight int
	SSRs      []string
}

// ThroughOutcome is what happened to one passenger.
type ThroughOutcome struct {
	Ref      string
	Surname  string
	Given    string
	Accepted bool
	Seat     string
	Cabin    string
	Sequence int
	// Reason is the refusal, one of the ThroughRefused* codes, with a text.
	Reason string
	Text   string
}

// ThroughResult is the station's answer.
type ThroughResult struct {
	Flight   *Flight
	Outcomes []ThroughOutcome
	// Granted is whether every passenger was accepted.
	Granted bool
}

// Refusal codes, shared with the IATCI error vocabulary (element 9845) so
// the gateway can put them on the wire unchanged.
const (
	ThroughRefusedNotFound  = "1"
	ThroughRefusedSeat      = "2"
	ThroughRefusedFlight    = "5"
	ThroughRefusedCancelled = "15"
	ThroughRefusedClosed    = "35"
	ThroughRefusedFull      = "29"
	ThroughRefusedAccepted  = "6" // already accepted: too many passengers of that name
)

// ThroughCheckIn accepts another carrier's connecting passengers on our
// flight. Each passenger is matched by locator and surname, seated (the
// requested seat if it is free, otherwise the next), given a boarding
// sequence, and recorded with the inbound connection so the manifest and the
// transfer message know where they came from. Refusals are per passenger:
// a party of two where one name is not on the list gets one seat and one
// reason, not a refusal for both, because that is what the requesting agent
// needs to act on. The flight itself not being under control, closed or
// cancelled refuses everyone.
func (s *Station) ThroughCheckIn(ctx context.Context, req ThroughRequest) (*ThroughResult, error) {
	res := &ThroughResult{Granted: true}
	fl, err := s.Flight(req.Key)
	if err != nil {
		reason := ThroughRefusedFlight
		if errors.Is(err, ErrFlightNotFound) {
			reason = ThroughRefusedFlight
		}
		return refuseAll(req, reason, "flight not under control here"), nil
	}
	if fl.Cancelled {
		return refuseAll(req, ThroughRefusedCancelled, "flight cancelled"), nil
	}
	if fl.State == StateClosed {
		return refuseAll(req, ThroughRefusedClosed, "flight closed"), nil
	}
	for _, tp := range req.Passengers {
		out := ThroughOutcome{Ref: tp.Ref, Surname: tp.Surname, Given: tp.Given}
		// Find the one passenger of that name under that locator who is not
		// yet accepted, so a party's second name does not resolve to the
		// first name already seated.
		pid := 0
		for _, p := range fl.Passengers {
			if tp.Locator != "" && p.Locator != strings.ToUpper(tp.Locator) {
				continue
			}
			if !strings.EqualFold(p.Surname, tp.Surname) {
				continue
			}
			if tp.Given != "" && p.Given != "" && !strings.EqualFold(p.Given, tp.Given) {
				continue
			}
			if p.Status != StatusListed && p.Status != StatusStandby {
				continue
			}
			pid = p.ID
			break
		}
		if pid == 0 {
			out.Reason, out.Text = ThroughRefusedNotFound, "passenger not on the name list"
			res.Outcomes = append(res.Outcomes, out)
			res.Granted = false
			continue
		}
		areq := AcceptRequest{PassengerID: pid, Seat: tp.SeatWant, Inbound: &req.Inbound}
		if tp.BagPieces > 0 {
			// Connecting bags were tagged by the delivering carrier; the
			// weight rides with the passenger for the load, the tags are
			// theirs. Split the weight evenly across the pieces declared.
			per := tp.BagWeight / tp.BagPieces
			for i := 0; i < tp.BagPieces; i++ {
				areq.Bags = append(areq.Bags, per)
			}
		}
		acc, err := s.Accept(ctx, req.Key, areq)
		if err != nil && errors.Is(err, ErrSeatTaken) || err != nil && errors.Is(err, ErrNoSuchSeat) {
			// The seat they wanted is gone; any seat is better than no
			// acceptance, and the response says which one they got.
			areq.Seat = ""
			acc, err = s.Accept(ctx, req.Key, areq)
		}
		if err != nil {
			out.Reason, out.Text = refusalFor(err), err.Error()
			res.Outcomes = append(res.Outcomes, out)
			res.Granted = false
			continue
		}
		if len(acc.Passengers) > 0 {
			p := acc.Passengers[0]
			out.Accepted, out.Seat, out.Sequence = true, p.Seat, p.Sequence
			out.Cabin = cabinOf(acc.Flight, p.Seat)
		}
		res.Outcomes = append(res.Outcomes, out)
		res.Flight = acc.Flight
	}
	if res.Flight == nil {
		res.Flight = fl
	}
	return res, nil
}

func refuseAll(req ThroughRequest, reason, text string) *ThroughResult {
	res := &ThroughResult{}
	for _, tp := range req.Passengers {
		res.Outcomes = append(res.Outcomes, ThroughOutcome{Ref: tp.Ref, Surname: tp.Surname, Given: tp.Given, Reason: reason, Text: text})
	}
	return res
}

func refusalFor(err error) string {
	switch {
	case errors.Is(err, ErrFlightClosed), errors.Is(err, ErrCheckInClosed):
		return ThroughRefusedClosed
	case errors.Is(err, ErrNoSeat):
		return ThroughRefusedFull
	case errors.Is(err, ErrSeatTaken), errors.Is(err, ErrNoSuchSeat):
		return ThroughRefusedSeat
	case errors.Is(err, ErrAlreadyAccepted):
		return ThroughRefusedAccepted
	case errors.Is(err, ErrPassengerNotFound):
		return ThroughRefusedNotFound
	}
	return ThroughRefusedFlight
}

// cabinOf names the cabin a seat sits in, for the response's cabin class.
func cabinOf(f *Flight, seat string) string {
	if f == nil || seat == "" {
		return ""
	}
	if comp, ok := f.Cabin.Has(seat); ok && comp != "" {
		return comp
	}
	return "Y"
}

// Describe renders the request for logs.
func (r ThroughRequest) Describe() string {
	return fmt.Sprintf("through check-in from %s/%s: %d pax off %s onto %s %s %s",
		r.Requestor, r.Station, len(r.Passengers), r.Inbound.Flight, r.Key.Flight, r.Key.Date, r.Key.Board)
}

// RecordOnward writes what another carrier's departure control answered for
// a passenger's onward connection: the seat and sequence it gave, or the
// refusal. The passenger is found by id on the flight.
func (s *Station) RecordOnward(ctx context.Context, k Key, passengerID int, seat string, sequence int, refused string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.flights[k]
	if !ok {
		return ErrFlightNotFound
	}
	p := f.passenger(passengerID)
	if p == nil {
		return ErrPassengerNotFound
	}
	if p.Onward == nil {
		return ErrNotFound
	}
	p.Onward.Seat, p.Onward.Sequence, p.Onward.Refused = seat, sequence, refused
	f.Revision++
	return s.store().SaveFlight(ctx, f)
}
