package mct

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// at builds a 200-byte record by hand: each value at the byte the manual's
// layout table gives it, counted from one. The tests' fixtures are written
// this way from section 8.9.3 and the sample file of section 8.14, not
// with the package's encoder.
func at(fields map[int]string) string {
	b := []byte(strings.Repeat(" ", 200))
	for pos, v := range fields {
		copy(b[pos-1:], v)
	}
	return string(b)
}

// The sample transmission of section 8.14, as far as its fields can be
// read from the published page: a header from "ABC AGGREGATOR" made
// 05SEP18 at 14:53, a suppression at Heathrow, exceptions at Heathrow,
// Paris and Frankfurt, and the trailer checking serial 000008.
func sampleFile() string {
	lines := []string{
		at(map[int]string{1: "1", 2: "MINIMUM CONNECT TIME DATA SET", 32: "ABC AGGREGATOR", 67: "05SEP18", 74: "1453", 78: "F", 195: "000001"}),
		// 2LHR    IILHRAAYBAAAYBA ... 5 3 IN CA Y AA05SEP18: no AA*BA to AA*BA
		// connection from terminal 5 to terminal 3 between India and Canada.
		at(map[int]string{1: "2", 2: "LHR", 9: "II", 11: "LHR", 14: "AA", 16: "Y", 17: "BA", 19: "AA", 21: "Y", 22: "BA", 32: "5", 34: "3", 36: "IN", 41: "CA", 87: "Y", 95: "AA", 97: "05SEP18", 195: "000002"}),
		// 2LHR0100IILHRUAYLHUAYAC ... 2 2 ... CA N UA05SEP18
		at(map[int]string{1: "2", 2: "LHR", 5: "0100", 9: "II", 11: "LHR", 14: "UA", 16: "Y", 17: "LH", 19: "UA", 21: "Y", 22: "AC", 32: "2", 34: "2", 41: "CA", 87: "N", 95: "UA", 97: "05SEP18", 195: "000003"}),
		// 2CDG0130IICDGDLYAFDLY9W        2C2E ... 01JUN18 ... N DL05SEP18
		at(map[int]string{1: "2", 2: "CDG", 5: "0130", 9: "II", 11: "CDG", 14: "DL", 16: "Y", 17: "AF", 19: "DL", 21: "Y", 22: "9W", 32: "2C", 34: "2E", 72: "01JUN18", 87: "N", 95: "DL", 97: "05SEP18", 195: "000004"}),
		// 2FRA0200IIFRAUAYLHUAYAC ... LUX ... CA N UA05SEP18: from Luxembourg, on to Canada.
		at(map[int]string{1: "2", 2: "FRA", 5: "0200", 9: "II", 11: "FRA", 14: "UA", 16: "Y", 17: "LH", 19: "UA", 21: "Y", 22: "AC", 38: "LUX", 41: "CA", 87: "N", 95: "UA", 97: "05SEP18", 195: "000005"}),
		// 2FRA0045IIFRAUAYLHUAYLH ... US ... SCH N UA05SEP18: from the United States, on into Schengen.
		at(map[int]string{1: "2", 2: "FRA", 5: "0045", 9: "II", 11: "FRA", 14: "UA", 16: "Y", 17: "LH", 19: "UA", 21: "Y", 22: "LH", 36: "US", 69: "SCH", 87: "N", 95: "UA", 97: "05SEP18", 195: "000006"}),
		// 2FRA0100IIFRAUAYLHUAYLH ... US ... N UA05SEP18: from the United States, anywhere.
		at(map[int]string{1: "2", 2: "FRA", 5: "0100", 9: "II", 11: "FRA", 14: "UA", 16: "Y", 17: "LH", 19: "UA", 21: "Y", 22: "LH", 36: "US", 87: "N", 95: "UA", 97: "05SEP18", 195: "000007"}),
		// A station standard, which the sample does not show: Frankfurt, 45 minutes.
		at(map[int]string{1: "2", 2: "FRA", 5: "0045", 9: "II", 11: "FRA", 195: "000008"}),
		at(map[int]string{1: "3", 194: "E", 195: "000008"}),
	}
	return strings.Join(lines, "\r\n") + "\r\n"
}

func TestParseTheManualsSampleRecords(t *testing.T) {
	// The Paris record begins exactly as the manual prints it.
	lines := strings.Split(sampleFile(), "\r\n")
	if want := "2CDG0130IICDGDLYAFDLY9W        2C2E"; !strings.HasPrefix(lines[3], want) {
		t.Fatalf("the fixture is not the manual's record: %q", lines[3][:40])
	}
	f, err := Parse(strings.NewReader(sampleFile()))
	if err != nil {
		t.Fatal(err)
	}
	if f.Creator != "ABC AGGREGATOR" || !f.Full || !f.Created.Equal(time.Date(2018, 9, 5, 14, 53, 0, 0, time.UTC)) || len(f.Records) != 7 {
		t.Fatalf("header %q full %v created %v, %d records", f.Creator, f.Full, f.Created, len(f.Records))
	}
	sup := f.Records[0]
	if !sup.Suppress || sup.Minutes != 0 || sup.ArrCarrier != "AA" || !sup.ArrCodeshare || sup.ArrOperating != "BA" || sup.DepOperating != "BA" ||
		sup.ArrTerminal != "5" || sup.DepTerminal != "3" || sup.PrevCountry != "IN" || sup.NextCountry != "CA" || sup.Submitter != "AA" {
		t.Errorf("the Heathrow suppression: %+v", sup)
	}
	cdg := f.Records[2]
	if cdg.Arrival != "CDG" || cdg.Minutes != 90 || cdg.Status != II || cdg.ArrCarrier != "DL" || cdg.ArrOperating != "AF" || cdg.DepCarrier != "DL" || cdg.DepOperating != "9W" ||
		cdg.ArrTerminal != "2C" || cdg.DepTerminal != "2E" || !cdg.From.Equal(time.Date(2018, 6, 1, 0, 0, 0, 0, time.UTC)) || cdg.Suppress || !cdg.Filed.Equal(time.Date(2018, 9, 5, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("the Paris exception: %+v", cdg)
	}
	if lux := f.Records[3]; lux.PrevStation != "LUX" || lux.NextCountry != "CA" || lux.Minutes != 120 {
		t.Errorf("the Luxembourg exception: %+v", lux)
	}
	if sch := f.Records[4]; sch.PrevCountry != "US" || sch.NextRegion != "SCH" || sch.Minutes != 45 {
		t.Errorf("the Schengen exception: %+v", sch)
	}
	if std := f.Records[6]; !std.StationStandard() || std.Minutes != 45 {
		t.Errorf("the station standard: %+v", std)
	}

	// Written and read again, the records are the same, and the Paris
	// record's bytes are the manual's.
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatal(err)
	}
	out := strings.Split(buf.String(), "\n")
	if want := lines[3][:194]; out[3][:194] != want {
		t.Errorf("the Paris record written\n got %q\nwant %q", out[3][:194], want)
	}
	for i, l := range out[:len(out)-1] {
		if len(l) != 200 {
			t.Errorf("line %d is %d bytes", i+1, len(l))
		}
	}
	if last := out[len(out)-2]; last[0] != '4' || last[193] != 'E' || last[194:] != "000008" {
		t.Errorf("trailer %q", last[190:])
	}
	g, err := Parse(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Records) != len(f.Records) {
		t.Fatalf("%d records back, %d out", len(g.Records), len(f.Records))
	}
	for i := range f.Records {
		if g.Records[i] != f.Records[i] {
			t.Errorf("record %d changed in the round trip:\n%+v\n%+v", i, f.Records[i], g.Records[i])
		}
	}
}

func clock(h, m int) time.Time { return time.Date(2021, 3, 1, h, m, 0, 0, time.UTC) }

// Section 8.8, the arrival codeshare indicator's example. "Passenger wants
// to fly LHR-MIA-DFW": BA0207 arrives Miami at 1500 and is also sold as
// AA6160; AA2718 leaves at 1635 and AA2720 at 1700. MCT 1 is 0140 for an
// American flight operated by British Airways connecting to American;
// MCT 2 is 0130 for British Airways to American. "BA207 connects to
// AA2718 and AA2720 with MCT 2. AA6160 connects to AA2720 with MCT 1.
// AA6160 will not connect to AA2718."
func TestTheManualsMiamiExample(t *testing.T) {
	tab := NewTable([]Record{
		{Arrival: "MIA", Departure: "MIA", Status: ID, Minutes: 100, ArrCarrier: "AA", ArrCodeshare: true, ArrOperating: "BA", DepCarrier: "AA"},
		{Arrival: "MIA", Departure: "MIA", Status: ID, Minutes: 90, ArrCarrier: "BA", DepCarrier: "AA"},
	})
	ba207 := Flight{Carrier: "BA", Number: 207}
	aa6160 := Flight{Carrier: "AA", Number: 6160, Operating: "BA", OperatingNumber: 207}
	conn := func(in Flight, out int) Connection {
		return Connection{Arrival: "MIA", Departure: "MIA", Status: ID, In: in, Out: Flight{Carrier: "AA", Number: out}}
	}
	cases := []struct {
		in      Flight
		out     int
		depart  time.Time
		minutes int
		builds  bool
	}{
		{ba207, 2718, clock(16, 35), 90, true},
		{ba207, 2720, clock(17, 0), 90, true},
		{aa6160, 2720, clock(17, 0), 100, true},
		{aa6160, 2718, clock(16, 35), 100, false},
	}
	for _, c := range cases {
		ok, a := tab.Check(conn(c.in, c.out), clock(15, 0), c.depart)
		if ok != c.builds || a.Minutes != c.minutes {
			t.Errorf("%s%d to AA%d: builds %v with %d minutes, want %v with %d", c.in.Carrier, c.in.Number, c.out, ok, a.Minutes, c.builds, c.minutes)
		}
	}
}

// The departure codeshare indicator's example. VS1345 arrives New York at
// 1730. DL0810 leaves at 1820 and is also sold as VS2810; DL0910 leaves
// at 1855 and is also sold as VS1700. MCT 1 is 0100 for Virgin to a
// Virgin flight operated by Delta, MCT 2 is 0045 Delta to Delta, MCT 3
// is 0045 Virgin to Delta. "VS1345 connects to DL0810 with MCT 3. VS1345
// connect to VS1700 with MCT 1. VS1345 and VS2810 will not connect."
func TestTheManualsNewYorkExample(t *testing.T) {
	mct1 := Record{Arrival: "JFK", Departure: "JFK", Status: ID, Minutes: 60, ArrCarrier: "VS", DepCarrier: "VS", DepCodeshare: true, DepOperating: "DL"}
	mct3 := Record{Arrival: "JFK", Departure: "JFK", Status: ID, Minutes: 45, ArrCarrier: "VS", DepCarrier: "DL"}
	tab := NewTable([]Record{mct1, {Arrival: "JFK", Departure: "JFK", Status: ID, Minutes: 45, ArrCarrier: "DL", DepCarrier: "DL"}, mct3})
	in := Flight{Carrier: "VS", Number: 1345}
	conn := func(out Flight) Connection {
		return Connection{Arrival: "JFK", Departure: "JFK", Status: ID, In: in, Out: out}
	}
	ok, a := tab.Check(conn(Flight{Carrier: "DL", Number: 810}), clock(17, 30), clock(18, 20))
	if !ok || *a.Record != mct3 {
		t.Errorf("VS1345 to DL0810: %v with %v", ok, a.Record)
	}
	ok, a = tab.Check(conn(Flight{Carrier: "VS", Number: 1700, Operating: "DL", OperatingNumber: 910}), clock(17, 30), clock(18, 55))
	if !ok || *a.Record != mct1 {
		t.Errorf("VS1345 to VS1700: %v with %v", ok, a.Record)
	}
	ok, a = tab.Check(conn(Flight{Carrier: "VS", Number: 2810, Operating: "DL", OperatingNumber: 810}), clock(17, 30), clock(18, 20))
	if ok || *a.Record != mct1 {
		t.Errorf("VS1345 and VS2810 will not connect: %v with %v", ok, a.Record)
	}
}

// Section 8.10: the global defaults, a station standard over them, an
// exception over the standard, and the order between exceptions.
func TestHierarchy(t *testing.T) {
	f, err := Parse(strings.NewReader(sampleFile()))
	if err != nil {
		t.Fatal(err)
	}
	tab := f.Table()
	// No record for Vienna: the global defaults, and four hours between
	// two airports.
	for status, want := range map[string]int{DD: 30, DI: 60, ID: 90, II: 90} {
		if a := tab.Lookup(Connection{Arrival: "VIE", Departure: "VIE", Status: status}); a.Minutes != want || a.Record != nil {
			t.Errorf("global default %s: %d", status, a.Minutes)
		}
	}
	if a := tab.Lookup(Connection{Arrival: "LHR", Departure: "LGW", Status: II}); a.Minutes != 240 {
		t.Errorf("between two airports: %d", a.Minutes)
	}
	// Frankfurt's standard for anyone.
	if a := tab.Lookup(Connection{Arrival: "FRA", Departure: "FRA", Status: II, In: Flight{Carrier: "BA", Number: 900}, Out: Flight{Carrier: "BA", Number: 901}}); a.Minutes != 45 || !a.Record.StationStandard() {
		t.Errorf("station standard: %+v", a)
	}
	// United sold, Lufthansa flown, both ways, arriving from the United
	// States: an hour, but 45 minutes when the passenger goes on into
	// Schengen, because the record that names the next region is the more
	// specific.
	ua := func(n int) Flight {
		return Flight{Carrier: "UA", Number: n, Operating: "LH", OperatingNumber: n - 8000}
	}
	in := ua(8840)
	in.Country, in.Region = "US", "NOA"
	out := ua(8850)
	out.Country, out.Region = "GB", "EUR"
	if a := tab.Lookup(Connection{Arrival: "FRA", Departure: "FRA", Status: II, In: in, Out: out}); a.Minutes != 60 {
		t.Errorf("from the United States to Britain: %+v", a)
	}
	out.Country, out.Region = "IT", "SCH"
	if a := tab.Lookup(Connection{Arrival: "FRA", Departure: "FRA", Status: II, In: in, Out: out}); a.Minutes != 45 || a.Record.NextRegion != "SCH" {
		t.Errorf("from the United States into Schengen: %+v", a)
	}
	// The Heathrow suppression: terminal 5 to terminal 3, India to Canada.
	aa := func(n int) Flight {
		return Flight{Carrier: "AA", Number: n, Operating: "BA", OperatingNumber: n - 6000}
	}
	in, out = aa(6100), aa(6200)
	in.Terminal, in.Country, out.Terminal, out.Country = "5", "IN", "3", "CA"
	if a := tab.Lookup(Connection{Arrival: "LHR", Departure: "LHR", Status: II, In: in, Out: out}); !a.Suppressed {
		t.Errorf("the suppressed connection was built: %+v", a)
	}
	out.Country = "US"
	if a := tab.Lookup(Connection{Arrival: "LHR", Departure: "LHR", Status: II, In: in, Out: out}); a.Suppressed {
		t.Errorf("a connection the suppression does not name: %+v", a)
	}
	// The Paris record is effective from 1 June 2018.
	dl := func(op string) Flight {
		return Flight{Carrier: "DL", Number: 8000, Operating: op, OperatingNumber: 100}
	}
	in, out = dl("AF"), dl("9W")
	in.Terminal, out.Terminal = "2C", "2E"
	c := Connection{Arrival: "CDG", Departure: "CDG", Status: II, In: in, Out: out, Date: time.Date(2018, 5, 31, 0, 0, 0, 0, time.UTC)}
	if a := tab.Lookup(c); a.Record != nil {
		t.Errorf("before its effective date the record applied: %+v", a)
	}
	c.Date = time.Date(2018, 6, 1, 0, 0, 0, 0, time.UTC)
	if a := tab.Lookup(c); a.Minutes != 90 {
		t.Errorf("on its effective date: %+v", a)
	}
}

// The tie rules and the rest of the order: the departure side before the
// arrival side, a narrower flight range, a dated record, and an exception
// at a station over a suppression filed for the world.
func TestMoreSpecificRecordsWin(t *testing.T) {
	std := Record{Arrival: "AMS", Departure: "AMS", Status: II, Minutes: 50}
	arr := Record{Arrival: "AMS", Departure: "AMS", Status: II, Minutes: 70, ArrCarrier: "KL"}
	dep := Record{Arrival: "AMS", Departure: "AMS", Status: II, Minutes: 40, DepCarrier: "KL"}
	wide := Record{Arrival: "AMS", Departure: "AMS", Status: II, Minutes: 60, DepCarrier: "KL", DepFlightFrom: 1000, DepFlightTo: 1999}
	narrow := Record{Arrival: "AMS", Departure: "AMS", Status: II, Minutes: 55, DepCarrier: "KL", DepFlightFrom: 1200, DepFlightTo: 1299}
	a380 := Record{Arrival: "AMS", Departure: "AMS", Status: II, Minutes: 90, ArrCarrier: "KL", DepCarrier: "LH", ArrAircraft: "380"}
	world := Record{Status: II, Suppress: true, ArrCarrier: "KL", DepCarrier: "ZZ"}
	here := Record{Arrival: "AMS", Departure: "AMS", Status: II, Minutes: 75, ArrCarrier: "KL", DepCarrier: "ZZ"}
	tab := NewTable([]Record{std, arr, dep, wide, narrow, a380, world, here})
	look := func(in, out Flight) Answer {
		return tab.Lookup(Connection{Arrival: "AMS", Departure: "AMS", Status: II, In: in, Out: out})
	}
	if a := look(Flight{Carrier: "BA", Number: 1}, Flight{Carrier: "AF", Number: 2}); a.Minutes != 50 {
		t.Errorf("station standard: %d", a.Minutes)
	}
	if a := look(Flight{Carrier: "KL", Number: 1}, Flight{Carrier: "AF", Number: 2}); a.Minutes != 70 {
		t.Errorf("the delivering carrier's record: %d", a.Minutes)
	}
	// Both sides KLM: the receiving carrier's record outranks the
	// delivering carrier's.
	if a := look(Flight{Carrier: "KL", Number: 1}, Flight{Carrier: "KL", Number: 2}); a.Minutes != 40 {
		t.Errorf("departure side before arrival side: %d", a.Minutes)
	}
	if a := look(Flight{Carrier: "BA", Number: 1}, Flight{Carrier: "KL", Number: 1500}); a.Minutes != 60 {
		t.Errorf("a flight range outranks none: %d", a.Minutes)
	}
	if a := look(Flight{Carrier: "BA", Number: 1}, Flight{Carrier: "KL", Number: 1250}); a.Minutes != 55 {
		t.Errorf("the subset range outranks the larger: %d", a.Minutes)
	}
	// The aircraft type must match the schedule's exactly.
	if a := look(Flight{Carrier: "KL", Number: 1, Aircraft: "380"}, Flight{Carrier: "LH", Number: 2}); a.Minutes != 90 {
		t.Errorf("the A380 record: %d", a.Minutes)
	}
	if a := look(Flight{Carrier: "KL", Number: 1, Aircraft: "388"}, Flight{Carrier: "LH", Number: 2}); a.Minutes != 70 {
		t.Errorf("a 388 is not a 380: %d", a.Minutes)
	}
	// Suppressed worldwide, excepted here.
	if a := look(Flight{Carrier: "KL", Number: 1}, Flight{Carrier: "ZZ", Number: 2}); a.Suppressed || a.Minutes != 75 {
		t.Errorf("an exception at the station outranks a suppression for the world: %+v", a)
	}
	if a := tab.Lookup(Connection{Arrival: "CDG", Departure: "CDG", Status: II, In: Flight{Carrier: "KL", Number: 1}, Out: Flight{Carrier: "ZZ", Number: 2}}); !a.Suppressed {
		t.Errorf("the world suppression elsewhere: %+v", a)
	}
}

// Section 8.2's example. Lists filed: American permits British Airways
// and Lufthansa; British Airways permits American and Vueling; Lufthansa
// permits United.
func TestTheManualsConnectionBuildingFilter(t *testing.T) {
	f := &File{Full: true, Creator: "TEST", Partners: []PartnerList{{"AA", []string{"BA", "LH"}}, {"BA", []string{"AA", "VY"}}, {"LH", []string{"UA"}}}}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatal(err)
	}
	g, err := Parse(&buf)
	if err != nil {
		t.Fatal(err)
	}
	tab := g.Table()
	fl := func(marketing, operating string) Flight {
		return Flight{Carrier: marketing, Number: 1, Operating: operating}
	}
	cases := []struct {
		in, out Flight
		builds  bool
		why     string
	}{
		{fl("AA", ""), fl("AA", ""), true, "online connections are always allowed"},
		{fl("AA", ""), fl("BA", ""), true, "both file and allow each other"},
		{fl("LH", ""), fl("UA", ""), true, "LH allows UA, and UA does not file"},
		{fl("VY", ""), fl("BA", ""), true, "BA allows VY, and VY does not file"},
		{fl("DL", ""), fl("KL", ""), true, "neither files"},
		{fl("AA", ""), fl("LH", ""), false, "AA allows LH, LH does not allow AA"},
		{fl("LH", ""), fl("BA", ""), false, "neither allows the other"},
		{fl("AA", ""), fl("VY", ""), false, "VY is not in AA's list"},
		{fl("BA", "AA"), fl("VY", ""), false, "BA and VY connect, AA and VY do not"},
		{fl("AA", "BA"), fl("VY", "BA"), false, "the operating connection is BA-BA, the marketing carriers do not connect"},
	}
	for _, c := range cases {
		a := tab.Lookup(Connection{Arrival: "LHR", Departure: "LHR", Status: II, In: c.in, Out: c.out})
		if a.Suppressed == c.builds {
			t.Errorf("%s*%s to %s*%s builds %v: %s", c.in.Carrier, c.in.Operating, c.out.Carrier, c.out.Operating, !a.Suppressed, c.why)
		}
	}
}

// A file that is cut short, misnumbered or malformed is refused, and so
// is a record that breaks the conditions on its fields.
func TestMalformedFilesAreRefused(t *testing.T) {
	good := strings.Split(strings.TrimRight(sampleFile(), "\r\n"), "\r\n")
	join := func(lines []string) string { return strings.Join(lines, "\n") + "\n" }
	if _, err := Parse(strings.NewReader(join(good[:len(good)-1]))); err == nil {
		t.Error("a file with no trailer was read")
	}
	skipped := append(append([]string{}, good[:2]...), good[3:]...)
	if _, err := Parse(strings.NewReader(join(skipped))); err == nil {
		t.Error("a file with a record missing was read")
	}
	bad := append([]string{}, good...)
	bad[2] = at(map[int]string{1: "2", 2: "LHR", 5: "0100", 9: "XX", 11: "LHR", 195: "000003"})
	if _, err := Parse(strings.NewReader(join(bad))); err == nil {
		t.Error("a record with status XX was read")
	}
	for _, r := range []Record{
		{Arrival: "LHR", Departure: "LHR", Status: II, Minutes: 60, ArrAircraft: "380", ArrBody: "W"},
		{Arrival: "LHR", Departure: "LHR", Status: II, Minutes: 60, DepFlightFrom: 100},
		{Arrival: "LHR", Departure: "LHR", Status: II, Minutes: 60, PrevState: "CA"},
		{Arrival: "LHR", Departure: "LHR", Status: II, Minutes: 60, ArrOperating: "BA"},
		{Arrival: "LHR", Departure: "LHR", Status: II},
	} {
		if _, err := r.Encode(); err == nil {
			t.Errorf("an invalid record was encoded: %+v", r)
		}
	}
}

// Whatever a record holds, what is written is read back the same.
func FuzzRecordRoundTrip(f *testing.F) {
	f.Add("LHR", "LHR", "II", 60, "BA", "AA", true, "IB", "5", "3", 100, 199, "US", "SCH")
	f.Add("CDG", "ORY", "DI", 180, "", "", false, "", "", "", 0, 0, "", "")
	f.Fuzz(func(t *testing.T, arr, dep, status string, minutes int, ac, dc string, share bool, op, t1, t2 string, f1, f2 int, country, region string) {
		clean := func(s string, n int) string {
			var b []byte
			for i := 0; i < len(s) && len(b) < n; i++ {
				c := s[i]
				if (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
					b = append(b, c)
				}
			}
			return string(b)
		}
		r := Record{Arrival: clean(arr, 3), Departure: clean(dep, 3), Status: status, Minutes: minutes, ArrCarrier: clean(ac, 2), DepCarrier: clean(dc, 2),
			ArrCodeshare: share, ArrOperating: clean(op, 2), ArrTerminal: clean(t1, 2), DepTerminal: clean(t2, 2), DepFlightFrom: f1, DepFlightTo: f2,
			PrevCountry: clean(country, 2), NextRegion: clean(region, 3)}
		if len(r.Arrival) != 3 || len(r.Departure) != 3 || minutes < 1 || minutes > 99*60+59 || f1 < 0 || f2 < f1 || f2 > 9999 {
			return
		}
		if !share {
			r.ArrOperating = ""
		}
		line, err := r.Encode()
		if err != nil {
			return
		}
		if len(line) != 200 {
			t.Fatalf("%d bytes", len(line))
		}
		back, err := parseRecord(line)
		if err != nil {
			t.Fatalf("what was written could not be read: %v\n%q", err, line)
		}
		if back != r {
			t.Fatalf("round trip changed the record:\n%+v\n%+v", r, back)
		}
	})
}
