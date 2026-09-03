package dcs

import (
	"context"
	"fmt"
	"sort"
)

// EquipmentChange is what substituting the aircraft did to a flight under
// control: who kept their seat, who was moved, and who no longer fits.
type EquipmentChange struct {
	Flight     *Flight
	From, To   string // aircraft types
	Kept       int
	Reseated   []*Passenger
	Displaced  []*Passenger
	SeatsAfter int
}

// ChangeEquipment substitutes the aircraft on an open flight: the schedule
// change an ASM EQT announces, and the story behind an AOG. The cabin is
// rebuilt from the new type. Every accepted or boarded passenger keeps their
// seat if the new cabin has it in the same compartment, is re-seated in
// their compartment if it does not, and goes to standby -- involuntarily
// denied boarding, with an alert a supervisor sees -- when the new cabin has
// no seat left for them. Boarded passengers are seated first, then accepted
// ones in sequence order, so the aircraft's own order of arrival decides who
// is displaced. A closed flight cannot change aircraft.
func (s *Station) ChangeEquipment(ctx context.Context, k Key, eq Equipment) (*EquipmentChange, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.flights[k]
	if !ok {
		return nil, ErrFlightNotFound
	}
	if f.State == StateClosed {
		return nil, ErrFlightClosed
	}
	t, ok := s.fleet().Type(eq.Type)
	if !ok {
		return nil, fmt.Errorf("dcs: aircraft type %q is not in the fleet data", eq.Type)
	}
	now := s.now()
	ch := &EquipmentChange{From: f.Equipment, To: eq.Type}
	old := f.Cabin
	f.Equipment = eq.Type
	if eq.Registration != "" {
		f.Registration = eq.Registration
	}
	if eq.Crew != "" {
		f.Crew = eq.Crew
	}
	f.Version = t.Version()
	f.Cabin = t.Cabin.instance()
	ch.SeatsAfter = f.Cabin.Seats()

	// Seat holders, boarded first, then by sequence: the order the aircraft
	// itself would have honoured.
	var holders []*Passenger
	for _, p := range f.Passengers {
		if (p.Status == StatusAccepted || p.Status == StatusBoarded) && p.Seat != "" {
			holders = append(holders, p)
		}
	}
	sort.SliceStable(holders, func(i, j int) bool {
		if holders[i].Status != holders[j].Status {
			return holders[i].Status == StatusBoarded
		}
		return holders[i].Sequence < holders[j].Sequence
	})
	for _, p := range holders {
		wantComp, _ := old.Has(p.Seat)
		if comp, exists := f.Cabin.Has(p.Seat); exists && comp == wantComp {
			if err := f.Cabin.Take(p.Seat, p.ID); err == nil {
				ch.Kept++
				continue
			}
		}
		comp := wantComp
		if comp == "" {
			comp = f.Cabin.CompartmentFor(p.Class)
		}
		seats, err := f.Cabin.Assign(comp, 1)
		if err != nil || len(seats) == 0 {
			// No seat in their cabin: try any cabin before turning them away.
			for _, c := range f.Cabin.compartments() {
				if seats, err = f.Cabin.Assign(c, 1); err == nil && len(seats) > 0 {
					break
				}
			}
		}
		if err != nil || len(seats) == 0 {
			p.Seat = ""
			p.Status = StatusStandby
			ch.Displaced = append(ch.Displaced, p)
			continue
		}
		p.Seat = seats[0]
		ch.Reseated = append(ch.Reseated, p)
	}
	f.alert(now, "equipment_change", fmt.Sprintf("aircraft changed %s to %s (%s): %d kept their seat, %d re-seated, %d displaced",
		ch.From, ch.To, f.Version, ch.Kept, len(ch.Reseated), len(ch.Displaced)))
	if len(ch.Displaced) > 0 {
		f.alert(now, "denied_boarding", fmt.Sprintf("%d accepted passengers have no seat on the %s and are on standby", len(ch.Displaced), ch.To))
	}
	f.Revision++
	if err := s.store().SaveFlight(ctx, f); err != nil {
		return nil, err
	}
	ch.Flight = f.snapshot()
	return ch, nil
}
