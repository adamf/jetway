package demo

import (
	"testing"
	"time"
)

func TestJourneysProduceInterlineItineraries(t *testing.T) {
	js := InterlineJourneys()
	if len(js) == 0 {
		t.Fatal("the schedule should yield at least one multi-carrier journey")
	}
	for _, j := range js {
		if len(j.Legs) != 2 {
			t.Errorf("%s: legs = %d, want 2", j.Label(), len(j.Legs))
		}
		if j.Legs[0].Carrier == j.Legs[1].Carrier {
			t.Errorf("%s: both legs on %s is not interline", j.Label(), j.Legs[0].Carrier)
		}
		if j.Legs[0].Off != j.Legs[1].Board {
			t.Errorf("%s: legs do not connect: %s then %s",
				j.Label(), j.Legs[0].Off, j.Legs[1].Board)
		}
		if j.Via != j.Legs[0].Off {
			t.Errorf("%s: via = %q, want %q", j.Label(), j.Via, j.Legs[0].Off)
		}
		// A journey that doubles back is not an itinerary anyone would sell.
		if j.Origin == j.Destination {
			t.Errorf("%s: origin equals destination", j.Label())
		}
		if j.ConnectMinutes < int(MinConnect.Minutes()) || j.ConnectMinutes > int(MaxConnect.Minutes()) {
			t.Errorf("%s: connect time %dm is outside the plausible window", j.Label(), j.ConnectMinutes)
		}
		if len(j.Carriers) != 2 {
			t.Errorf("%s: carriers = %v", j.Label(), j.Carriers)
		}
	}
}

// A flight that lands the next morning pushes its connection to the following
// day; the offset has to say so or the second segment is sold for a date the
// passenger cannot make.
func TestOvernightArrivalPushesTheConnection(t *testing.T) {
	var sawOvernight bool
	for _, j := range Journeys() {
		if len(j.Legs) != 2 {
			continue
		}
		dep, arr, ok := arrivalMinutes(Route{
			Depart: j.Legs[0].Depart, Arr: j.Legs[0].Arrive,
			Board: j.Legs[0].Board, Off: j.Legs[0].Off,
			Number: j.Legs[0].FlightNum, Carrier: j.Legs[0].Carrier,
		})
		if !ok {
			t.Fatalf("%s: unparsable times", j.Label())
		}
		if arr < 24*60 {
			// Same-day arrival: the connection is on day 0 or, if it had to
			// wait, day 1.
			if j.Legs[1].DayOffset > 1 {
				t.Errorf("%s: day offset %d is too large", j.Label(), j.Legs[1].DayOffset)
			}
			continue
		}
		sawOvernight = true
		if j.Legs[1].DayOffset < 1 {
			t.Errorf("%s: leg 1 arrives the next day (dep %d, arr %d) but the connection "+
				"is on day %d", j.Label(), dep, arr, j.Legs[1].DayOffset)
		}
	}
	if !sawOvernight {
		t.Log("no overnight arrival in the schedule; the offset path was not exercised")
	}
}

func TestNonStopsAreListed(t *testing.T) {
	var nonstops int
	for _, j := range Journeys() {
		if len(j.Legs) == 1 {
			nonstops++
			if j.Interline {
				t.Errorf("%s: a single leg cannot be interline", j.Label())
			}
		}
	}
	if nonstops != len(Schedule) {
		t.Errorf("non-stops = %d, want %d (one per schedule entry)", nonstops, len(Schedule))
	}
}

// The interline journeys must be first, since they are the ones worth showing.
func TestInterlineJourneysSortFirst(t *testing.T) {
	js := Journeys()
	seenNonInterline := false
	for _, j := range js {
		if !j.Interline {
			seenNonInterline = true
			continue
		}
		if seenNonInterline {
			t.Fatalf("interline journey %s appears after a non-interline one", j.Label())
		}
	}
}

func TestScheduleKeysCoverEveryClass(t *testing.T) {
	keys := ScheduleKeys("BA", time.Now().UTC())
	var baRoutes int
	for _, r := range Schedule {
		if r.Carrier == "BA" {
			baRoutes++
		}
	}
	if want := baRoutes * len(BookingClasses); len(keys) != want {
		t.Errorf("keys = %d, want %d", len(keys), want)
	}
}
