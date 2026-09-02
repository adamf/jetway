// Package pnl implements the Passenger Name List and the Additions and
// Deletions List: the messages a reservations system sends the airport so
// check-in knows who is coming.
//
// The formats are IATA Recommended Practices 1707/1708. The practices
// themselves are paywalled; this implementation is reconstructed from freely
// published vendor documentation and worked examples, which agree on the
// shape: a header naming the flight, date, boarding point and part number;
// groups headed by destination, seat count and booking class; name items of
// party size, surname and given names; dotted elements carrying the record
// locator and services; and an END line that says whether more parts follow.
// Element coverage is the commonly published core, not the full directory.
package pnl

import (
	"fmt"
	"strconv"
	"strings"
)

// Kind distinguishes the full list from the update to it.
type Kind string

const (
	KindPNL Kind = "PNL"
	KindADL Kind = "ADL"
)

// Change is an ADL section marker. The practice fixes their order: deletions,
// then additions, then changes.
type Change string

const (
	ChangeDEL Change = "DEL"
	ChangeADD Change = "ADD"
	ChangeCHG Change = "CHG"
)

// Name is one name item: a party travelling together under one surname.
type Name struct {
	// Party is the leading count: 2COSTA/ANAMRS/TIAGOMSTR is two people.
	Party   int
	Surname string
	// Givens are the given-name-and-title runs, one per person, exactly as
	// the wire carries them: RUIMR, ANAMRS, TIAGOMSTR.
	Givens []string
	// Elements are the dotted items after the name, verbatim: .L/A1B2C3 is
	// the record locator, .R/VGML HK1 a service request. Kept as text
	// because the airport reads them forward; nothing here acts on them.
	Elements []string
}

// Group is one destination-and-class block of the list.
type Group struct {
	Dest  string
	Count int
	Class string
	// Names carries a PNL group's items.
	Names []Name
	// Sections carries an ADL group's items, in DEL, ADD, CHG order.
	Sections []Section
}

// Section is one ADL change block.
type Section struct {
	Change Change
	Names  []Name
}

// Message is one part of a name list.
type Message struct {
	Kind   Kind
	Flight string // e.g. BA0117
	Date   string // DDMMM
	Board  string
	Part   int
	// Final reports whether this part ends the list (ENDPNL/ENDADL) or more
	// parts follow (ENDPART n).
	Final  bool
	Groups []Group
}

// IsNameList reports whether a Type B text is a PNL or ADL.
func IsNameList(text string) bool {
	first := firstLine(text)
	return first == "PNL" || first == "ADL"
}

func firstLine(text string) string {
	for _, ln := range strings.Split(text, "\n") {
		if t := strings.TrimSpace(ln); t != "" {
			return t
		}
	}
	return ""
}

// Build renders one part.
func Build(m *Message) (string, error) {
	if m.Kind != KindPNL && m.Kind != KindADL {
		return "", fmt.Errorf("pnl: kind must be PNL or ADL, not %q", m.Kind)
	}
	if m.Flight == "" || m.Date == "" || m.Board == "" {
		return "", fmt.Errorf("pnl: flight, date and board point are all required")
	}
	part := m.Part
	if part <= 0 {
		part = 1
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", m.Kind)
	fmt.Fprintf(&b, "%s/%s %s PART%d\n", m.Flight, m.Date, m.Board, part)
	for _, g := range m.Groups {
		fmt.Fprintf(&b, "-%s%02d%s\n", g.Dest, g.Count, g.Class)
		if m.Kind == KindPNL {
			for _, n := range g.Names {
				b.WriteString(nameLine(n) + "\n")
			}
			continue
		}
		for _, sec := range g.Sections {
			fmt.Fprintf(&b, "%s\n", sec.Change)
			for _, n := range sec.Names {
				b.WriteString(nameLine(n) + "\n")
			}
		}
	}
	if m.Final {
		fmt.Fprintf(&b, "END%s", m.Kind)
	} else {
		fmt.Fprintf(&b, "ENDPART%d", part)
	}
	return b.String(), nil
}

// NameLine renders one name item as the wire carries it, for the other
// list messages of the family -- final sales, ticket lists -- that share
// the form.
func NameLine(n Name) string { return nameLine(n) }

// ParseName reads one name item. It is the same parser the list uses,
// exported for the messages that borrow the PNL name form.
func ParseName(t string) (Name, error) { return parseName(strings.TrimSpace(t)) }

func nameLine(n Name) string {
	party := n.Party
	if party <= 0 {
		party = max(1, len(n.Givens))
	}
	s := fmt.Sprintf("%d%s", party, n.Surname)
	for _, g := range n.Givens {
		s += "/" + g
	}
	for _, e := range n.Elements {
		s += " " + e
	}
	return s
}

// linesPerPart keeps each part inside the Type B 60-line envelope, leaving
// room for the address block a transmission system prepends.
const linesPerPart = 50

// BuildParts renders a whole list, partitioned so every part is a legal
// Type B message. Groups split across parts repeat their heading with the
// count of names carried in that part.
func BuildParts(kind Kind, flight, date, board string, groups []Group) ([]string, error) {
	var parts []string
	cur := &Message{Kind: kind, Flight: flight, Date: date, Board: board, Part: 1}
	lines := 2 // identifier and header

	flush := func(final bool) error {
		cur.Final = final
		text, err := Build(cur)
		if err != nil {
			return err
		}
		parts = append(parts, text)
		cur = &Message{Kind: kind, Flight: flight, Date: date, Board: board, Part: len(parts) + 1}
		lines = 2
		return nil
	}

	for _, g := range groups {
		if kind == KindPNL {
			pending := g.Names
			for len(pending) > 0 {
				room := linesPerPart - lines - 1 // group heading
				if room < 1 {
					if err := flush(false); err != nil {
						return nil, err
					}
					continue
				}
				take := min(room, len(pending))
				cur.Groups = append(cur.Groups, Group{
					Dest: g.Dest, Count: countOf(pending[:take]), Class: g.Class,
					Names: pending[:take],
				})
				lines += 1 + take
				pending = pending[take:]
			}
			continue
		}
		// ADL groups are small -- they carry a day's changes, not a cabin --
		// so they move whole; a group too big for the remaining room starts
		// the next part.
		need := 1
		for _, sec := range g.Sections {
			need += 1 + len(sec.Names)
		}
		if lines+need+1 > linesPerPart && len(cur.Groups) > 0 {
			if err := flush(false); err != nil {
				return nil, err
			}
		}
		cur.Groups = append(cur.Groups, g)
		lines += need
	}
	if err := flush(true); err != nil {
		return nil, err
	}
	return parts, nil
}

func countOf(names []Name) int {
	n := 0
	for _, x := range names {
		p := x.Party
		if p <= 0 {
			p = max(1, len(x.Givens))
		}
		n += p
	}
	return n
}

// Parse reads one part back into its structure.
func Parse(text string) (*Message, error) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var clean []string
	for _, ln := range lines {
		if t := strings.TrimRight(ln, " "); strings.TrimSpace(t) != "" {
			clean = append(clean, t)
		}
	}
	if len(clean) < 3 {
		return nil, fmt.Errorf("pnl: message too short to be a name list")
	}
	m := &Message{Kind: Kind(strings.TrimSpace(clean[0]))}
	if m.Kind != KindPNL && m.Kind != KindADL {
		return nil, fmt.Errorf("pnl: identifier %q is not PNL or ADL", clean[0])
	}
	// Header: BA0117/16DEC LHR PART1
	head := strings.Fields(clean[1])
	if len(head) < 2 {
		return nil, fmt.Errorf("pnl: header %q lacks flight and board point", clean[1])
	}
	fd := strings.SplitN(head[0], "/", 2)
	if len(fd) != 2 {
		return nil, fmt.Errorf("pnl: header %q lacks a /date", clean[1])
	}
	m.Flight, m.Date, m.Board = fd[0], fd[1], head[1]
	m.Part = 1
	for _, f := range head[2:] {
		if strings.HasPrefix(f, "PART") {
			if n, err := strconv.Atoi(f[4:]); err == nil {
				m.Part = n
			}
		}
	}

	var g *Group
	var sec *Section
	closeSection := func() {
		if g != nil && sec != nil {
			g.Sections = append(g.Sections, *sec)
			sec = nil
		}
	}
	closeGroup := func() {
		closeSection()
		if g != nil {
			m.Groups = append(m.Groups, *g)
			g = nil
		}
	}
	for _, ln := range clean[2:] {
		t := strings.TrimSpace(ln)
		switch {
		case t == "END"+string(m.Kind):
			m.Final = true
			closeGroup()
			return m, nil
		case strings.HasPrefix(t, "ENDPART"):
			m.Final = false
			closeGroup()
			return m, nil
		case strings.HasPrefix(t, "-"):
			closeGroup()
			ng, err := parseGroupHead(t)
			if err != nil {
				return nil, err
			}
			g = &ng
		case t == "DEL" || t == "ADD" || t == "CHG":
			if g == nil {
				return nil, fmt.Errorf("pnl: change marker %s before any group", t)
			}
			closeSection()
			sec = &Section{Change: Change(t)}
		default:
			n, err := parseName(t)
			if err != nil {
				return nil, err
			}
			switch {
			case sec != nil:
				sec.Names = append(sec.Names, n)
			case g != nil:
				g.Names = append(g.Names, n)
			default:
				return nil, fmt.Errorf("pnl: name %q before any group heading", t)
			}
		}
	}
	return nil, fmt.Errorf("pnl: message has no END line")
}

func parseGroupHead(t string) (Group, error) {
	// -OPO03Y: destination, zero-padded count, class. The count width varies
	// in published examples, so any digit run is accepted.
	body := strings.TrimPrefix(t, "-")
	if len(body) < 5 {
		return Group{}, fmt.Errorf("pnl: group heading %q is too short", t)
	}
	i := 3
	for i < len(body) && body[i] >= '0' && body[i] <= '9' {
		i++
	}
	count, err := strconv.Atoi(body[3:i])
	if err != nil || i == len(body) {
		return Group{}, fmt.Errorf("pnl: group heading %q lacks a count and class", t)
	}
	return Group{Dest: body[:3], Count: count, Class: body[i:]}, nil
}

func parseName(t string) (Name, error) {
	fields := strings.Fields(t)
	head := fields[0]
	i := 0
	for i < len(head) && head[i] >= '0' && head[i] <= '9' {
		i++
	}
	if i == 0 {
		return Name{}, fmt.Errorf("pnl: name item %q lacks its party count", t)
	}
	party, _ := strconv.Atoi(head[:i])
	parts := strings.Split(head[i:], "/")
	n := Name{Party: party, Surname: parts[0]}
	if n.Surname == "" {
		return Name{}, fmt.Errorf("pnl: name item %q lacks a surname", t)
	}
	n.Givens = append(n.Givens, parts[1:]...)
	// Everything after the name is elements; dotted items may carry spaces
	// (.R/VGML HK1), so split on the dots, not the blanks.
	rest := strings.TrimSpace(strings.TrimPrefix(t, head))
	for _, e := range splitElements(rest) {
		n.Elements = append(n.Elements, e)
	}
	return n, nil
}

func splitElements(rest string) []string {
	if rest == "" {
		return nil
	}
	var out []string
	start := -1
	for i := 0; i < len(rest); i++ {
		if rest[i] == '.' && (i == 0 || rest[i-1] == ' ') &&
			i+1 < len(rest) && rest[i+1] >= 'A' && rest[i+1] <= 'Z' {
			if start >= 0 {
				out = append(out, strings.TrimSpace(rest[start:i]))
			}
			start = i
		}
	}
	if start >= 0 {
		out = append(out, strings.TrimSpace(rest[start:]))
	} else if s := strings.TrimSpace(rest); s != "" {
		out = append(out, s)
	}
	return out
}
