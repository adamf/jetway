package pnr

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// months maps the three-letter month abbreviations used throughout airline
// messaging. They are English and fixed by the standards, never localised.
var months = map[string]time.Month{
	"JAN": time.January, "FEB": time.February, "MAR": time.March,
	"APR": time.April, "MAY": time.May, "JUN": time.June,
	"JUL": time.July, "AUG": time.August, "SEP": time.September,
	"OCT": time.October, "NOV": time.November, "DEC": time.December,
}

var monthNames = [...]string{"JAN", "FEB", "MAR", "APR", "MAY", "JUN", "JUL", "AUG", "SEP", "OCT", "NOV", "DEC"}

// FormatDate renders a time as the DDMMM form the wire uses.
func FormatDate(t time.Time) string {
	return fmt.Sprintf("%02d%s", t.Day(), monthNames[int(t.Month())-1])
}

// Booking horizons. Carriers sell roughly a year ahead, and messages about a
// departure arrive for a short while after it, so the window a bare DDMMM can
// legitimately mean runs from a little in the past to just under a year ahead.
const (
	pastWindow   = 14 * 24 * time.Hour
	futureWindow = 350 * 24 * time.Hour
)

// ResolveDate turns a DDMMM date into an absolute date, relative to ref.
//
// Airline messages carry no year. A gateway must supply one, and getting it
// wrong is not a cosmetic problem: a December booking message processed on
// 1 January is a year out, which silently misfiles the segment and breaks every
// subsequent match against it. The rule here is to choose the candidate year
// that places the date inside the window a booking can plausibly occupy,
// preferring the nearest future date.
//
// ref should be the time the message was received, not the current time, so
// that replaying an old message reproduces the original interpretation.
func ResolveDate(ddmmm string, ref time.Time) (time.Time, error) {
	s := strings.ToUpper(strings.TrimSpace(ddmmm))
	if len(s) < 5 {
		return time.Time{}, fmt.Errorf("pnr: %q is too short for a DDMMM date", ddmmm)
	}
	day, err := strconv.Atoi(s[:2])
	if err != nil || day < 1 || day > 31 {
		return time.Time{}, fmt.Errorf("pnr: %q has an invalid day", ddmmm)
	}
	mon, ok := months[s[2:5]]
	if !ok {
		return time.Time{}, fmt.Errorf("pnr: %q has an unknown month", ddmmm)
	}

	// A four- or six-character suffix carrying an explicit year removes the
	// ambiguity entirely; honour it when present.
	if rest := s[5:]; rest != "" {
		if y, err := strconv.Atoi(rest); err == nil {
			switch len(rest) {
			case 2:
				y += 2000
			case 4:
			default:
				return time.Time{}, fmt.Errorf("pnr: %q has an unusable year suffix", ddmmm)
			}
			d := time.Date(y, mon, day, 0, 0, 0, 0, time.UTC)
			if d.Day() != day || d.Month() != mon {
				return time.Time{}, fmt.Errorf("pnr: %s %d is not a real date", s[:5], y)
			}
			return d, nil
		}
	}

	ref = ref.UTC()
	earliest := ref.Add(-pastWindow)
	var best time.Time
	for _, y := range []int{ref.Year() - 1, ref.Year(), ref.Year() + 1} {
		d := time.Date(y, mon, day, 0, 0, 0, 0, time.UTC)
		// 29 February in a non-leap year normalises to 1 March; reject rather
		// than silently shift the departure by a day.
		if d.Day() != day || d.Month() != mon {
			continue
		}
		if d.Before(earliest) {
			continue
		}
		if d.After(ref.Add(futureWindow)) {
			continue
		}
		if best.IsZero() || d.Before(best) {
			best = d
		}
	}
	if best.IsZero() {
		return time.Time{}, fmt.Errorf("pnr: %q does not fall in the booking window around %s",
			ddmmm, ref.Format("2006-01-02"))
	}
	return best, nil
}

// ResolveTime combines a resolved date with an HHMM time-of-day string. The
// result carries no zone: airline local times are meaningless without the
// station's zone, which is reference data this package deliberately does not
// embed. Callers holding a station database should convert.
func ResolveTime(date time.Time, hhmm string) (time.Time, error) {
	hhmm = strings.TrimSpace(hhmm)
	if len(hhmm) != 4 {
		return date, fmt.Errorf("pnr: %q is not an HHMM time", hhmm)
	}
	h, err1 := strconv.Atoi(hhmm[:2])
	m, err2 := strconv.Atoi(hhmm[2:])
	if err1 != nil || err2 != nil || h > 23 || m > 59 {
		return date, fmt.Errorf("pnr: %q is not a valid HHMM time", hhmm)
	}
	return time.Date(date.Year(), date.Month(), date.Day(), h, m, 0, 0, date.Location()), nil
}
