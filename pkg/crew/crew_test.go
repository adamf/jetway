package crew

import (
	"testing"
	"time"
)

// Table B as published: a 0700 report with two segments may run 14 hours,
// with five segments 12.5; a 1700 report with six segments 9 hours. Table
// A: a 0430 report may fly 8 block hours, a 0900 report 9.
func TestPart117TablesAsPublished(t *testing.T) {
	at := func(hh, mm int) time.Time { return time.Date(2026, 11, 26, hh, mm, 0, 0, time.UTC) }
	cases := []struct {
		report   time.Time
		segments int
		want     float64
	}{
		{at(7, 0), 2, 14}, {at(7, 0), 5, 12.5}, {at(11, 59), 7, 11.5}, {at(12, 0), 1, 13},
		{at(17, 0), 6, 9}, {at(17, 0), 3, 11}, {at(4, 30), 4, 10}, {at(4, 30), 5, 9},
		{at(23, 15), 4, 9}, {at(0, 5), 1, 9}, {at(5, 0), 1, 12}, {at(6, 0), 3, 12}, {at(13, 0), 2, 12}, {at(22, 0), 3, 10},
	}
	for _, c := range cases {
		if got := Part117.FDPLimit(c.report, c.segments); got != time.Duration(c.want*float64(time.Hour)) {
			t.Errorf("FDP at %s with %d segments: %s, want %vh", c.report.Format("1504"), c.segments, got, c.want)
		}
	}
	if Part117.FlightTimeLimit(at(4, 30)) != 8*time.Hour || Part117.FlightTimeLimit(at(9, 0)) != 9*time.Hour || Part117.FlightTimeLimit(at(20, 0)) != 8*time.Hour {
		t.Error("Table A flight time limits")
	}
}

// A four-leg day reporting at 0600 (limit 12 hours): planned to release at
// 1715 it is legal; a delay pushing the last arrival to 1830 is legal only
// on the two-hour extension; to 2030 it is not legal at all, and the latest
// legal departure of the last leg is the point the crew times out.
func TestDutyLegalityUnderDelay(t *testing.T) {
	at := func(hh, mm int) time.Time { return time.Date(2026, 11, 26, hh, mm, 0, 0, time.UTC) }
	d := Duty{Rules: Part117, Report: at(6, 0), Release: 15 * time.Minute, Legs: []Leg{
		{Flight: "WN10", Depart: at(7, 0), Arrive: at(9, 0)},
		{Flight: "WN20", Depart: at(9, 45), Arrive: at(11, 30)},
		{Flight: "WN30", Depart: at(12, 15), Arrive: at(14, 30)},
		{Flight: "WN40", Depart: at(15, 15), Arrive: at(17, 0)},
	}}
	if v := d.Check(); !v.Legal || v.Extended || v.FDP != 11*time.Hour+15*time.Minute || v.FDPLimit != 12*time.Hour {
		t.Errorf("planned day: %+v", v)
	}
	d.Legs[3].Depart, d.Legs[3].Arrive = at(16, 45), at(18, 30)
	if v := d.Check(); !v.Legal || !v.Extended {
		t.Errorf("an hour and a half late needs the extension: %+v", v)
	}
	d.Legs[3].Depart, d.Legs[3].Arrive = at(18, 45), at(20, 30)
	if v := d.Check(); v.Legal || v.Reason == "" {
		t.Errorf("three and a half hours late times the crew out: %+v", v)
	}
	// Latest departure: 0600 + 12h + 2h - 15m release - 1h45 block = 1800.
	if latest, ok := d.LatestDeparture(); !ok || !latest.Equal(at(18, 0)) {
		t.Errorf("latest legal departure %s", latest.Format("1504"))
	}
	// Block time: nine hours of flying on a 0900 report is the line.
	long := Duty{Rules: Part117, Report: at(9, 0), Release: 15 * time.Minute, Legs: []Leg{
		{Depart: at(10, 0), Arrive: at(15, 0)}, {Depart: at(16, 0), Arrive: at(20, 30)},
	}}
	if v := long.Check(); v.Legal {
		t.Errorf("nine and a half block hours on a 0900 report: %+v", v)
	}
}
