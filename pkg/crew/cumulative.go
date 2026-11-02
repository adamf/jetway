package crew

import (
	"fmt"
	"sort"
	"time"
)

// Measure is what a cumulative limit adds up: block time or flight duty
// period time.
type Measure int

const (
	// MeasureFlight adds block time: the legs from out to in.
	MeasureFlight Measure = iota
	// MeasureDuty adds flight duty period time: report to release.
	MeasureDuty
)

// Cumulative is one look-back limit: no more than Limit of the measure in
// any Span of consecutive hours, or, when CalendarDays is set, in any
// period of that many consecutive calendar days.
type Cumulative struct {
	Name         string
	Of           Measure
	Limit        time.Duration
	Span         time.Duration
	CalendarDays int
	// Cite names the rule, for the verdict's reason.
	Cite string
}

// window is the look-back the limit takes when it ends at t.
func (c Cumulative) window(t time.Time) (from, to time.Time) {
	if c.CalendarDays > 0 {
		y, m, d := t.Date()
		end := time.Date(y, m, d, 0, 0, 0, 0, t.Location()).AddDate(0, 0, 1)
		return end.AddDate(0, 0, -c.CalendarDays), end
	}
	return t.Add(-c.Span), t
}

// Part117Cumulative is §117.23(b) and (c) as published.
var Part117Cumulative = []Cumulative{
	{Name: "flight time in 672 hours", Of: MeasureFlight, Limit: 100 * time.Hour, Span: 672 * time.Hour, Cite: "14 CFR 117.23(b)(1)"},
	{Name: "flight time in 365 days", Of: MeasureFlight, Limit: 1000 * time.Hour, CalendarDays: 365, Cite: "14 CFR 117.23(b)(2)"},
	{Name: "FDP in 168 hours", Of: MeasureDuty, Limit: 60 * time.Hour, Span: 168 * time.Hour, Cite: "14 CFR 117.23(c)(1)"},
	{Name: "FDP in 672 hours", Of: MeasureDuty, Limit: 190 * time.Hour, Span: 672 * time.Hour, Cite: "14 CFR 117.23(c)(2)"},
}

// History is a crew member's past duty as a tracking system holds it. A
// carrier's own crew tracking store implements it; Record is the plain
// one, every period kept.
type History interface {
	// FlightTime is the block time flown inside [from, to).
	FlightTime(from, to time.Time) time.Duration
	// DutyTime is the flight duty period time inside [from, to).
	DutyTime(from, to time.Time) time.Duration
	// LastDuty is the last duty of any kind that ended at or before the
	// time: its report and its release.
	LastDuty(before time.Time) (report, release time.Time, ok bool)
	// LongestFree is the longest stretch inside [from, to) free of all
	// duty, reserve included.
	LongestFree(from, to time.Time) time.Duration
}

// Total is one cumulative limit as a duty would leave it.
type Total struct {
	Name  string
	Used  time.Duration
	Limit time.Duration
	Cite  string
}

// Remaining is the headroom under the limit.
func (t Total) Remaining() time.Duration {
	if t.Used >= t.Limit {
		return 0
	}
	return t.Limit - t.Used
}

// Admits says whether a crew member with the given history may fly the
// duty: the duty's own limits (Check), the rest immediately before it
// (§117.25(e)), the free time in the week before it (§117.25(b)), and
// every cumulative limit with the duty added (§117.23). The verdict's
// Totals are the cumulative limits at the duty's worst point.
//
// The check runs on the times the duty's legs carry: scheduled times at
// planning, revised times on the day. A duty that is legal as planned and
// pushed over a cumulative limit by delay is not legal: §117.23 has no
// extension.
func (d Duty) Admits(h History) Verdict {
	v := d.Check()
	if h == nil || len(d.Legs) == 0 {
		return v
	}
	if !v.Legal {
		return v
	}
	if _, release, ok := h.LastDuty(d.Report); ok && d.Rules.Rest > 0 {
		if rest := d.Report.Sub(release); rest < d.Rules.Rest {
			v.Legal, v.Extended = false, false
			v.Reason = fmt.Sprintf("rest of %s before report, under the %s required (14 CFR 117.25(e))", rest.Round(time.Minute), d.Rules.Rest)
			return v
		}
	}
	if d.Rules.FreeTime > 0 && d.Rules.FreeWindow > 0 {
		if free := h.LongestFree(d.Report.Add(-d.Rules.FreeWindow), d.Report); free < d.Rules.FreeTime {
			v.Legal, v.Extended = false, false
			v.Reason = fmt.Sprintf("longest time free of duty in the %s before report is %s, under %s (14 CFR 117.25(b))", d.Rules.FreeWindow, free.Round(time.Minute), d.Rules.FreeTime)
			return v
		}
	}
	v.Totals = d.Totals(h)
	for _, t := range v.Totals {
		if t.Used > t.Limit {
			v.Legal, v.Extended = false, false
			v.Reason = fmt.Sprintf("%s would be %s, over the %s limit (%s)", t.Name, fmtHours(t.Used), fmtHours(t.Limit), t.Cite)
			return v
		}
	}
	return v
}

// Totals is every cumulative limit of the rules with the duty added, each
// at the point of the duty where it is highest: block time at a leg's
// arrival, duty time at release. Between those points the history leaves
// the window at most as fast as the duty adds to it, so the worst point is
// always one of them.
func (d Duty) Totals(h History) []Total {
	if len(d.Legs) == 0 {
		return nil
	}
	release := d.Legs[0].Arrive
	for _, l := range d.Legs {
		if l.Arrive.After(release) {
			release = l.Arrive
		}
	}
	release = release.Add(d.Release)
	out := make([]Total, 0, len(d.Rules.Cumulative))
	for _, c := range d.Rules.Cumulative {
		t := Total{Name: c.Name, Limit: c.Limit, Cite: c.Cite}
		switch c.Of {
		case MeasureFlight:
			for _, at := range d.Legs {
				from, to := c.window(at.Arrive)
				used := d.blockIn(from, to)
				if h != nil {
					used += h.FlightTime(from, to)
				}
				t.Used = max(t.Used, used)
			}
		case MeasureDuty:
			from, to := c.window(release)
			used := overlap(d.Report, release, from, to)
			if h != nil {
				used += h.DutyTime(from, to)
			}
			t.Used = used
		}
		out = append(out, t)
	}
	return out
}

// Remaining is the headroom under every cumulative limit for a crew member
// at the given time, with no duty added: how much more the member may fly
// and be on duty before a limit stops them.
func (r Rules) Remaining(h History, at time.Time) []Total {
	out := make([]Total, 0, len(r.Cumulative))
	for _, c := range r.Cumulative {
		from, to := c.window(at)
		t := Total{Name: c.Name, Limit: c.Limit, Cite: c.Cite}
		if h != nil {
			if c.Of == MeasureFlight {
				t.Used = h.FlightTime(from, to)
			} else {
				t.Used = h.DutyTime(from, to)
			}
		}
		out = append(out, t)
	}
	return out
}

// blockIn is the duty's own block time inside [from, to).
func (d Duty) blockIn(from, to time.Time) time.Duration {
	var sum time.Duration
	for _, l := range d.Legs {
		sum += overlap(l.Depart, l.Arrive, from, to)
	}
	return sum
}

// overlap is the length of [a, b) inside [from, to).
func overlap(a, b, from, to time.Time) time.Duration {
	if a.Before(from) {
		a = from
	}
	if b.After(to) {
		b = to
	}
	if !b.After(a) {
		return 0
	}
	return b.Sub(a)
}

func fmtHours(d time.Duration) string {
	h := int(d / time.Hour)
	m := int((d % time.Hour) / time.Minute)
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh%02d", h, m)
}

// Kind is what a period of a record was.
type Kind int

const (
	// FlightDuty is a flight duty period: it counts toward the FDP limits.
	FlightDuty Kind = iota
	// Reserve is a reserve availability period: duty for rest, not FDP.
	Reserve
	// OtherDuty is training, office or deadhead time: duty, not FDP.
	OtherDuty
)

// Period is one duty in a crew member's record.
type Period struct {
	Kind            Kind
	Report, Release time.Time
	// Legs are the blocks flown in the period, at their actual times.
	Legs []Leg
}

// Record is a crew member's past as a list of periods: the exact form of
// History. A tracking system adds each duty as it is released and trims
// what no limit can reach any more.
type Record struct {
	Periods []Period
}

// Add files a period in order.
func (r *Record) Add(p Period) {
	i := sort.Search(len(r.Periods), func(i int) bool { return r.Periods[i].Report.After(p.Report) })
	r.Periods = append(r.Periods, Period{})
	copy(r.Periods[i+1:], r.Periods[i:])
	r.Periods[i] = p
}

// AddDuty files a duty as flown: its report, its legs, and its release
// after the last arrival.
func (r *Record) AddDuty(d Duty) {
	if len(d.Legs) == 0 {
		return
	}
	last := d.Legs[0].Arrive
	for _, l := range d.Legs {
		if l.Arrive.After(last) {
			last = l.Arrive
		}
	}
	r.Add(Period{Kind: FlightDuty, Report: d.Report, Release: last.Add(d.Release), Legs: append([]Leg(nil), d.Legs...)})
}

// Trim drops the periods released before the time. No Part 117 limit
// looks back further than 365 calendar days.
func (r *Record) Trim(before time.Time) {
	keep := r.Periods[:0]
	for _, p := range r.Periods {
		if !p.Release.Before(before) {
			keep = append(keep, p)
		}
	}
	r.Periods = keep
}

// FlightTime implements History.
func (r *Record) FlightTime(from, to time.Time) time.Duration {
	var sum time.Duration
	for _, p := range r.Periods {
		for _, l := range p.Legs {
			sum += overlap(l.Depart, l.Arrive, from, to)
		}
	}
	return sum
}

// DutyTime implements History: flight duty periods only.
func (r *Record) DutyTime(from, to time.Time) time.Duration {
	var sum time.Duration
	for _, p := range r.Periods {
		if p.Kind == FlightDuty {
			sum += overlap(p.Report, p.Release, from, to)
		}
	}
	return sum
}

// LastDuty implements History.
func (r *Record) LastDuty(before time.Time) (report, release time.Time, ok bool) {
	for _, p := range r.Periods {
		if p.Release.After(before) {
			continue
		}
		if !ok || p.Release.After(release) {
			report, release, ok = p.Report, p.Release, true
		}
	}
	return report, release, ok
}

// LongestFree implements History: every period is duty.
func (r *Record) LongestFree(from, to time.Time) time.Duration {
	return LongestFree(r.Periods, from, to)
}

// LongestFree is the longest gap inside [from, to) between the periods,
// which need not be sorted or disjoint.
func LongestFree(periods []Period, from, to time.Time) time.Duration {
	type span struct{ a, b time.Time }
	var busy []span
	for _, p := range periods {
		if p.Release.After(from) && p.Report.Before(to) {
			busy = append(busy, span{p.Report, p.Release})
		}
	}
	sort.Slice(busy, func(i, j int) bool { return busy[i].a.Before(busy[j].a) })
	best := time.Duration(0)
	cursor := from
	for _, s := range busy {
		if s.a.After(cursor) {
			best = max(best, s.a.Sub(cursor))
		}
		if s.b.After(cursor) {
			cursor = s.b
		}
	}
	if to.After(cursor) {
		best = max(best, to.Sub(cursor))
	}
	return best
}
