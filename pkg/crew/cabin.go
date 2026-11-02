package crew

import (
	"fmt"
	"time"
)

// CabinRules are flight attendant duty and rest limits. Unlike Part 117
// they limit the duty as scheduled: 14 CFR 121.467(b)(14) says a flight
// attendant is not over the limit when flights scheduled to end within it
// run late for reasons beyond the carrier's control, so the check is made
// on the schedule and the day's delays do not time a cabin crew out.
type CabinRules struct {
	Name string
	// MaxDuty is the longest scheduled duty period with the given number
	// of flight attendants above the minimum complement; outside48 says a
	// flight of the duty lands or takes off outside the 48 contiguous
	// states and the District of Columbia.
	MaxDuty func(extra int, outside48 bool) time.Duration
	// RestAfter is the scheduled rest a duty of the given length needs
	// before the next duty begins.
	RestAfter func(duty time.Duration) time.Duration
	// FreeTime is the time free of all duty a flight attendant must have
	// in any FreeDays consecutive calendar days.
	FreeTime time.Duration
	FreeDays int
}

// Part121Cabin is 14 CFR 121.467(b) as published: 14 hours of scheduled
// duty with the minimum complement, 16 with one flight attendant more, 18
// with two, 20 with three on a duty touching a point outside the 48
// contiguous states; 10 hours of rest after a duty of 14 hours or less and
// 12 after a longer one; 24 hours free of duty in any 7 consecutive
// calendar days. The reduced rest of (b)(8), 10 hours after a long duty
// when 14 follow, is not offered: a scheduler that wants it checks the
// later rest itself.
var Part121Cabin = CabinRules{
	Name: "14 CFR 121.467",
	MaxDuty: func(extra int, outside48 bool) time.Duration {
		switch {
		case extra >= 3 && outside48:
			return 20 * time.Hour
		case extra >= 2:
			return 18 * time.Hour
		case extra >= 1:
			return 16 * time.Hour
		default:
			return 14 * time.Hour
		}
	},
	RestAfter: func(duty time.Duration) time.Duration {
		if duty > 14*time.Hour {
			return 12 * time.Hour
		}
		return 10 * time.Hour
	},
	FreeTime: 24 * time.Hour,
	FreeDays: 7,
}

// Admits says whether a flight attendant with the given history may be
// scheduled to the duty: its scheduled length within MaxDuty, the rest
// since the last duty at least what that duty required, and a whole free
// day in the calendar week that ends with the duty's report.
func (c CabinRules) Admits(h History, d Duty, extra int, outside48 bool) Verdict {
	v := Verdict{Legal: true}
	if len(d.Legs) == 0 {
		return v
	}
	last := d.Legs[0].Arrive
	for _, l := range d.Legs {
		if l.Arrive.After(last) {
			last = l.Arrive
		}
		v.Flight += l.Arrive.Sub(l.Depart)
	}
	v.FDP = last.Add(d.Release).Sub(d.Report)
	v.FDPLimit = c.MaxDuty(extra, outside48)
	if v.FDP > v.FDPLimit {
		v.Legal = false
		v.Reason = fmt.Sprintf("scheduled duty %s over the %s limit (14 CFR 121.467(b))", v.FDP.Round(time.Minute), v.FDPLimit)
		return v
	}
	if h == nil {
		return v
	}
	if report, release, ok := h.LastDuty(d.Report); ok {
		need := c.RestAfter(release.Sub(report))
		if rest := d.Report.Sub(release); rest < need {
			v.Legal = false
			v.Reason = fmt.Sprintf("rest of %s before report, under the %s the last duty requires (14 CFR 121.467(b))", rest.Round(time.Minute), need)
			return v
		}
	}
	if c.FreeDays > 0 && c.FreeTime > 0 {
		y, m, dd := d.Report.Date()
		from := time.Date(y, m, dd, 0, 0, 0, 0, d.Report.Location()).AddDate(0, 0, 1-c.FreeDays)
		if free := h.LongestFree(from, d.Report); free < c.FreeTime {
			v.Legal = false
			v.Reason = fmt.Sprintf("longest time free of duty in the %d calendar days to report is %s, under %s (14 CFR 121.467(b)(13))", c.FreeDays, free.Round(time.Minute), c.FreeTime)
			return v
		}
	}
	return v
}

// MinFlightAttendants is the minimum number of flight attendants on a
// passenger aircraft with the given number of seats, by 14 CFR 121.391(a):
// one from 10 to 50 seats, two from 51 to 100, and above 100 two plus one
// for each unit or part of a unit of 50 seats over 100. Below 10 seats no
// flight attendant is required. The rule's payload distinction for 10 to
// 19 seats is not modelled: aircraft that small carry one.
func MinFlightAttendants(seats int) int {
	switch {
	case seats <= 9:
		return 0
	case seats <= 50:
		return 1
	case seats <= 100:
		return 2
	default:
		return 2 + (seats-100+49)/50
	}
}
