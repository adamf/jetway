package ssim

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// The sample records the ssimparser package ships (an R parser of the
// same layout), with its date placeholders filled: the external artefact
// the column positions are read against. The type 3 line is reproduced
// as the package builds it, padded to 200 characters.
var sampleType3 = "3XAF 43310101J01NOV2001DEC2012345672CDG18451845+0100T1ALC20252020+01001F320XX                  XX   XX        XX XX    XXX      XX XX XX XX 1234   2L W                                         00000003"
var sampleType4 = "4XAF 43310101J              XX020CDGALCAF TEST                                                                                                                                                    000004"

func TestParseFileReadsTheSampleRecords(t *testing.T) {
	src := "1AIRLINE STANDARD SCHEDULE DATA SET\n" + strings.Repeat("0", 200) + "\n" +
		"2LAF      W20 01NOV2001DEC2001NOV20SSIM EXAMPLE SCHEDULE        01NOV20CKENNY\n" +
		sampleType3 + "\n" + sampleType4 + "\n" + "5 AF 01NOV20\n"
	f, err := ParseFile(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if f.Carrier != "AF" || f.TimeMode != LocalTime || f.Season != "W20" || f.Title != "SSIM EXAMPLE SCHEDULE" || f.Status != "C" || f.Creator != "KENNY" {
		t.Errorf("carrier record: %+v", f)
	}
	if !f.From.Equal(time.Date(2020, 11, 1, 0, 0, 0, 0, time.UTC)) || !f.To.Equal(time.Date(2020, 12, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("validity %v..%v", f.From, f.To)
	}
	if len(f.Legs) != 1 || len(f.Data) != 1 || len(f.Fragments) != 0 {
		t.Fatalf("legs %d data %d fragments %v", len(f.Legs), len(f.Data), f.Fragments)
	}
	l := f.Legs[0]
	if l.Suffix != "X" || l.Carrier != "AF" || l.Number != "4331" || l.Variation != 1 || l.Sequence != 1 || l.ServiceType != "J" {
		t.Errorf("designator: %+v", l)
	}
	if l.Days != "1234567" || l.FrequencyRate != "2" {
		t.Errorf("period: days %q rate %q", l.Days, l.FrequencyRate)
	}
	if l.Board != "CDG" || l.STD != "1845" || l.PaxSTD != "" || l.DepVariation != "+0100" || l.DepTerminal != "T1" {
		t.Errorf("departure: %+v", l)
	}
	if l.Off != "ALC" || l.STA != "2025" || l.PaxSTA != "2020" || l.ArrVariation != "+0100" || l.ArrTerminal != "1F" {
		t.Errorf("arrival: %+v", l)
	}
	if l.Equipment != "320" || l.Classes != "XX" || l.Modifier != "XX" || l.Meal != "XX" || l.Joint != "XX XX" || l.Serial != 3 {
		t.Errorf("rest: %+v", l)
	}
	d := f.Data[0]
	if d.Carrier != "AF" || d.Number != "4331" || d.Suffix != "X" || d.BoardIndicator != "X" || d.OffIndicator != "X" ||
		d.DEI != "020" || d.Board != "CDG" || d.Off != "ALC" || d.Data != "AF TEST" || d.Serial != 4 {
		t.Errorf("segment data: %+v", d)
	}
}

func sampleFile() *File {
	day := time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)
	return &File{
		Carrier: "OO", TimeMode: UTC, Season: "W26", From: day, To: day, Created: day.Add(-10 * 24 * time.Hour), Released: day.Add(-10 * 24 * time.Hour),
		Title: "WHOLESKY SYNTHETIC DAY", Status: "C", Creator: "WHOLESKY",
		Legs: []FlightLeg{
			{Carrier: "OO", Number: "3000", Variation: 1, Sequence: 1, ServiceType: "J", From: day, To: day, Days: "   4   ",
				Board: "SEA", STD: "1430", DepVariation: "-0800", Off: "GEG", STA: "1535", ArrVariation: "-0800", Equipment: "E75",
				Classes: "YBMHQKLVSN", Configuration: "E75Y76", OnwardCarrier: "OO", OnwardNumber: "3001"},
			{Carrier: "OO", Number: "3001", Variation: 1, Sequence: 1, ServiceType: "J", From: day, To: day, Days: "   4   ",
				Board: "GEG", STD: "2300", DepVariation: "-0800", Off: "SEA", STA: "0010", ArrVariation: "-0800", Equipment: "E75", ArrDateVariation: 1},
		},
		Data: []SegmentData{
			{Carrier: "OO", Number: "3000", Variation: 1, Sequence: 1, ServiceType: "J", DEI: DEIMarketingFlights, Board: "SEA", Off: "GEG", Data: "AS3000"},
		},
	}
}

func TestFileRoundTrip(t *testing.T) {
	want := sampleFile()
	var buf bytes.Buffer
	if err := want.Write(&buf); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 6 {
		t.Fatalf("records: %d\n%s", len(lines), buf.String())
	}
	for i, l := range lines {
		if len(l) != 200 {
			t.Errorf("record %d is %d characters", i+1, len(l))
		}
		if got := l[194:]; got != strings.Repeat("0", 5)+string(rune('1'+i)) {
			t.Errorf("record %d serial %q", i+1, got)
		}
	}
	if lines[0][:35] != "1AIRLINE STANDARD SCHEDULE DATA SET" || lines[1][:5] != "2UOO " || lines[5][0] != '5' {
		t.Errorf("framing:\n%s", buf.String())
	}
	// The leg record by column, against the layout.
	l := lines[2]
	for _, c := range []struct {
		from, to int
		want     string
	}{
		{1, 1, "3"}, {3, 5, "OO "}, {6, 9, "3000"}, {10, 13, "0101"}, {14, 14, "J"}, {15, 21, "26NOV26"}, {22, 28, "26NOV26"},
		{29, 35, "   4   "}, {37, 39, "SEA"}, {40, 43, "1430"}, {44, 47, "1430"}, {48, 52, "-0800"}, {55, 57, "GEG"}, {58, 61, "1535"},
		{62, 65, "1535"}, {66, 70, "-0800"}, {73, 75, "E75"}, {76, 85, "YBMHQKLVSN"}, {138, 144, "OO 3001"}, {173, 178, "E75Y76"},
	} {
		if got := l[c.from-1 : c.to]; got != c.want {
			t.Errorf("columns %d-%d = %q, want %q", c.from, c.to, got, c.want)
		}
	}
	if lines[3][193] != '1' {
		t.Errorf("arrival date variation: %q", lines[3][192:194])
	}
	got, err := ParseFile(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if got.Carrier != want.Carrier || got.Season != want.Season || got.Title != want.Title || !got.From.Equal(want.From) || got.Status != "C" {
		t.Errorf("carrier record back: %+v", got)
	}
	if len(got.Legs) != 2 || len(got.Data) != 1 {
		t.Fatalf("back: %d legs %d data", len(got.Legs), len(got.Data))
	}
	for i := range want.Legs {
		w, g := want.Legs[i], got.Legs[i]
		g.Serial = 0
		if w != g {
			t.Errorf("leg %d\n want %+v\n got  %+v", i, w, g)
		}
	}
	d := got.Data[0]
	d.Serial = 0
	w := want.Data[0]
	w.BoardIndicator, w.OffIndicator = "X", "X"
	if d != w {
		t.Errorf("data\n want %+v\n got  %+v", w, d)
	}
	if op := got.OperatingFlight(got.Legs[0]); op != (Flight{}) {
		t.Errorf("an operated leg has no operating flight of its own: %+v", op)
	}
}

func TestOperatingFlightFromDEI010(t *testing.T) {
	f := &File{Legs: []FlightLeg{{Carrier: "AS", Number: "3000", Variation: 1, Sequence: 1, Disclosure: "L"}},
		Data: []SegmentData{{Carrier: "AS", Number: "3000", Variation: 1, Sequence: 1, DEI: DEIOperatingFlight, Data: "OO 3000"}}}
	if op := f.OperatingFlight(f.Legs[0]); op.Carrier != "OO" || op.Number != "3000" {
		t.Errorf("operating flight: %+v", op)
	}
	for in, want := range map[string]Flight{"OO3000": {Carrier: "OO", Number: "3000"}, "OO 3000A": {Carrier: "OO", Number: "3000", Suffix: "A"}, "UA 0012": {Carrier: "UA", Number: "12"}} {
		if got := parseDesignator(in); got != want {
			t.Errorf("%q -> %+v, want %+v", in, got, want)
		}
	}
}

func TestParseFileKeepsUnknownRecordsAndTrimmedLines(t *testing.T) {
	src := "1AIRLINE STANDARD SCHEDULE DATA SET\n2UOO\n3 OO 30000101J26NOV2626NOV26   4   \n7SOMETHING NEW\n5 OO\n"
	f, err := ParseFile(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Legs) != 1 || f.Legs[0].Number != "3000" || f.Legs[0].Days != "   4   " {
		t.Errorf("trimmed leg: %+v", f.Legs)
	}
	if len(f.Fragments) != 1 || f.Fragments[0] != "7SOMETHING NEW" {
		t.Errorf("fragments: %v", f.Fragments)
	}
	if _, err := ParseFile(strings.NewReader("")); err == nil {
		t.Error("an empty file is not a schedule")
	}
}

func FuzzFileRoundTrip(f *testing.F) {
	f.Add("OO", "3000", "SEA", "GEG", "1430", "YBM")
	f.Fuzz(func(t *testing.T, carrier, number, board, off, std, classes string) {
		in := sampleFile()
		in.Legs[0].Carrier, in.Legs[0].Number, in.Legs[0].Board, in.Legs[0].Off, in.Legs[0].STD, in.Legs[0].Classes = carrier, number, board, off, std, classes
		var buf bytes.Buffer
		if err := in.Write(&buf); err != nil {
			t.Fatal(err)
		}
		for i, l := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
			if len(l) != 200 {
				t.Fatalf("record %d is %d characters: %q", i, len(l), l)
			}
		}
		out, err := ParseFile(&buf)
		if err != nil {
			t.Fatal(err)
		}
		if len(out.Legs) != 2 {
			t.Fatalf("legs %d", len(out.Legs))
		}
		// Only what a record can carry round-trips: printable ASCII that
		// fits the column and has no blanks at its ends.
		clean := func(s string, n int) bool {
			if len(s) > n || s == "" || s != strings.TrimSpace(s) {
				return false
			}
			for i := 0; i < len(s); i++ {
				if s[i] < 0x20 || s[i] > 0x7e {
					return false
				}
			}
			return true
		}
		if clean(board, 3) && out.Legs[0].Board != board {
			t.Errorf("board %q became %q", board, out.Legs[0].Board)
		}
		if clean(std, 4) && out.Legs[0].STD != std {
			t.Errorf("std %q became %q", std, out.Legs[0].STD)
		}
	})
}
