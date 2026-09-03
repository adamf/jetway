package dcs

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// AcceptRequest is what an agent enters to check somebody in.
//
// Either PassengerID names one passenger, or Locator (with Surname when the
// record covers several surnames) names a party and the whole party is
// accepted together. Bags belong to the party; the first passenger accepted
// carries them on the messages, which is how the wire attributes bags too.
type AcceptRequest struct {
	PassengerID int    `json:"passenger_id,omitempty"`
	Locator     string `json:"locator,omitempty"`
	Surname     string `json:"surname,omitempty"`
	// Seat is a requested seat for a single passenger. Empty auto-assigns;
	// parties are always auto-seated together.
	Seat string `json:"seat,omitempty"`
	// Bags are the weights, in kilos, of each bag checked.
	Bags   []int  `json:"bags,omitempty"`
	Ticket string `json:"ticket,omitempty"`
	// Onward and Inbound record a connection when the agent through-checks.
	Onward  *Connection `json:"onward,omitempty"`
	Inbound *Connection `json:"inbound,omitempty"`
	// Force is a supervisor override: accept after check-in has closed.
	Force bool `json:"force,omitempty"`
}

// Acceptance is the result: the passengers as accepted and the tags issued.
type Acceptance struct {
	Flight     *Flight      `json:"flight"`
	Passengers []*Passenger `json:"passengers"`
	Tags       []Bag        `json:"tags,omitempty"`
}

// Accept checks a passenger or a party in: seats them, issues sequence
// numbers, tags their bags.
func (s *Station) Accept(ctx context.Context, k Key, req AcceptRequest) (*Acceptance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.flights[k]
	if !ok {
		return nil, ErrFlightNotFound
	}
	if f.State == StateClosed {
		return nil, ErrFlightClosed
	}
	if f.State == StateCheckInClosed && !req.Force {
		return nil, ErrCheckInClosed
	}
	targets, err := f.resolve(req)
	if err != nil {
		return nil, err
	}
	if req.Seat != "" && len(targets) > 1 {
		return nil, fmt.Errorf("dcs: a requested seat applies to one passenger; %d resolved", len(targets))
	}
	acc, err := s.accept(f, targets, req)
	if err != nil {
		return nil, err
	}
	f.Revision++
	if err := s.store().SaveFlight(ctx, f); err != nil {
		return nil, err
	}
	acc.Flight = f.snapshot()
	return acc, nil
}

// resolve turns a request into the passengers it names.
func (f *Flight) resolve(req AcceptRequest) ([]*Passenger, error) {
	if req.PassengerID > 0 {
		p := f.passenger(req.PassengerID)
		if p == nil || p.Status == StatusDeleted {
			return nil, ErrPassengerNotFound
		}
		switch p.Status {
		case StatusAccepted, StatusBoarded:
			return nil, ErrAlreadyAccepted
		case StatusListed, StatusStandby:
			return []*Passenger{p}, nil
		default:
			return nil, fmt.Errorf("dcs: passenger is %s", p.Status)
		}
	}
	loc := strings.ToUpper(strings.TrimSpace(req.Locator))
	sur := strings.ToUpper(strings.TrimSpace(req.Surname))
	if loc == "" && sur == "" {
		return nil, ErrPassengerNotFound
	}
	var out []*Passenger
	accepted := 0
	for _, p := range f.Passengers {
		if loc != "" && p.Locator != loc {
			continue
		}
		if sur != "" && p.Surname != sur {
			continue
		}
		switch p.Status {
		case StatusListed, StatusStandby:
			out = append(out, p)
		case StatusAccepted, StatusBoarded:
			accepted++
		}
	}
	if len(out) == 0 {
		if accepted > 0 {
			return nil, ErrAlreadyAccepted
		}
		return nil, ErrPassengerNotFound
	}
	return out, nil
}

// accept does the work under the lock. Seats are assigned per compartment,
// the party together where a row allows.
func (s *Station) accept(f *Flight, targets []*Passenger, req AcceptRequest) (*Acceptance, error) {
	byComp := map[string][]*Passenger{}
	var comps []string
	for _, p := range targets {
		if _, seen := byComp[p.Compartment]; !seen {
			comps = append(comps, p.Compartment)
		}
		byComp[p.Compartment] = append(byComp[p.Compartment], p)
	}
	sort.Strings(comps)
	seats := map[int]string{}
	if req.Seat != "" {
		seat := strings.ToUpper(strings.TrimSpace(req.Seat))
		comp, ok := f.Cabin.Has(seat)
		if !ok {
			return nil, ErrNoSuchSeat
		}
		if _, taken := f.Cabin.Occupied[seat]; taken {
			return nil, ErrSeatTaken
		}
		p := targets[0]
		if comp != p.Compartment {
			// Seating across cabins is a real thing -- an upgrade -- and it
			// is a decision, so it is recorded on the flight.
			f.alert(s.now(), "seated_out_of_compartment", fmt.Sprintf("%s booked %s seated in %s at %s", p.Display(), p.Compartment, comp, seat))
			p.Compartment = comp
		}
		seats[p.ID] = seat
	} else {
		for _, comp := range comps {
			group := byComp[comp]
			assigned, err := f.Cabin.Assign(comp, len(group))
			if err != nil {
				return nil, err
			}
			for i, p := range group {
				seats[p.ID] = assigned[i]
			}
		}
	}
	now := s.now()
	acc := &Acceptance{}
	for _, p := range targets {
		seat := seats[p.ID]
		if err := f.Cabin.Take(seat, p.ID); err != nil {
			return nil, err
		}
		p.Seat = seat
		p.Status = StatusAccepted
		p.Sequence = f.nextSeq
		f.nextSeq++
		t := now
		p.AcceptedAt = &t
		if req.Ticket != "" {
			p.Ticket = req.Ticket
		}
		if req.Onward != nil {
			c := *req.Onward
			p.Onward = &c
		}
		if req.Inbound != nil {
			c := *req.Inbound
			p.Inbound = &c
		}
		acc.Passengers = append(acc.Passengers, p)
	}
	lead := targets[0]
	for _, w := range req.Bags {
		b := Bag{Tag: s.nextTag(), Weight: w}
		lead.Bags = append(lead.Bags, b)
		acc.Tags = append(acc.Tags, b)
	}
	return acc, nil
}

// GoShow is a passenger at the counter whom the name list did not carry.
type GoShow struct {
	Surname string `json:"surname"`
	Given   string `json:"given"`
	Class   string `json:"class"`
	Dest    string `json:"dest,omitempty"`
	Locator string `json:"locator,omitempty"`
	Ticket  string `json:"ticket,omitempty"`
	// Category is how the final sales message will report them. Empty picks
	// GOSHO when a locator is given and NOREC when not.
	Category Category `json:"category,omitempty"`
	Seat     string   `json:"seat,omitempty"`
	Bags     []int    `json:"bags,omitempty"`
	// Standby lists the passenger for a seat at check-in close instead of
	// accepting now. Staff travel is always standby.
	Standby bool `json:"standby,omitempty"`
}

// AcceptGoShow adds a passenger the list did not carry and accepts them, or
// lists them for standby.
func (s *Station) AcceptGoShow(ctx context.Context, k Key, g GoShow) (*Acceptance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.flights[k]
	if !ok {
		return nil, ErrFlightNotFound
	}
	if f.State == StateClosed {
		return nil, ErrFlightClosed
	}
	if f.State == StateCheckInClosed {
		return nil, ErrCheckInClosed
	}
	if g.Surname == "" {
		return nil, fmt.Errorf("dcs: a go-show needs a surname")
	}
	cat := g.Category
	if cat == "" {
		cat = CategoryNoRec
		if g.Locator != "" {
			cat = CategoryGoShow
		}
	}
	class := g.Class
	if class == "" {
		class = "Y"
	}
	dest := g.Dest
	if dest == "" {
		dest = f.Dest
	}
	p := &Passenger{
		ID: f.nextID, Surname: strings.ToUpper(g.Surname), Given: strings.ToUpper(g.Given),
		Locator: strings.ToUpper(g.Locator), Class: class, Compartment: f.compartmentFor(class),
		Dest: dest, Type: typeOf(strings.ToUpper(g.Given), nil), Ticket: g.Ticket,
		Status: StatusStandby, Category: cat,
	}
	p.Party = fmt.Sprintf("GOSHOW/%d/%s", p.ID, p.Surname)
	f.nextID++
	f.Passengers = append(f.Passengers, p)
	acc := &Acceptance{}
	// A go-show gets a seat only beyond the booked load: the passengers still
	// on the list and not yet at the counter hold theirs until check-in
	// closes. Staff travel always waits.
	standby := g.Standby || cat == CategoryIDPad || f.spare(p.Compartment) <= 0
	if !standby {
		var err error
		acc, err = s.accept(f, []*Passenger{p}, AcceptRequest{Seat: g.Seat, Bags: g.Bags})
		if err != nil {
			if err == ErrNoSeat {
				// Nothing to sit in: they wait, as at any full counter.
				f.alert(s.now(), "goshow_standby", fmt.Sprintf("%s listed standby: no %s seat", p.Display(), p.Compartment))
			} else {
				f.Passengers = f.Passengers[:len(f.Passengers)-1]
				return nil, err
			}
		}
	}
	if p.Status == StatusStandby {
		acc.Passengers = []*Passenger{p}
	}
	f.Revision++
	if err := s.store().SaveFlight(ctx, f); err != nil {
		return nil, err
	}
	acc.Flight = f.snapshot()
	return acc, nil
}

// spare is how many seats in a compartment are neither occupied nor owed
// to a listed passenger who has not yet checked in.
func (f *Flight) spare(comp string) int {
	if f.Cabin == nil {
		return 0
	}
	owed := 0
	for _, p := range f.Passengers {
		if p.Status == StatusListed && p.Compartment == comp {
			owed++
		}
	}
	return f.Cabin.Free(comp) - owed
}

// Board passes a passenger through the gate.
func (s *Station) Board(ctx context.Context, k Key, passengerID int) (*Passenger, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.flights[k]
	if !ok {
		return nil, ErrFlightNotFound
	}
	if f.State == StateClosed {
		return nil, ErrFlightClosed
	}
	p := f.passenger(passengerID)
	if p == nil {
		return nil, ErrPassengerNotFound
	}
	switch p.Status {
	case StatusBoarded:
		return nil, ErrAlreadyBoarded
	case StatusAccepted:
	default:
		return nil, ErrNotAccepted
	}
	now := s.now()
	p.Status = StatusBoarded
	p.BoardedAt = &now
	f.Revision++
	if err := s.store().SaveFlight(ctx, f); err != nil {
		return nil, err
	}
	c := *p
	return &c, nil
}

// Offload removes an accepted or boarded passenger. Their seat is freed
// and their bags are marked to come off; the returned passenger carries the
// tags so the caller can tell the sortation system.
func (s *Station) Offload(ctx context.Context, k Key, passengerID int, reason string) (*Passenger, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.flights[k]
	if !ok {
		return nil, ErrFlightNotFound
	}
	if f.State == StateClosed {
		return nil, ErrFlightClosed
	}
	p := f.passenger(passengerID)
	if p == nil {
		return nil, ErrPassengerNotFound
	}
	if !p.Flying() {
		return nil, ErrNotAccepted
	}
	f.offload(p, reason)
	f.Revision++
	if err := s.store().SaveFlight(ctx, f); err != nil {
		return nil, err
	}
	c := *p
	return &c, nil
}

func (f *Flight) offload(p *Passenger, reason string) {
	if p.Seat != "" {
		f.Cabin.Release(p.Seat)
	}
	p.Status = StatusOffloaded
	p.OffloadReason = reason
	for i := range p.Bags {
		p.Bags[i].Offloaded = true
	}
}

// CloseCheckIn stops acceptance and clears the standby list into whatever
// seats remain: revenue passengers first, staff last, in the order they
// presented.
func (s *Station) CloseCheckIn(ctx context.Context, k Key) (*Flight, []*Passenger, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.flights[k]
	if !ok {
		return nil, nil, ErrFlightNotFound
	}
	if f.State == StateClosed {
		return nil, nil, ErrFlightClosed
	}
	cleared := s.closeCheckIn(f)
	f.Revision++
	if err := s.store().SaveFlight(ctx, f); err != nil {
		return nil, nil, err
	}
	return f.snapshot(), cleared, nil
}

func (s *Station) closeCheckIn(f *Flight) []*Passenger {
	if f.State != StateOpen {
		return nil
	}
	now := s.now()
	f.State = StateCheckInClosed
	f.CheckInClosedAt = &now
	var standby []*Passenger
	for _, p := range f.Passengers {
		if p.Status == StatusStandby {
			standby = append(standby, p)
		}
	}
	sort.SliceStable(standby, func(i, j int) bool {
		si, sj := standby[i].Category == CategoryIDPad, standby[j].Category == CategoryIDPad
		if si != sj {
			return !si
		}
		return standby[i].ID < standby[j].ID
	})
	var cleared []*Passenger
	for _, p := range standby {
		if _, err := s.accept(f, []*Passenger{p}, AcceptRequest{}); err != nil {
			continue
		}
		cleared = append(cleared, p)
	}
	return cleared
}

// CloseOptions is what load control needs beyond the manifest.
type CloseOptions struct {
	// Fuel is the flight plan's take-off and trip fuel, in kilos.
	Fuel FuelPlan `json:"fuel"`
	// Cargo and Mail are the dead load, in kilos.
	Cargo int `json:"cargo,omitempty"`
	Mail  int `json:"mail,omitempty"`
	// RequireReconciled refuses to close while a loaded bag has no boarded
	// passenger, with ErrUnreconciledBags naming them; the caller pulls the
	// bags (BSM DEL to sortation, then BagOffloaded) and closes again.
	// Without it the closure still reports the reconciliation and the
	// unaccompanied bags are marked offloaded, as the load must not count
	// them.
	RequireReconciled bool `json:"require_reconciled,omitempty"`
	// Force closes a flight whose check-in is still open, closing check-in
	// first. Boarding is not implied: accepted passengers who have not
	// boarded are offloaded, which is what closing the door means.
	Force bool `json:"force,omitempty"`
}

// Closure is what closing a flight produces: the messages, the loadsheet,
// and the bags that must come off.
type Closure struct {
	Flight *Flight `json:"flight"`
	Counts Counts  `json:"counts"`
	// Offloaded lists the passengers removed at close -- accepted, never
	// boarded -- with their bags still attached so the caller can send the
	// sortation system a BSM DEL for each.
	Offloaded []*Passenger `json:"offloaded,omitempty"`

	// Message texts, ready for a Type B envelope. Lists may run to parts.
	PFS []string `json:"pfs"`
	PTM []string `json:"ptm,omitempty"`
	PSM []string `json:"psm"`
	ETL []string `json:"etl,omitempty"`
	LDM string   `json:"ldm"`
	CPM string   `json:"cpm,omitempty"`

	Loadsheet string `json:"loadsheet"`
	Load      *Load  `json:"load"`

	// Reconciliation is the bag reconciliation at the door: every bag the
	// sortation system reported loaded must belong to a passenger who
	// boarded. A loaded bag whose passenger did not fly is a hold item the
	// aircraft cannot depart with; a boarded passenger's bag that was never
	// loaded travels later as a rush bag. The practice is BRS; the rule is
	// security regulation in most jurisdictions.
	Reconciliation Reconciliation `json:"reconciliation"`
}

// BagRef names one bag and whose it is.
type BagRef struct {
	Tag         string `json:"tag"`
	PassengerID int    `json:"passenger_id"`
	Surname     string `json:"surname"`
	Status      Status `json:"passenger_status"`
	Position    string `json:"position,omitempty"`
}

// Reconciliation is the result of matching loaded bags to boarded
// passengers.
type Reconciliation struct {
	// Loaded is how many bags the hold reports; Boarded how many belong to
	// passengers who boarded.
	Loaded  int `json:"loaded"`
	Boarded int `json:"boarded"`
	// Unaccompanied are loaded bags whose passenger did not board: they
	// must come off before the door closes.
	Unaccompanied []BagRef `json:"unaccompanied,omitempty"`
	// NotLoaded are boarded passengers' bags the hold never reported:
	// short-shipped, to follow as rush bags.
	NotLoaded []BagRef `json:"not_loaded,omitempty"`
}

// Clear reports whether the flight may depart: no loaded bag without its
// passenger on board.
func (r Reconciliation) Clear() bool { return len(r.Unaccompanied) == 0 }

// reconcile matches the hold against the cabin.
func reconcile(f *Flight) Reconciliation {
	var r Reconciliation
	for _, p := range f.Passengers {
		for _, b := range p.Bags {
			ref := BagRef{Tag: b.Tag, PassengerID: p.ID, Surname: p.Surname, Status: p.Status, Position: b.Position}
			if b.Loaded {
				r.Loaded++
				if p.Status == StatusBoarded {
					r.Boarded++
				} else {
					r.Unaccompanied = append(r.Unaccompanied, ref)
				}
			} else if p.Status == StatusBoarded && !b.Offloaded {
				r.NotLoaded = append(r.NotLoaded, ref)
			}
		}
	}
	return r
}

// UnreconciledError is CloseFlight refusing to close a door with a loaded
// bag whose passenger is not on board.
type UnreconciledError struct{ Bags []BagRef }

func (e *UnreconciledError) Error() string {
	return fmt.Sprintf("dcs: %d loaded bag(s) without a boarded passenger", len(e.Bags))
}

// ErrUnreconciledBags is the sentinel errors.Is matches for UnreconciledError.
var ErrUnreconciledBags = errors.New("dcs: loaded bags without boarded passengers")

func (e *UnreconciledError) Is(target error) bool { return target == ErrUnreconciledBags }

// CloseFlight is the door closing: no-shows are declared, unboarded
// passengers offloaded, the load computed, the messages built. After this
// the flight does not change.
func (s *Station) CloseFlight(ctx context.Context, k Key, opts CloseOptions) (*Closure, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.flights[k]
	if !ok {
		return nil, ErrFlightNotFound
	}
	if f.State == StateClosed {
		return nil, ErrFlightClosed
	}
	if f.State == StateOpen {
		if !opts.Force {
			return nil, ErrCheckInOpen
		}
		s.closeCheckIn(f)
	}
	now := s.now()
	cl := &Closure{}
	for _, p := range f.Passengers {
		switch p.Status {
		case StatusListed:
			p.Status = StatusNoShow
		case StatusAccepted:
			f.offload(p, "not boarded at close")
			cl.Offloaded = append(cl.Offloaded, p)
		}
	}
	rec := reconcile(f)
	if opts.RequireReconciled && !rec.Clear() {
		// Undo nothing: the offloads above stand, the door stays open.
		return nil, &UnreconciledError{Bags: rec.Unaccompanied}
	}
	// A loaded bag whose passenger did not fly comes off the aircraft, so
	// the load never counts it.
	for _, p := range f.Passengers {
		if p.Status == StatusBoarded {
			continue
		}
		for i := range p.Bags {
			if p.Bags[i].Loaded {
				p.Bags[i].Loaded = false
				p.Bags[i].Offloaded = true
			}
		}
	}
	cl.Reconciliation = rec
	t, ok := s.fleet().Type(f.Equipment)
	if !ok {
		t = s.fleet().Default()
	}
	f.Load = t.Plan(f, s.Weights, opts.Fuel, opts.Cargo, opts.Mail)
	f.Loadsheet = t.Loadsheet(f, f.Load, now)
	f.State = StateClosed
	f.ClosedAt = &now
	f.Revision++

	cl.PFS = BuildPFS(f)
	cl.PTM = BuildPTM(f)
	cl.PSM = BuildPSM(f)
	cl.ETL = BuildETL(f)
	cl.LDM = BuildLDM(f, f.Load)
	if t.hasULDs() {
		cl.CPM = BuildCPM(f, f.Load)
	}
	cl.Loadsheet = f.Loadsheet
	cl.Load = f.Load
	cl.Counts = f.Counts()
	if err := s.store().SaveFlight(ctx, f); err != nil {
		return nil, err
	}
	cl.Flight = f.snapshot()
	return cl, nil
}

// Cancellation is what cancelling a flight under control produces: the
// passengers who had been accepted or boarded, now offloaded with their
// bags still attached so the caller can pull them, and the passengers who
// were still only listed, whom reservations will re-accommodate.
type Cancellation struct {
	Flight    *Flight      `json:"flight"`
	Offloaded []*Passenger `json:"offloaded,omitempty"`
	Listed    []*Passenger `json:"listed,omitempty"`
}

// CancelFlight closes a flight that will not fly. Everyone accepted or
// boarded is offloaded and their bags come off; nobody becomes a no-show,
// because nobody failed to turn up -- the aircraft did. No load, no
// messages: a cancelled departure has nothing to report to the arrival
// station, and reservations already knows from the schedule message.
func (s *Station) CancelFlight(ctx context.Context, k Key, reason string) (*Cancellation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.flights[k]
	if !ok {
		return nil, ErrFlightNotFound
	}
	if f.State == StateClosed {
		return nil, ErrFlightClosed
	}
	now := s.now()
	c := &Cancellation{}
	for _, p := range f.Passengers {
		switch p.Status {
		case StatusAccepted, StatusBoarded:
			f.offload(p, "flight cancelled: "+reason)
			c.Offloaded = append(c.Offloaded, p)
		case StatusListed, StatusStandby:
			c.Listed = append(c.Listed, p)
		}
	}
	if f.State == StateOpen {
		f.State = StateCheckInClosed
		f.CheckInClosedAt = &now
	}
	f.State = StateClosed
	f.ClosedAt = &now
	f.Cancelled = true
	f.CancelReason = reason
	f.alert(now, "flight_cancelled", reason)
	f.Revision++
	if err := s.store().SaveFlight(ctx, f); err != nil {
		return nil, err
	}
	c.Flight = f.snapshot()
	return c, nil
}
