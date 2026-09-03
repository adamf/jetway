package baggage

import (
	"fmt"
	"strings"
)

// Tracing is the bag office's side of the mishandled bag: when a passenger
// arrives and a bag does not, the station opens a delayed-bag file (AHL,
// "advise if hold") naming the passenger, the tag, the bag's colour and
// type and the routing, and every station holding a bag nobody has claimed
// opens an on-hand file (OHD); the two are matched on the tag, and the
// holding station forwards the bag (FWD) to where the passenger is. This
// is the shape of the industry's tracing system as its published training
// material describes it -- the file kinds, the element codes NM, TN, CT,
// RT, FD, FL, TX -- laid out here as the dotted element lines the rest of
// the bag messages use. The vendor's own record formats were not
// available, so this is this package's profile, and it says so.

// Tracing file kinds.
const (
	KindAHL Kind = "AHL" // delayed bag: the passenger has arrived, the bag has not
	KindOHD Kind = "OHD" // on hand: a bag nobody has claimed
	KindFWD Kind = "FWD" // forward: the bag is on its way to the passenger
)

// TracingFile is one AHL, OHD or FWD.
type TracingFile struct {
	Kind Kind
	// Reference is the file reference the opening station assigns, station
	// and carrier and a serial: LHRBA12345.
	Reference string
	// Station is the office holding the file; Carrier the airline.
	Station, Carrier string
	// Tags are the bags concerned.
	Tags []Tag
	// Surname and Givens name the passenger; blank on an on-hand file for
	// a bag with no name on it.
	Surname string
	Givens  []string
	// Colour and Type describe the bag as the IATA colour/type codes do
	// (BK22: black, upright hard-shell) -- the matching elements.
	ColourType string
	// Routing is the itinerary's stations in order; Flights the flights
	// and dates the bag was to travel or is travelling on.
	Routing []string
	Flights []FlightLeg
	// Contact is how to reach the passenger; Text free text.
	Contact string
	Text    string
	// ForwardTo is the station a FWD sends the bag to, and Matches the
	// file it answers (an OHD matched to an AHL, a FWD to the AHL).
	ForwardTo string
	Matches   string
	Elements  []string
}

// IsTracing reports whether the text is a tracing file.
func IsTracing(text string) bool {
	first := firstLine(text)
	for _, k := range []Kind{KindAHL, KindOHD, KindFWD} {
		if first == string(k) || strings.HasPrefix(first, string(k)+" ") {
			return true
		}
	}
	return false
}

// BuildTracing renders a file.
func BuildTracing(f *TracingFile) (string, error) {
	switch f.Kind {
	case KindAHL, KindOHD, KindFWD:
	default:
		return "", fmt.Errorf("baggage: tracing kind must be AHL, OHD or FWD, not %q", f.Kind)
	}
	if len(f.Tags) == 0 && f.Kind != KindAHL {
		return "", fmt.Errorf("baggage: an on-hand or forward file names a bag")
	}
	if f.Reference == "" {
		return "", fmt.Errorf("baggage: a tracing file has a reference")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n", f.Kind, f.Reference)
	if f.Station != "" || f.Carrier != "" {
		fmt.Fprintf(&b, ".V/%s%s\n", f.Station, f.Carrier)
	}
	if f.Surname != "" {
		s := ".NM/" + f.Surname
		for _, g := range f.Givens {
			s += "/" + g
		}
		b.WriteString(s + "\n")
	}
	for _, t := range f.Tags {
		count := t.Count
		if count <= 0 {
			count = 1
		}
		fmt.Fprintf(&b, ".TN/%s%03d\n", t.Number, count)
	}
	if f.ColourType != "" {
		fmt.Fprintf(&b, ".CT/%s\n", f.ColourType)
	}
	if len(f.Routing) > 0 {
		fmt.Fprintf(&b, ".RT/%s\n", strings.Join(f.Routing, "/"))
	}
	for _, l := range f.Flights {
		s := ".FD/" + l.Flight + "/" + l.Date
		if l.City != "" {
			s += "/" + l.City
		}
		b.WriteString(s + "\n")
	}
	if f.ForwardTo != "" {
		fmt.Fprintf(&b, ".FW/%s\n", f.ForwardTo)
	}
	if f.Matches != "" {
		fmt.Fprintf(&b, ".MR/%s\n", f.Matches)
	}
	if f.Contact != "" {
		fmt.Fprintf(&b, ".PN/%s\n", f.Contact)
	}
	if f.Text != "" {
		fmt.Fprintf(&b, ".TX/%s\n", f.Text)
	}
	for _, e := range f.Elements {
		b.WriteString(e + "\n")
	}
	fmt.Fprintf(&b, "END%s", f.Kind)
	return b.String(), nil
}

// ParseTracing reads a file. Elements it does not know are kept verbatim.
func ParseTracing(text string) (*TracingFile, error) {
	var clean []string
	for _, ln := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if t := strings.TrimSpace(ln); t != "" {
			clean = append(clean, t)
		}
	}
	if len(clean) < 2 {
		return nil, fmt.Errorf("baggage: tracing file too short")
	}
	head := strings.Fields(clean[0])
	f := &TracingFile{Kind: Kind(head[0])}
	switch f.Kind {
	case KindAHL, KindOHD, KindFWD:
	default:
		return nil, fmt.Errorf("baggage: identifier %q is not AHL, OHD or FWD", head[0])
	}
	if len(head) > 1 {
		f.Reference = head[1]
	}
	for _, ln := range clean[1:] {
		switch {
		case ln == "END"+string(f.Kind):
			if f.Reference == "" {
				return nil, fmt.Errorf("baggage: tracing file carries no reference")
			}
			return f, nil
		case strings.HasPrefix(ln, ".V/"):
			v := ln[3:]
			if len(v) >= 3 {
				f.Station, f.Carrier = v[:3], v[3:]
			}
		case strings.HasPrefix(ln, ".NM/"):
			parts := strings.Split(ln[4:], "/")
			f.Surname = parts[0]
			f.Givens = append(f.Givens, parts[1:]...)
		case strings.HasPrefix(ln, ".TN/"):
			tag, err := parseTag(".N/" + ln[4:])
			if err != nil {
				return nil, err
			}
			f.Tags = append(f.Tags, tag)
		case strings.HasPrefix(ln, ".CT/"):
			f.ColourType = ln[4:]
		case strings.HasPrefix(ln, ".RT/"):
			f.Routing = strings.Split(ln[4:], "/")
		case strings.HasPrefix(ln, ".FD/"):
			parts := strings.Split(ln[4:], "/")
			l := FlightLeg{Flight: parts[0]}
			if len(parts) > 1 {
				l.Date = parts[1]
			}
			if len(parts) > 2 {
				l.City = parts[2]
			}
			f.Flights = append(f.Flights, l)
		case strings.HasPrefix(ln, ".FW/"):
			f.ForwardTo = ln[4:]
		case strings.HasPrefix(ln, ".MR/"):
			f.Matches = ln[4:]
		case strings.HasPrefix(ln, ".PN/"):
			f.Contact = ln[4:]
		case strings.HasPrefix(ln, ".TX/"):
			f.Text = ln[4:]
		default:
			f.Elements = append(f.Elements, ln)
		}
	}
	return nil, fmt.Errorf("baggage: tracing file has no END%s", f.Kind)
}

// Match reports whether an on-hand file answers a delayed-bag file: a tag
// they share, or failing tags the same surname on a bag of the same
// colour and type -- the order the tracing systems match in.
func Match(ahl, ohd *TracingFile) bool {
	if ahl == nil || ohd == nil || ahl.Kind != KindAHL || ohd.Kind != KindOHD {
		return false
	}
	for _, a := range ahl.Tags {
		for _, o := range ohd.Tags {
			if a.Number != "" && a.Number == o.Number {
				return true
			}
		}
	}
	return ahl.Surname != "" && strings.EqualFold(ahl.Surname, ohd.Surname) && ahl.ColourType != "" && ahl.ColourType == ohd.ColourType
}
