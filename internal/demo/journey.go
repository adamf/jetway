package demo

import (
	"fmt"
	"sort"
	"strconv"
	"time"
)

// Connection limits for a generated journey. The minimum keeps an itinerary
// plausible -- an international connection needs time to clear the terminal --
// and the maximum keeps it from proposing an overnight sit as a connection.
const (
	MinConnect = 60 * time.Minute
	MaxConnect = 8 * time.Hour
)

// Leg is one flight in a generated journey.
type Leg struct {
	Carrier   string `json:"carrier"`
	FlightNum string `json:"flight_num"`
	Board     string `json:"board"`
	Off       string `json:"off"`
	Depart    string `json:"depart"`
	Arrive    string `json:"arrive"`
	// DayOffset is how many days after the journey's first departure this leg
	// leaves. A flight that lands the next morning pushes its connection over.
	DayOffset int `json:"day_offset"`
}

// Journey is an itinerary from an origin to a destination.
//
// An interline journey is one whose legs are operated by different carriers.
// That is the case worth demonstrating: it produces a single passenger name
// record that two carriers each hold a piece of, which is the entire reason
// interline messaging exists.
type Journey struct {
	Origin      string   `json:"origin"`
	Destination string   `json:"destination"`
	Via         string   `json:"via,omitempty"`
	Legs        []Leg    `json:"legs"`
	Carriers    []string `json:"carriers"`
	// Interline reports whether more than one carrier is involved.
	Interline bool `json:"interline"`
	// ConnectMinutes is the ground time at the connecting point.
	ConnectMinutes int `json:"connect_minutes"`
}

// Label renders the journey for a menu.
func (j Journey) Label() string {
	s := fmt.Sprintf("%s–%s", j.Origin, j.Destination)
	if j.Via != "" {
		s += " via " + j.Via
	}
	for _, l := range j.Legs {
		s += fmt.Sprintf("  %s%s", l.Carrier, l.FlightNum)
	}
	if j.Interline {
		s += "  [interline]"
	}
	return s
}

// hhmm parses an HHMM clock time into minutes past midnight.
func hhmm(s string) (int, bool) {
	if len(s) != 4 {
		return 0, false
	}
	h, err1 := strconv.Atoi(s[:2])
	m, err2 := strconv.Atoi(s[2:])
	if err1 != nil || err2 != nil || h > 23 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

// arrivalMinutes returns the arrival time in minutes from the departure day's
// midnight, accounting for a flight that lands the following day.
func arrivalMinutes(r Route) (dep, arr int, ok bool) {
	dep, ok1 := hhmm(r.Depart)
	arr, ok2 := hhmm(r.Arr)
	if !ok1 || !ok2 {
		return 0, 0, false
	}
	if arr <= dep {
		arr += 24 * 60 // landed the next day
	}
	return dep, arr, true
}

// Journeys enumerates the itineraries the demo schedule can produce: every
// non-stop, and every single-connection journey whose ground time is plausible.
//
// Interline journeys are listed first, because they are the ones worth looking
// at: a booking that spans two carriers exercises two links, two dialects and
// two record locators against one record.
func Journeys() []Journey {
	var out []Journey

	for _, r := range Schedule {
		out = append(out, Journey{
			Origin: r.Board, Destination: r.Off,
			Legs: []Leg{{Carrier: r.Carrier, FlightNum: r.Number, Board: r.Board,
				Off: r.Off, Depart: r.Depart, Arrive: r.Arr}},
			Carriers: []string{r.Carrier},
		})
	}

	for _, first := range Schedule {
		_, arr, ok := arrivalMinutes(first)
		if !ok {
			continue
		}
		for _, second := range Schedule {
			if second.Board != first.Off || second.Off == first.Board {
				continue
			}
			dep2, _, ok := arrivalMinutes(second)
			if !ok {
				continue
			}
			// The connecting flight may leave the day the first one lands, or
			// the day after if that is the only way to make the minimum.
			offset := arr / (24 * 60)
			connect := (offset*24*60 + dep2) - arr
			if connect < int(MinConnect.Minutes()) {
				offset++
				connect += 24 * 60
			}
			if connect < int(MinConnect.Minutes()) || connect > int(MaxConnect.Minutes()) {
				continue
			}
			carriers := []string{first.Carrier}
			if second.Carrier != first.Carrier {
				carriers = append(carriers, second.Carrier)
			}
			out = append(out, Journey{
				Origin: first.Board, Destination: second.Off, Via: first.Off,
				Legs: []Leg{
					{Carrier: first.Carrier, FlightNum: first.Number, Board: first.Board,
						Off: first.Off, Depart: first.Depart, Arrive: first.Arr},
					{Carrier: second.Carrier, FlightNum: second.Number, Board: second.Board,
						Off: second.Off, Depart: second.Depart, Arrive: second.Arr,
						DayOffset: offset},
				},
				Carriers:       carriers,
				Interline:      len(carriers) > 1,
				ConnectMinutes: connect,
			})
		}
	}

	// Interline first, then by origin and destination, so the list is stable
	// and the interesting cases are at the top.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Interline != out[j].Interline {
			return out[i].Interline
		}
		if out[i].Origin != out[j].Origin {
			return out[i].Origin < out[j].Origin
		}
		if out[i].Destination != out[j].Destination {
			return out[i].Destination < out[j].Destination
		}
		return out[i].Via < out[j].Via
	})
	return out
}

// InterlineJourneys returns only the multi-carrier itineraries.
func InterlineJourneys() []Journey {
	var out []Journey
	for _, j := range Journeys() {
		if j.Interline {
			out = append(out, j)
		}
	}
	return out
}
