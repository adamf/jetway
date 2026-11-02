package crew

import (
	"fmt"
	"time"
)

// PayRules is the pay arithmetic of a labour agreement for one crew
// position: how a duty earns credit hours, and what the credit, the
// premiums and the time away from base are paid.
//
// The shape is the one North American crew agreements share. The American
// Airlines flight attendants' On-Duty Contract Guide (APFA, April 2016,
// section 11.D) states it plainly: a completed sequence receives the
// greater of flight time scheduled or actual, a 5-hour minimum per duty
// period, a trip rig of 1 hour for 3.5 away, and a duty rig of 1 hour for
// 2 on duty, prorated by the minute. Here the rule is applied per duty,
// which is the same for a one-duty sequence and an approximation for a
// longer one, where the agreement totals the sequence. Time flown past
// the scheduled release and duty forced onto a day off are paid at a
// premium; per diem is paid by the hour away from base. Every number is
// the agreement's, supplied by the caller. Monthly guarantees, line value
// and overtime above it are settled over a month and not here.
type PayRules struct {
	// Rate is the pay per credit hour, in the smallest currency unit.
	Rate int64
	// MinDay is the least credit a duty period earns.
	MinDay time.Duration
	// DutyRig is credit per hour on duty, report to release: 0.5 is 1:2.
	DutyRig float64
	// TripRig is credit per hour away from base: 1/3.5 is 1:3.5.
	TripRig float64
	// PayProtected credits the scheduled block when the duty flew less,
	// including a duty cancelled outright.
	PayProtected bool
	// ExtensionPremium multiplies the credit earned beyond the schedule's:
	// 1.5 pays it at time and a half. 0 or 1 pays it straight.
	ExtensionPremium float64
	// JuniorPremium multiplies a duty assigned on a day off: 2.0 is double.
	JuniorPremium float64
	// ReserveCredit is the credit for a reserve day on which the member is
	// not called.
	ReserveCredit time.Duration
	// PerDiem is paid per hour away from base, in the smallest unit.
	PerDiem int64
	// DeadheadShare is the credit a deadhead leg earns as a share of its
	// block: 1 is full credit, 0.5 half.
	DeadheadShare float64
}

// Assignment is one duty as a crew member was scheduled to it and flew it.
type Assignment struct {
	Scheduled Duty
	// Flown is the duty at its actual times, with the legs that did not
	// operate left out. No legs means the duty was cancelled.
	Flown Duty
	// Away is the time away from base this duty accounts for: report to
	// release at base, or to the end of the layover rest.
	Away time.Duration
	// Deadhead are legs ridden as a passenger to position.
	Deadhead []Leg
	// Junior says the duty was assigned on the member's day off.
	Junior bool
}

// Statement is what a duty pays and why.
type Statement struct {
	Block, Rig, Trip, MinDay, Credit time.Duration
	// Basis names which credit won: "block", "duty rig", "trip rig" or
	// "minimum day".
	Basis string
	// Pay is the credit at the rate; Premium the extension and junior
	// premiums on top; PerDiem the expense allowance; Total the sum.
	Pay, Premium, PerDiem, Total int64
	Lines                        []string
}

// Pay credits and prices one assignment.
func (p PayRules) Pay(a Assignment) Statement {
	var st Statement
	sched := dutyTimes(a.Scheduled)
	flown := dutyTimes(a.Flown)
	st.Block = flown.block
	if p.PayProtected && sched.block > st.Block {
		st.Block = sched.block
		st.Lines = append(st.Lines, fmt.Sprintf("pay protected to the scheduled %s block", fmtHours(sched.block)))
	}
	for _, l := range a.Deadhead {
		st.Block += time.Duration(float64(l.Arrive.Sub(l.Depart)) * p.DeadheadShare)
	}
	onDuty := flown.duty
	if p.PayProtected && sched.duty > onDuty {
		onDuty = sched.duty
	}
	st.Rig = time.Duration(float64(onDuty) * p.DutyRig)
	st.Trip = time.Duration(float64(a.Away) * p.TripRig)
	if len(a.Flown.Legs) > 0 || p.PayProtected {
		st.MinDay = p.MinDay
	}
	st.Credit, st.Basis = st.Block, "block"
	for _, c := range []struct {
		d     time.Duration
		basis string
	}{{st.Rig, "duty rig"}, {st.Trip, "trip rig"}, {st.MinDay, "minimum day"}} {
		if c.d > st.Credit {
			st.Credit, st.Basis = c.d, c.basis
		}
	}
	st.Pay = p.cost(st.Credit)
	// What the day earned beyond what the schedule would have: the
	// extension, paid at its premium.
	schedCredit := max(sched.block, time.Duration(float64(sched.duty)*p.DutyRig), p.MinDay)
	if extra := st.Credit - schedCredit; extra > 0 && p.ExtensionPremium > 1 && len(a.Flown.Legs) > 0 {
		prem := int64(float64(p.cost(extra)) * (p.ExtensionPremium - 1))
		st.Premium += prem
		st.Lines = append(st.Lines, fmt.Sprintf("%s beyond the schedule at %.1fx", fmtHours(extra), p.ExtensionPremium))
	}
	if a.Junior && p.JuniorPremium > 1 {
		prem := int64(float64(st.Pay) * (p.JuniorPremium - 1))
		st.Premium += prem
		st.Lines = append(st.Lines, fmt.Sprintf("assigned on a day off at %.1fx", p.JuniorPremium))
	}
	st.PerDiem = int64(float64(p.PerDiem) * a.Away.Hours())
	st.Total = st.Pay + st.Premium + st.PerDiem
	st.Lines = append([]string{fmt.Sprintf("credit %s by %s", fmtHours(st.Credit), st.Basis)}, st.Lines...)
	return st
}

// ReserveDay is what a reserve day pays when the member is not called.
func (p PayRules) ReserveDay() int64 { return p.cost(p.ReserveCredit) }

func (p PayRules) cost(d time.Duration) int64 {
	return int64(float64(p.Rate) * d.Hours())
}

type spans struct{ block, duty time.Duration }

// dutyTimes is a duty's block and its time from report to release.
func dutyTimes(d Duty) spans {
	var s spans
	if len(d.Legs) == 0 {
		return s
	}
	last := d.Legs[0].Arrive
	for _, l := range d.Legs {
		s.block += l.Arrive.Sub(l.Depart)
		if l.Arrive.After(last) {
			last = l.Arrive
		}
	}
	s.duty = last.Add(d.Release).Sub(d.Report)
	return s
}
