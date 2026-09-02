package dcs

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Section is one run of identical rows in a cabin.
type Section struct {
	// Compartment is the cabin code the industry uses on load messages: F,
	// C or Y. Booking classes map onto these; see Cabin.CompartmentFor.
	Compartment string `json:"compartment"`
	FromRow     int    `json:"from_row"`
	ToRow       int    `json:"to_row"`
	// Letters are the seat letters across one row, with a blank at each
	// aisle: "ABC DEF" is a single-aisle six-abreast row. RP 1710 skips I.
	Letters string `json:"letters"`
}

func (s Section) perRow() int { return len(strings.ReplaceAll(s.Letters, " ", "")) }

// CabinLayout is an aircraft type's seating.
type CabinLayout struct {
	Sections []Section `json:"sections"`
}

// Seats is the total seat count.
func (l CabinLayout) Seats() int {
	n := 0
	for _, s := range l.Sections {
		n += s.perRow() * (s.ToRow - s.FromRow + 1)
	}
	return n
}

// Version renders the configuration as the load messages name it: seats
// per compartment in F, C, Y order, e.g. C48Y312.
func (l CabinLayout) Version() string {
	counts := map[string]int{}
	for _, s := range l.Sections {
		counts[s.Compartment] += s.perRow() * (s.ToRow - s.FromRow + 1)
	}
	var b strings.Builder
	for _, c := range []string{"F", "C", "Y"} {
		if counts[c] > 0 {
			fmt.Fprintf(&b, "%s%d", c, counts[c])
		}
	}
	return b.String()
}

// Cabin is a layout with its occupancy: a seat map.
type Cabin struct {
	Layout CabinLayout `json:"layout"`
	// Occupied maps a seat designator to the passenger holding it.
	Occupied map[string]int `json:"occupied"`
}

func (l CabinLayout) instance() *Cabin {
	return &Cabin{Layout: l, Occupied: map[string]int{}}
}

// Seats is the cabin's seat count.
func (c *Cabin) Seats() int { return c.Layout.Seats() }

// reindex rebuilds occupancy from the passengers who hold seats.
func (c *Cabin) reindex(pax []*Passenger) {
	c.Occupied = map[string]int{}
	for _, p := range pax {
		if p.Seat != "" && p.Flying() {
			c.Occupied[p.Seat] = p.ID
		}
	}
}

// compartments lists the cabin codes present, in F, C, Y order.
func (c *Cabin) compartments() []string {
	have := map[string]bool{}
	for _, s := range c.Layout.Sections {
		have[s.Compartment] = true
	}
	var out []string
	for _, code := range []string{"F", "C", "Y"} {
		if have[code] {
			out = append(out, code)
		}
	}
	return out
}

// CompartmentFor maps a booking class to the cabin that seats it. First
// class codes go to F, business to C, everything else to Y; a cabin the
// aircraft does not have falls to the next one down, and an all-economy
// aircraft seats everyone in Y.
func (c *Cabin) CompartmentFor(class string) string {
	comps := c.compartments()
	has := func(code string) bool {
		for _, x := range comps {
			if x == code {
				return true
			}
		}
		return false
	}
	var want []string
	switch class {
	case "F", "A", "P", "R":
		want = []string{"F", "C", "Y"}
	case "J", "C", "D", "I", "Z":
		want = []string{"C", "Y", "F"}
	default:
		want = []string{"Y", "C", "F"}
	}
	for _, w := range want {
		if has(w) {
			return w
		}
	}
	if len(comps) > 0 {
		return comps[0]
	}
	return "Y"
}

// Free counts the unoccupied seats in a compartment; empty counts the cabin.
func (c *Cabin) Free(comp string) int {
	n := 0
	for _, s := range c.Layout.Sections {
		if comp != "" && s.Compartment != comp {
			continue
		}
		for row := s.FromRow; row <= s.ToRow; row++ {
			for _, l := range s.Letters {
				if l == ' ' {
					continue
				}
				if _, taken := c.Occupied[seatName(row, l)]; !taken {
					n++
				}
			}
		}
	}
	return n
}

func seatName(row int, letter rune) string { return strconv.Itoa(row) + string(letter) }

// Has reports whether a seat designator exists, and in which compartment.
func (c *Cabin) Has(seat string) (string, bool) {
	row, letter, ok := splitSeat(seat)
	if !ok {
		return "", false
	}
	for _, s := range c.Layout.Sections {
		if row < s.FromRow || row > s.ToRow {
			continue
		}
		if strings.ContainsRune(strings.ReplaceAll(s.Letters, " ", ""), letter) {
			return s.Compartment, true
		}
	}
	return "", false
}

func splitSeat(seat string) (int, rune, bool) {
	seat = strings.ToUpper(strings.TrimSpace(seat))
	if len(seat) < 2 {
		return 0, 0, false
	}
	row, err := strconv.Atoi(seat[:len(seat)-1])
	if err != nil {
		return 0, 0, false
	}
	return row, rune(seat[len(seat)-1]), true
}

// Assign finds n seats for a party in a compartment: together in one row if
// a row has room, otherwise the first free seats in cabin order. Seats are
// returned in row order and are not yet marked occupied; Take does that.
func (c *Cabin) Assign(comp string, n int) ([]string, error) {
	if n <= 0 {
		return nil, nil
	}
	// A block of adjacent seats in one row, walking the cabin front to back
	// within the compartment. Parties larger than a block take the largest
	// block and spill into the next row.
	var out []string
	remaining := n
	for _, s := range c.Layout.Sections {
		if s.Compartment != comp {
			continue
		}
		for row := s.FromRow; row <= s.ToRow && remaining > 0; row++ {
			for _, block := range strings.Fields(s.Letters) {
				var free []string
				for _, l := range block {
					seat := seatName(row, l)
					if _, taken := c.Occupied[seat]; !taken {
						free = append(free, seat)
					}
				}
				if len(free) >= remaining {
					out = append(out, free[:remaining]...)
					remaining = 0
					break
				}
			}
		}
		if remaining == 0 {
			break
		}
	}
	if remaining == 0 {
		return out, nil
	}
	// No row seats them together: take what is free, front to back.
	out = out[:0]
	remaining = n
	for _, s := range c.Layout.Sections {
		if s.Compartment != comp {
			continue
		}
		for row := s.FromRow; row <= s.ToRow && remaining > 0; row++ {
			for _, l := range s.Letters {
				if l == ' ' || remaining == 0 {
					continue
				}
				seat := seatName(row, l)
				if _, taken := c.Occupied[seat]; !taken {
					out = append(out, seat)
					remaining--
				}
			}
		}
	}
	if remaining > 0 {
		return nil, ErrNoSeat
	}
	return out, nil
}

// Take marks a seat occupied by a passenger.
func (c *Cabin) Take(seat string, id int) error {
	if _, ok := c.Has(seat); !ok {
		return ErrNoSuchSeat
	}
	if holder, taken := c.Occupied[seat]; taken && holder != id {
		return ErrSeatTaken
	}
	c.Occupied[seat] = id
	return nil
}

// Release frees a seat.
func (c *Cabin) Release(seat string) { delete(c.Occupied, seat) }

// Row is one row of the seat map as a display renders it.
type Row struct {
	Row         int      `json:"row"`
	Compartment string   `json:"compartment"`
	Seats       []string `json:"seats"` // letters, with "" at each aisle
}

// Rows renders the layout row by row, for a seat map display.
func (c *Cabin) Rows() []Row {
	var out []Row
	for _, s := range c.Layout.Sections {
		for row := s.FromRow; row <= s.ToRow; row++ {
			r := Row{Row: row, Compartment: s.Compartment}
			for _, l := range s.Letters {
				if l == ' ' {
					r.Seats = append(r.Seats, "")
				} else {
					r.Seats = append(r.Seats, string(l))
				}
			}
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Row < out[j].Row })
	return out
}

// zoneOf finds the row a seat is in, for weight and balance.
func rowOf(seat string) int {
	row, _, ok := splitSeat(seat)
	if !ok {
		return 0
	}
	return row
}
