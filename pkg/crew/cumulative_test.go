package crew

import (
	"strings"
	"testing"
	"time"
)

var day0 = time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)

func at(day, hh, mm int) time.Time {
	return day0.AddDate(0, 0, day).Add(time.Duration(hh)*time.Hour + time.Duration(mm)*time.Minute)
}

// pastDuty is a one-leg flight duty on an earlier day: report an hour
// before the leg, release fifteen minutes after.
func pastDuty(day, depHH, blockMin int) Period {
	dep := at(day, depHH, 0)
	arr := dep.Add(time.Duration(blockMin) * time.Minute)
	return Period{Kind: FlightDuty, Report: dep.Add(-time.Hour), Release: arr.Add(15 * time.Minute), Legs: []Leg{{Depart: dep, Arrive: arr}}}
}

func today(blockMin int) Duty {
	dep := at(0, 9, 0)
	return Duty{Rules: Part117, Report: dep.Add(-time.Hour), Release: 15 * time.Minute,
		Legs: []Leg{{Flight: "XX1", Depart: dep, Arrive: dep.Add(time.Duration(blockMin) * time.Minute)}}}
}

// §117.23(b)(1): 100 hours of flight time in any 672 consecutive hours.
// Nineteen past days of five hours each are 95 hours; a five-hour leg
// today makes 100, which is the limit and legal; five and a half is not.
func TestFlightTimeIn672Hours(t *testing.T) {
	var r Record
	for d := -20; d <= -2; d++ {
		r.Add(pastDuty(d, 8, 300))
	}
	if v := today(300).Admits(&r); !v.Legal {
		t.Fatalf("exactly 100 hours is within the limit: %+v", v)
	}
	v := today(330).Admits(&r)
	if v.Legal || !strings.Contains(v.Reason, "117.23(b)(1)") {
		t.Fatalf("100h30 in 672 hours: %+v", v)
	}
	// The worst point is the leg's arrival: the total there is 100h30.
	for _, tot := range v.Totals {
		if tot.Name == "flight time in 672 hours" && tot.Used != 100*time.Hour+30*time.Minute {
			t.Errorf("used %s", tot.Used)
		}
	}
	// A day that has left the window no longer counts: move the oldest
	// duty 29 days back and the same leg is legal again.
	r.Periods[0] = pastDuty(-29, 8, 300)
	if v := today(330).Admits(&r); !v.Legal {
		t.Errorf("a duty 29 days ago is outside 672 hours: %+v", v)
	}
}

// A leg the window's start cuts counts only for its part inside it.
func TestWindowCutsALeg(t *testing.T) {
	var r Record
	// 96 hours of flying spread over the past 27 days.
	for d := -27; d <= -4; d++ {
		r.Add(pastDuty(d, 8, 240))
	}
	duty := today(240)
	// The 672 hours ending at today's arrival (13:00) start at 13:00 on
	// day -28; a leg on day -28 from 11:00 to 15:00 has two hours inside.
	r.Add(pastDuty(-28, 11, 240))
	v := duty.Admits(&r)
	if v.Legal {
		t.Fatalf("96 + 4 + 2 inside the window is 102 hours: %+v", v)
	}
	if got := v.Totals[0].Used; got != 102*time.Hour {
		t.Errorf("used %s, want 102h", got)
	}
}

// §117.23(b)(2): 1,000 hours in any 365 consecutive calendar days.
func TestFlightTimeIn365Days(t *testing.T) {
	var r Record
	// 200 duties of five hours spread over the year, none within the
	// last 28 days: 1,000 hours.
	for i := 0; i < 200; i++ {
		r.Add(pastDuty(-360+i*(330/200), 8, 300))
	}
	v := today(60).Admits(&r)
	if v.Legal || !strings.Contains(v.Reason, "117.23(b)(2)") {
		t.Fatalf("an hour more than 1,000 in 365 days: %+v", v)
	}
	// The calendar window is days -364 to 0: move one five-hour duty to
	// day -365 and an hour's leg is legal.
	r.Periods[0] = pastDuty(-365, 8, 300)
	if v := today(60).Admits(&r); !v.Legal {
		t.Errorf("a duty on day -365 is outside 365 calendar days: %+v", v)
	}
}

// §117.23(c)(1): 60 flight duty period hours in any 168 consecutive hours.
// Five eleven-hour duties in the last six days are 55 hours.
func TestFDPIn168HoursLimit(t *testing.T) {
	var r Record
	for d := -6; d <= -2; d++ {
		dep := at(d, 7, 0)
		r.Add(Period{Kind: FlightDuty, Report: dep.Add(-time.Hour), Release: dep.Add(10 * time.Hour), Legs: []Leg{{Depart: dep, Arrive: dep.Add(2 * time.Hour)}}})
	}
	// Today's duty: report 08:00, block 3h45, release 13:00: five hours,
	// which makes 60 hours of FDP, the limit. Ninety minutes more of
	// block makes 61h30.
	if v := today(225).Admits(&r); !v.Legal {
		t.Errorf("59 hours of FDP in 168: %+v", v)
	}
	v := today(315).Admits(&r)
	if v.Legal || !strings.Contains(v.Reason, "117.23(c)(1)") {
		t.Errorf("61h30 of FDP in 168 hours: %+v", v)
	}
	// A reserve period is duty for rest, not FDP: seven hours of it inside
	// the window do not count.
	r.Add(Period{Kind: Reserve, Report: at(-7, 9, 0), Release: at(-7, 20, 0)})
	if v := today(225).Admits(&r); !v.Legal {
		t.Errorf("reserve time counted as FDP: %+v", v)
	}
}

// §117.23(c)(2): 190 FDP hours in any 672 consecutive hours.
func TestFDPIn672Hours(t *testing.T) {
	var r Record
	// Nineteen ten-hour duties on days -27 to -9 are 190 hours; none is in
	// the 168 hours before today, so only the 672-hour limit is reached.
	for d := -27; d <= -9; d++ {
		dep := at(d, 7, 0)
		r.Add(Period{Kind: FlightDuty, Report: dep.Add(-time.Hour), Release: dep.Add(9 * time.Hour), Legs: []Leg{{Depart: dep, Arrive: dep.Add(time.Hour)}}})
	}
	v := today(60).Admits(&r)
	if v.Legal || !strings.Contains(v.Reason, "117.23(c)(2)") {
		t.Errorf("190 + 2h15 FDP hours in 672: %+v", v)
	}
}

// §117.25(e): ten consecutive hours of rest before the duty begins.
func TestRestBeforeDuty(t *testing.T) {
	var r Record
	r.Add(Period{Kind: FlightDuty, Report: at(-1, 13, 0), Release: at(-1, 23, 0)})
	// Report 08:00 after a 23:00 release: nine hours.
	v := today(120).Admits(&r)
	if v.Legal || !strings.Contains(v.Reason, "117.25(e)") {
		t.Fatalf("nine hours of rest: %+v", v)
	}
	r.Periods[0].Release = at(-1, 22, 0)
	if v := today(120).Admits(&r); !v.Legal {
		t.Errorf("ten hours of rest is enough: %+v", v)
	}
}

// §117.25(b): thirty consecutive hours free of all duty in the 168 hours
// before the duty. A member on reserve or duty every day for a week, with
// twenty-hour gaps, has not had them.
func TestThirtyHoursFreeInAWeek(t *testing.T) {
	var r Record
	for d := -7; d <= -1; d++ {
		r.Add(Period{Kind: Reserve, Report: at(d, 6, 0), Release: at(d, 10, 0)})
	}
	v := today(120).Admits(&r)
	if v.Legal || !strings.Contains(v.Reason, "117.25(b)") {
		t.Fatalf("no thirty hours free in a week: %+v", v)
	}
	// Free the middle of the week: day -4's period goes, leaving 10:00 on
	// day -5 to 06:00 on day -3 free, which is 44 hours.
	r.Periods = append(r.Periods[:3], r.Periods[4:]...)
	if v := today(120).Admits(&r); !v.Legal {
		t.Errorf("44 hours free: %+v", v)
	}
}

func TestRemainingHeadroom(t *testing.T) {
	var r Record
	for d := -10; d <= -1; d++ {
		r.Add(pastDuty(d, 8, 420))
	}
	got := Part117.Remaining(&r, at(0, 6, 0))
	if got[0].Used != 70*time.Hour || got[0].Remaining() != 30*time.Hour {
		t.Errorf("flight time in 672 hours: %+v", got[0])
	}
	// FDP in 168 hours: the window starts at 06:00 on day -7, so the
	// duties of days -7 to -1 are inside, 8h15 each.
	if got[2].Used != 7*(8*time.Hour+15*time.Minute) {
		t.Errorf("FDP in 168 hours: %+v", got[2])
	}
}

func TestLongestFree(t *testing.T) {
	p := []Period{
		{Report: at(0, 8, 0), Release: at(0, 12, 0)},
		{Report: at(0, 10, 0), Release: at(0, 14, 0)}, // overlaps the first
		{Report: at(1, 2, 0), Release: at(1, 3, 0)},
	}
	if got := LongestFree(p, at(0, 0, 0), at(1, 6, 0)); got != 12*time.Hour {
		t.Errorf("longest free %s, want 12h (14:00 to 02:00)", got)
	}
	if got := LongestFree(nil, at(0, 0, 0), at(1, 0, 0)); got != 24*time.Hour {
		t.Errorf("an empty record is all free: %s", got)
	}
}

// 121.467(b): 14 hours scheduled with the minimum complement, 16 with one
// more flight attendant; 12 hours of rest after a duty over 14 hours.
func TestCabinRules(t *testing.T) {
	long := Duty{Report: at(0, 6, 0), Release: 15 * time.Minute, Legs: []Leg{{Depart: at(0, 7, 0), Arrive: at(0, 20, 0)}}}
	// 06:00 to 20:15 is 14h15.
	if v := Part121Cabin.Admits(nil, long, 0, false); v.Legal {
		t.Errorf("14h15 with the minimum complement: %+v", v)
	}
	if v := Part121Cabin.Admits(nil, long, 1, false); !v.Legal {
		t.Errorf("14h15 with one extra: %+v", v)
	}
	var r Record
	r.Add(Period{Kind: FlightDuty, Report: at(-1, 4, 0), Release: at(-1, 19, 0)}) // 15 hours
	next := Duty{Report: at(0, 6, 0), Release: 15 * time.Minute, Legs: []Leg{{Depart: at(0, 7, 0), Arrive: at(0, 9, 0)}}}
	// 19:00 to 06:00 is eleven hours: enough after a short duty, not a long one.
	if v := Part121Cabin.Admits(&r, next, 0, false); v.Legal || !strings.Contains(v.Reason, "121.467") {
		t.Errorf("11 hours after a 15-hour duty: %+v", v)
	}
	r.Periods[0].Report = at(-1, 6, 0) // 13 hours
	if v := Part121Cabin.Admits(&r, next, 0, false); !v.Legal {
		t.Errorf("11 hours after a 13-hour duty: %+v", v)
	}
	if Part121Cabin.MaxDuty(3, false) != 18*time.Hour || Part121Cabin.MaxDuty(3, true) != 20*time.Hour {
		t.Error("the 20-hour duty needs a point outside the 48 states")
	}
}

// (b)(13): a whole free day in any 7 consecutive calendar days.
func TestCabinFreeDay(t *testing.T) {
	var r Record
	for d := -6; d <= -1; d++ {
		r.Add(Period{Kind: FlightDuty, Report: at(d, 6, 0), Release: at(d, 16, 0)})
	}
	next := Duty{Report: at(0, 6, 0), Release: 15 * time.Minute, Legs: []Leg{{Depart: at(0, 7, 0), Arrive: at(0, 9, 0)}}}
	if v := Part121Cabin.Admits(&r, next, 0, false); v.Legal || !strings.Contains(v.Reason, "(b)(13)") {
		t.Errorf("seventh straight day: %+v", v)
	}
}

// 121.391(a), seat by seat at each boundary.
func TestMinFlightAttendants(t *testing.T) {
	for seats, want := range map[int]int{9: 0, 10: 1, 50: 1, 51: 2, 100: 2, 101: 3, 150: 3, 151: 4, 200: 4, 201: 5, 396: 8} {
		if got := MinFlightAttendants(seats); got != want {
			t.Errorf("%d seats: %d flight attendants, want %d", seats, got, want)
		}
	}
}

// The worst point can be a middle leg's arrival: a seven-hour leg on day
// -28 is inside the 672 hours that end at the first arrival (12:00) and
// outside those that end at the second (19:00). At 12:00 the total is
// 91 + 7 + 3 = 101 hours; at 19:00 it is 91 + 4 = 95.
func TestWorstPointMidDuty(t *testing.T) {
	var r Record
	for d := -26; d <= -14; d++ {
		r.Add(pastDuty(d, 8, 420))
	}
	r.Add(Period{Kind: FlightDuty, Report: at(-28, 11, 0), Release: at(-28, 19, 15), Legs: []Leg{{Depart: at(-28, 12, 0), Arrive: at(-28, 19, 0)}}})
	d := Duty{Rules: Part117, Report: at(0, 8, 0), Release: 15 * time.Minute, Legs: legs(
		[2]time.Time{at(0, 9, 0), at(0, 12, 0)}, [2]time.Time{at(0, 18, 0), at(0, 19, 0)})}
	v := d.Admits(&r)
	if v.Legal || v.Totals[0].Used != 101*time.Hour {
		t.Errorf("101 hours at the first arrival: %+v", v)
	}
}
