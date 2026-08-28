// Package demo provides simulated carriers and a flight catalog so the gateway
// can be exercised end to end without a real interline partner.
package demo

import (
	"fmt"
	"time"

	"github.com/adamf/jetway/internal/store"
)

// Carrier describes a simulated airline reservation system.
type Carrier struct {
	// Designator is the two-character airline code.
	Designator string
	Name       string
	// TTYAddress is the Type B address this carrier receives on.
	TTYAddress string
	// Format is the wire encoding this carrier's link speaks. Running a mixed
	// fleet is the point: a gateway that only ever sees one encoding has not
	// been tested.
	Format store.Format
	// Hub is the carrier's main base, used to generate a plausible schedule.
	Hub string
}

// Fleet is the default simulated interline environment: three carriers, two
// wire formats, so a single booking can span both.
var Fleet = []Carrier{
	{Designator: "BA", Name: "British Airways", TTYAddress: "LHRRMBA", Format: store.FormatTypeB, Hub: "LHR"},
	{Designator: "AA", Name: "American Airlines", TTYAddress: "DFWRMAA", Format: store.FormatEDIFACT, Hub: "DFW"},
	{Designator: "LH", Name: "Lufthansa", TTYAddress: "FRARMLH", Format: store.FormatTypeB, Hub: "FRA"},
}

// Flight is one schedule entry offered to the booking form.
type Flight struct {
	Carrier   string   `json:"carrier"`
	FlightNum string   `json:"flight_num"`
	Board     string   `json:"board"`
	Off       string   `json:"off"`
	Depart    string   `json:"depart"` // HHMM
	Arrive    string   `json:"arrive"` // HHMM
	Classes   []string `json:"classes"`
}

// Route is a city pair in the demo schedule.
type Route struct {
	Board, Off  string
	Depart, Arr string
	Number      string
	Carrier     string
}

// Schedule is a small, fixed timetable. It is fixed rather than generated so
// that a demonstration repeats identically and a screenshot stays accurate.
var Schedule = []Route{
	{"LHR", "JFK", "0800", "1105", "0175", "BA"},
	{"LHR", "JFK", "1420", "1725", "0117", "BA"},
	{"JFK", "LHR", "1830", "0645", "0112", "BA"},
	{"LHR", "FRA", "0705", "0950", "0902", "BA"},
	{"DFW", "LHR", "1710", "0705", "0050", "AA"},
	{"LHR", "DFW", "1030", "1520", "0051", "AA"},
	{"JFK", "DFW", "0900", "1210", "2401", "AA"},
	{"FRA", "JFK", "1015", "1300", "0400", "LH"},
	{"JFK", "FRA", "1735", "0715", "0401", "LH"},
	{"FRA", "LHR", "1145", "1225", "0906", "LH"},
}

// BookingClasses are the classes the demo offers.
//
// Z is always refused by the simulated inventory, which gives the console a
// reliable way to show an unable response rather than only the happy path.
var BookingClasses = []string{"F", "J", "Y", "M", "Z"}

// Flights renders the schedule for the booking form.
func Flights() []Flight {
	out := make([]Flight, 0, len(Schedule))
	for _, r := range Schedule {
		out = append(out, Flight{
			Carrier: r.Carrier, FlightNum: r.Number, Board: r.Board, Off: r.Off,
			Depart: r.Depart, Arrive: r.Arr, Classes: BookingClasses,
		})
	}
	return out
}

// TimesFor returns the scheduled departure and arrival for a flight, when the
// demo schedule knows it.
func TimesFor(carrier, number, board, off string) (dep, arr string, ok bool) {
	for _, r := range Schedule {
		if r.Carrier == carrier && r.Number == number && r.Board == board && r.Off == off {
			return r.Depart, r.Arr, true
		}
	}
	return "", "", false
}

// DefaultDate returns a departure date a month out, which is inside the booking
// window under any interpretation and avoids demonstrations that break when run
// near a year boundary.
func DefaultDate() time.Time { return time.Now().UTC().AddDate(0, 0, 30) }

// CarrierByDesignator looks up a carrier in the fleet.
func CarrierByDesignator(d string) (Carrier, bool) {
	for _, c := range Fleet {
		if c.Designator == d {
			return c, true
		}
	}
	return Carrier{}, false
}

// String renders a carrier for logs.
func (c Carrier) String() string {
	return fmt.Sprintf("%s (%s, %s via %s)", c.Designator, c.Name, c.Format, c.TTYAddress)
}
