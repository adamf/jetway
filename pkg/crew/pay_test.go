package crew

import (
	"testing"
	"time"
)

// An agreement shaped like the published North American ones: minimum
// day 5h15, duty rig 1:2, trip rig 1:3.5, pay protection, time and a half
// past the schedule, double for a day off, per diem by the hour.
var agreement = PayRules{
	Rate: 30000, MinDay: 5*time.Hour + 15*time.Minute, DutyRig: 0.5, TripRig: 1 / 3.5,
	PayProtected: true, ExtensionPremium: 1.5, JuniorPremium: 2, ReserveCredit: 4 * time.Hour,
	PerDiem: 250, DeadheadShare: 0.5,
}

func legs(spans ...[2]time.Time) []Leg {
	var out []Leg
	for _, s := range spans {
		out = append(out, Leg{Depart: s[0], Arrive: s[1]})
	}
	return out
}

func TestPayCreditsTheGreatest(t *testing.T) {
	// Two short legs, 3h of block in a duty of 06:00 to 15:15: the duty
	// rig is 4h37, the minimum day 5h15, which wins.
	short := Duty{Report: at(0, 6, 0), Release: 15 * time.Minute, Legs: legs(
		[2]time.Time{at(0, 7, 0), at(0, 8, 30)}, [2]time.Time{at(0, 13, 30), at(0, 15, 0)})}
	st := agreement.Pay(Assignment{Scheduled: short, Flown: short, Away: 9*time.Hour + 15*time.Minute})
	if st.Basis != "minimum day" || st.Credit != 5*time.Hour+15*time.Minute || st.Pay != 157500 {
		t.Errorf("short day: %+v", st)
	}
	if st.PerDiem != 2312 || st.Total != st.Pay+st.PerDiem {
		t.Errorf("per diem: %+v", st)
	}
	// One long leg: 7h of block wins.
	long := Duty{Report: at(0, 8, 0), Release: 15 * time.Minute, Legs: legs([2]time.Time{at(0, 9, 0), at(0, 16, 0)})}
	if st := agreement.Pay(Assignment{Scheduled: long, Flown: long}); st.Basis != "block" || st.Credit != 7*time.Hour {
		t.Errorf("long leg: %+v", st)
	}
	// A three-day trip of 70 hours away, one duty of it accounted for:
	// the trip rig credits 20 hours.
	if st := agreement.Pay(Assignment{Scheduled: long, Flown: long, Away: 70 * time.Hour}); st.Basis != "trip rig" || st.Credit != 20*time.Hour {
		t.Errorf("trip rig: %+v", st)
	}
}

func TestPayUnderDisruption(t *testing.T) {
	sched := Duty{Report: at(0, 8, 0), Release: 15 * time.Minute, Legs: legs([2]time.Time{at(0, 9, 0), at(0, 16, 0)})}
	// Three hours late: 7h of block at 12:00-19:00, duty 08:00-19:15 is
	// 11h15, rig 5h37: block still wins and nothing is premium.
	late := Duty{Report: sched.Report, Release: sched.Release, Legs: legs([2]time.Time{at(0, 12, 0), at(0, 19, 0)})}
	if st := agreement.Pay(Assignment{Scheduled: sched, Flown: late}); st.Credit != 7*time.Hour || st.Premium != 0 {
		t.Errorf("late but same block: %+v", st)
	}
	// Diverted and flown long: 9h of block, two hours past the schedule's
	// credit, paid half again: 2h x 300 x 0.5 = 300.
	long := Duty{Report: sched.Report, Release: sched.Release, Legs: legs([2]time.Time{at(0, 9, 0), at(0, 18, 0)})}
	if st := agreement.Pay(Assignment{Scheduled: sched, Flown: long}); st.Credit != 9*time.Hour || st.Premium != 30000 {
		t.Errorf("extended: %+v", st)
	}
	// Cancelled: pay protection credits the schedule.
	cancelled := Duty{Report: sched.Report, Release: sched.Release}
	if st := agreement.Pay(Assignment{Scheduled: sched, Flown: cancelled}); st.Credit != 7*time.Hour || st.Pay != 210000 {
		t.Errorf("cancelled and protected: %+v", st)
	}
	unprotected := agreement
	unprotected.PayProtected = false
	if st := unprotected.Pay(Assignment{Scheduled: sched, Flown: cancelled}); st.Total != 0 {
		t.Errorf("cancelled without protection pays nothing: %+v", st)
	}
	// A junior assignment doubles the day.
	if st := agreement.Pay(Assignment{Scheduled: sched, Flown: sched, Junior: true}); st.Premium != st.Pay {
		t.Errorf("junior: %+v", st)
	}
	// Deadheading home after: three hours of block at half credit.
	dh := legs([2]time.Time{at(0, 17, 0), at(0, 20, 0)})
	if st := agreement.Pay(Assignment{Scheduled: sched, Flown: sched, Deadhead: dh}); st.Credit != 8*time.Hour+30*time.Minute {
		t.Errorf("deadhead: %+v", st)
	}
	if agreement.ReserveDay() != 120000 {
		t.Errorf("reserve day %d", agreement.ReserveDay())
	}
}

// APFA's On-Duty Contract Guide (April 2016), section 11.D: the greater of
// flight time scheduled or actual, a 5-hour minimum per duty period, trip
// rig 1 for 3.5, duty rig 1 for 2 prorated by the minute. A one-duty
// sequence with 4 hours scheduled, 3h30 flown after a leg cancelled, 11
// hours on duty and 12 away is credited by the duty rig: 660 minutes over
// 2 is 5h30, above the 5-hour minimum, the 3h25 trip rig and the 4 hours
// scheduled.
func TestPayAsTheAPFAGuideStatesIt(t *testing.T) {
	guide := PayRules{Rate: 6000, MinDay: 5 * time.Hour, DutyRig: 0.5, TripRig: 1 / 3.5, PayProtected: true}
	sched := Duty{Report: at(0, 6, 0), Release: 15 * time.Minute, Legs: legs(
		[2]time.Time{at(0, 7, 0), at(0, 9, 0)}, [2]time.Time{at(0, 14, 45), at(0, 16, 45)})}
	flown := Duty{Report: at(0, 6, 0), Release: 15 * time.Minute, Legs: legs(
		[2]time.Time{at(0, 7, 0), at(0, 8, 0)}, [2]time.Time{at(0, 14, 15), at(0, 16, 45)})}
	st := guide.Pay(Assignment{Scheduled: sched, Flown: flown, Away: 12 * time.Hour})
	if st.Block != 4*time.Hour {
		t.Errorf("scheduled flight time is the floor: %s", st.Block)
	}
	if st.Basis != "duty rig" || st.Credit != 5*time.Hour+30*time.Minute || st.Pay != 33000 {
		t.Errorf("%+v", st)
	}
	// A short duty: 1h scheduled and flown in 3 hours on duty is credited
	// the 5-hour minimum.
	short := Duty{Report: at(0, 6, 0), Release: 15 * time.Minute, Legs: legs([2]time.Time{at(0, 7, 0), at(0, 8, 0)})}
	if st := guide.Pay(Assignment{Scheduled: short, Flown: short, Away: 2*time.Hour + 15*time.Minute}); st.Basis != "minimum day" || st.Credit != 5*time.Hour {
		t.Errorf("%+v", st)
	}
}
