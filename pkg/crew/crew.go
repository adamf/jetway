// Package crew is flight crew legality: the flight time and flight duty
// period limits a scheduler checks before a crew is given a leg and a
// dispatcher checks again when the day runs late.
//
// The rules are 14 CFR Part 117 (public law): Table A's flight time limits
// and Table B's flight duty period limits for unaugmented operations, by
// the acclimated local time the duty starts and the number of flight
// segments, the two-hour extension of §117.19 for unforeseen circumstances
// before take-off, and the ten-hour rest of §117.25. Other regimes (EASA
// ORO.FTL, CAP 371) differ in the tables, not the shape; a Rules value
// carries the tables, and Part117 is the one shipped.
package crew

import (
	"fmt"
	"time"
)

// Rules is one regime's limits.
type Rules struct {
	Name string
	// FDP is the flight duty period table: rows by the hour of report
	// (acclimated local), columns by segments 1-2, 3, 4, 5, 6, 7+; values
	// in hours.
	FDP func(reportHour, reportMinute int) [6]float64
	// FlightTime is the maximum block time for a duty starting at the hour.
	FlightTime func(reportHour, reportMinute int) float64
	// Extension is how far the pilot in command may extend the FDP before
	// take-off; Rest the minimum between duties.
	Extension time.Duration
	Rest      time.Duration
}

// Part117 is 14 CFR Part 117 as published: Tables A and B, §117.19(a),
// §117.25(e).
var Part117 = Rules{
	Name: "14 CFR 117",
	FDP: func(h, m int) [6]float64 {
		t := h*100 + m
		switch {
		case t < 400:
			return [6]float64{9, 9, 9, 9, 9, 9}
		case t < 500:
			return [6]float64{10, 10, 10, 9, 9, 9}
		case t < 600:
			return [6]float64{12, 12, 12, 11.5, 11, 10.5}
		case t < 700:
			return [6]float64{13, 12, 12, 11.5, 11, 10.5}
		case t < 1200:
			return [6]float64{14, 13, 13, 12.5, 12, 11.5}
		case t < 1300:
			return [6]float64{13, 13, 13, 12.5, 12, 11.5}
		case t < 1700:
			return [6]float64{12, 12, 12, 11.5, 11, 10.5}
		case t < 2200:
			return [6]float64{12, 11, 11, 10, 9, 9}
		case t < 2300:
			return [6]float64{11, 10, 10, 9, 9, 9}
		default:
			return [6]float64{10, 10, 9, 9, 9, 9}
		}
	},
	FlightTime: func(h, m int) float64 {
		t := h*100 + m
		switch {
		case t < 500, t >= 2000:
			return 8
		default:
			return 9
		}
	},
	Extension: 2 * time.Hour,
	Rest:      10 * time.Hour,
}

// FDPLimit is the maximum flight duty period for a duty reporting at the
// given acclimated local time with the given number of flight segments.
func (r Rules) FDPLimit(report time.Time, segments int) time.Duration {
	row := r.FDP(report.Hour(), report.Minute())
	col := segments - 2
	if col < 0 {
		col = 0
	}
	if col > 5 {
		col = 5
	}
	return time.Duration(row[col] * float64(time.Hour))
}

// FlightTimeLimit is the maximum block time in one duty reporting at the
// given time.
func (r Rules) FlightTimeLimit(report time.Time) time.Duration {
	return time.Duration(r.FlightTime(report.Hour(), report.Minute()) * float64(time.Hour))
}

// Leg is one flight segment of a duty as planned or as it went.
type Leg struct {
	Flight string
	// Depart and Arrive are the leg's times as now expected: scheduled at
	// planning, then revised as the day runs. Block is Arrive - Depart.
	Depart, Arrive time.Time
}

// Duty is one crew's flight duty period: a report time and the legs it
// covers. The report is the first leg's departure less the report lead.
type Duty struct {
	Rules Rules
	// Report is when the duty began, acclimated local time.
	Report time.Time
	// Release is the buffer after the last block-in before the crew is
	// released, e.g. 15 minutes; the FDP ends at release.
	Release time.Duration
	Legs    []Leg
}

// Verdict is what the legality check says about a duty.
type Verdict struct {
	Legal bool
	// FDP and FDPLimit are the duty as planned and the table's limit;
	// Extended says the two-hour extension was needed to make it legal.
	FDP, FDPLimit   time.Duration
	Extended        bool
	Flight, FTLimit time.Duration
	Reason          string
}

// Check says whether the duty, with its legs at their current times, may be
// flown: the FDP from report to the last arrival plus release within Table
// B (or within it extended, which the verdict says), and the block time
// within Table A.
func (d Duty) Check() Verdict {
	v := Verdict{}
	if len(d.Legs) == 0 {
		v.Legal = true
		return v
	}
	last := d.Legs[0].Arrive
	var block time.Duration
	for _, l := range d.Legs {
		if l.Arrive.After(last) {
			last = l.Arrive
		}
		block += l.Arrive.Sub(l.Depart)
	}
	v.FDP = last.Add(d.Release).Sub(d.Report)
	v.FDPLimit = d.Rules.FDPLimit(d.Report, len(d.Legs))
	v.Flight, v.FTLimit = block, d.Rules.FlightTimeLimit(d.Report)
	switch {
	case v.Flight > v.FTLimit:
		v.Reason = fmt.Sprintf("block %s over the %s flight time limit", v.Flight.Round(time.Minute), v.FTLimit)
	case v.FDP > v.FDPLimit+d.Rules.Extension:
		v.Reason = fmt.Sprintf("duty %s over the %s limit even extended", v.FDP.Round(time.Minute), v.FDPLimit)
	case v.FDP > v.FDPLimit:
		v.Legal, v.Extended = true, true
	default:
		v.Legal = true
	}
	return v
}

// LatestDeparture is the latest the last leg may depart and still be
// legal, given its block time: the point past which the crew times out.
func (d Duty) LatestDeparture() (time.Time, bool) {
	if len(d.Legs) == 0 {
		return time.Time{}, false
	}
	last := d.Legs[len(d.Legs)-1]
	limit := d.Rules.FDPLimit(d.Report, len(d.Legs)) + d.Rules.Extension
	return d.Report.Add(limit).Add(-d.Release).Add(-last.Arrive.Sub(last.Depart)), true
}
