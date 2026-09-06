package avs

import (
	"testing"
	"time"
)

func FuzzParse(f *testing.F) {
	f.Add("AVS\nBA0175/27SEP/LHRJFK\nY/O J/C M/L")
	f.Add("AVS\nBA0175 27SEP LHRJFK Y O\nAA0050 27SEP DFWLHR J C")
	f.Add("AVS\nBA0175/27SEP/LHRJFK\nY/O4 J/O0")
	f.Add("AVS\nBA0175/27SEP/LHRJFK\nY/AS J/LA")
	f.Add("AVS\nBA0175/27SEP/LHRJFK\nY/O\nSOMETHING ELSE ENTIRELY")
	f.Add("AVS\nBA0117/16DEC LHRJFK\nY/O2")
	f.Fuzz(func(t *testing.T, text string) {
		_ = IsAvailability(text)
		now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
		m := Parse(text, now)
		_ = m.HasErrors()
		_ = Build(m.Entries)
		_ = Default.Clone("x").Parse(text, time.Time{})
	})
}
