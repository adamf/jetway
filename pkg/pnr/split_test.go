package pnr

import (
	"testing"
	"time"
)

func partyOfThree() *PNR {
	now := time.Now().UTC()
	return &PNR{
		RecordLocator: "PAR001", Status: StatusOpen, CreatedAt: now, UpdatedAt: now,
		Passengers: []Passenger{
			{Ref: 1, Surname: "FLETCHER", Given: "ADAM"},
			{Ref: 2, Surname: "FLETCHER", Given: "SAM"},
			{Ref: 3, Surname: "CARTER", Given: "JAMES"},
		},
		Segments: []Segment{
			{Ref: 1, Type: SegmentAir, Carrier: "BA", FlightNum: "0117", Status: "HK", Seats: 3},
			{Ref: 2, Type: SegmentAir, Carrier: "AA", FlightNum: "0050", Status: "HK", Seats: 3},
		},
		SSRs: []SSR{
			{Code: "VGML", Carrier: "BA", Status: "HK", Count: 1, PaxRef: 1},
			{Code: "WCHR", Carrier: "BA", Status: "HK", Count: 1, PaxRef: 3},
			{Code: "CTCM", Carrier: "BA", Status: "HK", Count: 1}, // booking-wide
		},
		Contacts: []Contact{{Type: "phone", Text: "LON 44-20-7777-7777"}},
	}
}

func TestDivideMovesPassengersAndSeats(t *testing.T) {
	p := partyOfThree()
	child, err := p.Divide([]int{3}, "CHD001")
	if err != nil {
		t.Fatalf("Divide: %v", err)
	}

	if len(p.Passengers) != 2 || len(child.Passengers) != 1 {
		t.Fatalf("parent %d, child %d passengers", len(p.Passengers), len(child.Passengers))
	}
	if child.Passengers[0].Surname != "CARTER" {
		t.Errorf("the wrong passenger moved: %+v", child.Passengers[0])
	}
	// A segment held for three becomes one held for two and one held for one.
	// Getting this wrong either oversells the flight or gives a seat back.
	for i := range p.Segments {
		if p.Segments[i].Seats != 2 {
			t.Errorf("parent segment %d holds %d seats, want 2", i+1, p.Segments[i].Seats)
		}
		if child.Segments[i].Seats != 1 {
			t.Errorf("child segment %d holds %d seats, want 1", i+1, child.Segments[i].Seats)
		}
	}
	if child.SplitFrom != "PAR001" {
		t.Errorf("SplitFrom = %q", child.SplitFrom)
	}
	if len(p.SplitTo) != 1 || p.SplitTo[0] != "CHD001" {
		t.Errorf("SplitTo = %v", p.SplitTo)
	}
	// Both halves keep what belongs to the booking rather than a passenger.
	if len(child.Contacts) != 1 || len(p.Contacts) != 1 {
		t.Error("contacts belong to the booking and should survive on both")
	}
}

func TestDivideRemapsPerPassengerReferences(t *testing.T) {
	p := partyOfThree()
	// Move the *first* passenger, so the survivors renumber: 2 becomes 1 and
	// 3 becomes 2. Anything pointing at the old numbers is now wrong.
	child, err := p.Divide([]int{1}, "CHD002")
	if err != nil {
		t.Fatal(err)
	}

	// The meal belonged to passenger 1 and went with them.
	if len(child.SSRs) != 2 {
		t.Fatalf("child SSRs = %+v, want the meal and the booking-wide one", child.SSRs)
	}
	var meal, ctcm bool
	for _, s := range child.SSRs {
		switch s.Code {
		case "VGML":
			meal = true
			if s.PaxRef != 1 {
				t.Errorf("the meal points at passenger %d on a record with one passenger", s.PaxRef)
			}
		case "CTCM":
			ctcm = true
		}
	}
	if !meal || !ctcm {
		t.Errorf("child SSRs = %+v", child.SSRs)
	}

	// The wheelchair belonged to old passenger 3, who is now passenger 2.
	var found bool
	for _, s := range p.SSRs {
		if s.Code == "WCHR" {
			found = true
			if s.PaxRef != 2 {
				t.Errorf("WCHR points at passenger %d; CARTER is now 2", s.PaxRef)
			}
			// And it must still be the right person.
			if p.Passengers[s.PaxRef-1].Surname != "CARTER" {
				t.Errorf("WCHR now belongs to %s", p.Passengers[s.PaxRef-1].Surname)
			}
		}
	}
	if !found {
		t.Error("the wheelchair did not stay with the parent")
	}
}

func TestDivideMovesTicketsWithTheirPassenger(t *testing.T) {
	p := partyOfThree()
	num := func(serial string) TicketNumber {
		n, err := NewTicketNumber("125", serial)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	p.Tickets = []Ticket{
		{Number: num("100000001"), PaxRef: 1, Coupons: []Coupon{{Number: 1, SegmentRef: 1, Status: CouponOpen}}},
		{Number: num("100000003"), PaxRef: 3, Coupons: []Coupon{{Number: 1, SegmentRef: 1, Status: CouponOpen}}},
	}

	child, err := p.Divide([]int{3}, "CHD003")
	if err != nil {
		t.Fatal(err)
	}
	if len(child.Tickets) != 1 || child.Tickets[0].PaxRef != 1 {
		t.Errorf("child tickets = %+v; the document follows its passenger and renumbers", child.Tickets)
	}
	if len(p.Tickets) != 1 || p.Tickets[0].PaxRef != 1 {
		t.Errorf("parent tickets = %+v", p.Tickets)
	}
}

func TestDivideRefusesTheDegenerateCases(t *testing.T) {
	if _, err := partyOfThree().Divide(nil, "X"); err == nil {
		t.Error("a split with no passengers should be refused")
	}
	// Moving everybody is a rename, not a split, and would leave a parent
	// holding seats for nobody.
	if _, err := partyOfThree().Divide([]int{1, 2, 3}, "X"); err == nil {
		t.Error("splitting every passenger should be refused")
	}
	if _, err := partyOfThree().Divide([]int{9}, "X"); err == nil {
		t.Error("splitting a passenger who is not on the record should be refused")
	}
}

func TestSeatShare(t *testing.T) {
	// The ordinary case: seats match passengers.
	if got := seatShare(3, 1, 3); got != 1 {
		t.Errorf("seatShare(3,1,3) = %d, want 1", got)
	}
	if got := seatShare(3, 2, 3); got != 2 {
		t.Errorf("seatShare(3,2,3) = %d, want 2", got)
	}
	// A count that disagrees with the passenger list came from a carrier, and
	// this is not the place to argue with it: share it out, never to zero.
	if got := seatShare(1, 1, 3); got < 1 {
		t.Errorf("seatShare(1,1,3) = %d, want at least 1", got)
	}
	if got := seatShare(0, 1, 3); got != 0 {
		t.Errorf("seatShare(0,1,3) = %d, want 0", got)
	}
}
