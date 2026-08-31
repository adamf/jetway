package mvt

import (
	"fmt"
	"strings"
)

// Build renders the message as Type B text.
//
// Element order follows the published examples: identification, then actual
// movement, then estimates, then delays, then passengers, then the leg date,
// with free text last. A message built here parses back to the same message,
// and FuzzRoundTrip holds this package to that.
func (m *Message) Build() (string, error) {
	if m.Kind != KindMVT && m.Kind != KindMVA && m.Kind != KindDIV {
		return "", fmt.Errorf("mvt: %q is not a movement message identifier", m.Kind)
	}
	if m.Flight == "" || m.Day == "" || m.Registration == "" || m.Station == "" {
		return "", fmt.Errorf("mvt: the identification needs flight, day, registration and station")
	}
	id := fmt.Sprintf("%s/%s.%s.%s", m.Flight, m.Day, m.Registration, m.Station)
	if !idRe.MatchString(id) {
		return "", fmt.Errorf("mvt: identification %q does not fit the wire form", id)
	}

	var b strings.Builder
	if m.Correction {
		b.WriteString("COR ")
	}
	b.WriteString(string(m.Kind))
	b.WriteString("\n")
	b.WriteString(id)

	line := func(s string) { b.WriteString("\n" + s) }

	if m.AD != nil {
		dep := "AD" + m.AD.wire()
		if m.EO != "" {
			dep += " EO" + m.EO
		}
		if m.EA != nil {
			dep += " " + m.EA.wire()
		}
		line(dep)
	} else {
		if m.EO != "" {
			line("EO" + m.EO)
		}
		if m.EA != nil {
			line(m.EA.wire())
		}
	}
	if m.RR != "" {
		line("RR" + m.RR)
	}
	if m.FR != nil {
		line("FR" + m.FR.wire())
	}
	if m.AA != nil {
		line("AA" + m.AA.wire())
	}
	if m.ED != "" {
		line("ED" + m.ED)
	}
	if m.NI != "" {
		line("NI" + m.NI)
	}
	if m.EB != "" {
		line("EB" + m.EB)
	}
	if len(m.Delays) > 0 {
		line("DL" + wireDelays(m.Delays))
	}
	if len(m.ExtraDelays) > 0 {
		line("EDL" + wireDelays(m.ExtraDelays))
	}
	if len(m.SubCodes) > 0 {
		line("DLA" + strings.Join(m.SubCodes, "/"))
	}
	if m.DR != "" {
		dr := "DR" + m.DR
		if len(m.Pax) > 0 {
			dr += " " + wirePax(m.Pax)
		}
		line(dr)
	} else if len(m.Pax) > 0 {
		line(wirePax(m.Pax))
	}
	if m.FLD != "" {
		line("FLD" + m.FLD)
	}
	if m.SI != "" {
		for _, si := range strings.Split(m.SI, "\n") {
			line("SI " + si)
		}
	}
	return b.String(), nil
}

func (p *TimePair) wire() string {
	s := p.DayA + p.First
	if p.Second != "" {
		s += "/" + p.DayB + p.Second
	}
	return s
}

func (e *ETA) wire() string {
	s := "EA" + e.Day + e.Time
	if e.Airport != "" {
		s += " " + e.Airport
	}
	return s
}

func wireDelays(ds []Delay) string {
	codes := make([]string, 0, len(ds))
	durs := make([]string, 0, len(ds))
	for _, d := range ds {
		codes = append(codes, d.Code)
		durs = append(durs, d.Duration)
	}
	return strings.Join(codes, "/") + "/" + strings.Join(durs, "/")
}

func wirePax(px []int) string {
	parts := make([]string, 0, len(px))
	for _, n := range px {
		parts = append(parts, fmt.Sprintf("%d", n))
	}
	return "PX" + strings.Join(parts, "/")
}
