package avs

import (
	"strings"
	"testing"
	"time"

	"github.com/adamf/jetway/pkg/avail"
)

func now() time.Time { return time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC) }

func TestParseFlightThenStatuses(t *testing.T) {
	m := Parse("AVS\nBA0175/27SEP/LHRJFK\nY/O J/C M/L", now())
	if m.HasErrors() {
		t.Fatalf("errors: %v", m.Diagnostics)
	}
	if len(m.Entries) != 3 {
		t.Fatalf("entries = %d, want 3: %v", len(m.Entries), m.Entries)
	}
	byClass := map[string]avail.Entry{}
	for _, e := range m.Entries {
		byClass[e.Key.Class] = e
	}
	for class, want := range map[string]avail.Status{"Y": avail.Open, "J": avail.Closed, "M": avail.Waitlist} {
		got, ok := byClass[class]
		if !ok {
			t.Errorf("class %s missing", class)
			continue
		}
		if got.Status != want {
			t.Errorf("class %s = %q, want %q", class, got.Status, want)
		}
		if got.Key.Carrier != "BA" || got.Key.FlightNum != "0175" ||
			got.Key.Board != "LHR" || got.Key.Off != "JFK" {
			t.Errorf("class %s key wrong: %+v", class, got.Key)
		}
		if got.Key.Date != "2026-09-27" {
			t.Errorf("class %s date = %q", class, got.Key.Date)
		}
		if got.Source != avail.SourceAVS {
			t.Errorf("source = %q", got.Source)
		}
	}
}

func TestParseSelfContainedLines(t *testing.T) {
	m := Parse("AVS\nBA0175 27SEP LHRJFK Y O\nAA0050 27SEP DFWLHR J C", now())
	if m.HasErrors() {
		t.Fatalf("errors: %v", m.Diagnostics)
	}
	if len(m.Entries) != 2 {
		t.Fatalf("entries = %d: %v", len(m.Entries), m.Entries)
	}
	if m.Entries[0].Key.Carrier != "BA" || m.Entries[0].Status != avail.Open {
		t.Errorf("first entry: %+v", m.Entries[0])
	}
	if m.Entries[1].Key.Carrier != "AA" || m.Entries[1].Status != avail.Closed {
		t.Errorf("second entry: %+v", m.Entries[1])
	}
}

// The numeric form carries seat counts, which bound free sale.
func TestNumericCounts(t *testing.T) {
	m := Parse("AVS\nBA0175/27SEP/LHRJFK\nY/O4 J/O0", now())
	if len(m.Entries) != 2 {
		t.Fatalf("entries = %d: %v", len(m.Entries), m.Entries)
	}
	byClass := map[string]avail.Entry{}
	for _, e := range m.Entries {
		byClass[e.Key.Class] = e
	}
	if y := byClass["Y"]; !y.SeatsKnown || y.Seats != 4 || y.Status != avail.Open {
		t.Errorf("Y = %+v, want 4 seats open", y)
	}
	// Zero seats means closed whatever letter accompanies it: a count is the
	// carrier's actual answer.
	if j := byClass["J"]; j.Status != avail.Closed {
		t.Errorf("J = %+v, want closed on a zero count", j)
	}
}

// An unmapped code must be reported, never guessed. Guessing here grants free
// sale on a class the carrier may have closed.
func TestUnmappedCodeIsAnErrorNotAGuess(t *testing.T) {
	m := Parse("AVS\nBA0175/27SEP/LHRJFK\nY/AS", now())
	if len(m.Entries) != 0 {
		t.Errorf("an unmapped code must produce no belief, got %v", m.Entries)
	}
	if !m.HasErrors() {
		t.Fatalf("expected an error diagnostic, got %v", m.Diagnostics)
	}
	found := false
	for _, d := range m.Diagnostics {
		if d.Code == "unmapped_status_code" && strings.Contains(d.Detail, "AS") {
			found = true
		}
	}
	if !found {
		t.Errorf("diagnostic should name the unmapped code: %v", m.Diagnostics)
	}
}

// A link that knows what its codes mean configures them; that is what the
// standard's bilateral options require.
func TestProfileExtendsTheStatusMap(t *testing.T) {
	p := Default.Clone("carrier-xx")
	p.Status["AS"] = avail.Open
	p.Status["LA"] = avail.Waitlist

	m := p.Parse("AVS\nBA0175/27SEP/LHRJFK\nY/AS J/LA", now())
	if m.HasErrors() {
		t.Fatalf("errors: %v", m.Diagnostics)
	}
	if len(m.Entries) != 2 {
		t.Fatalf("entries = %d", len(m.Entries))
	}
	// The default profile must be unaffected.
	if _, ok := Default.Status["AS"]; ok {
		t.Error("Clone must not mutate the default status map")
	}
}

func TestUnrecognisedLineIsReported(t *testing.T) {
	m := Parse("AVS\nBA0175/27SEP/LHRJFK\nY/O\nSOMETHING ELSE ENTIRELY", now())
	var warned bool
	for _, d := range m.Diagnostics {
		if d.Code == "unrecognised_line" {
			warned = true
		}
	}
	if !warned {
		t.Errorf("expected an unrecognised_line diagnostic: %v", m.Diagnostics)
	}
	// The good line must still have been taken.
	if len(m.Entries) != 1 {
		t.Errorf("entries = %d, want 1", len(m.Entries))
	}
}

// A status line before any flight line has no segment to apply to.
func TestStatusWithoutFlightContext(t *testing.T) {
	m := Parse("AVS\nY/O J/C", now())
	if len(m.Entries) != 0 {
		t.Errorf("entries = %d, want 0 without a flight context", len(m.Entries))
	}
	if len(m.Diagnostics) == 0 {
		t.Error("expected a diagnostic")
	}
}

func TestBuildRoundTrip(t *testing.T) {
	depart := time.Date(2026, time.September, 27, 0, 0, 0, 0, time.UTC)
	in := []avail.Entry{
		{Key: avail.NewKey("BA", "0175", depart, "LHR", "JFK", "Y"), Status: avail.Open, Source: avail.SourceAVS},
		{Key: avail.NewKey("BA", "0175", depart, "LHR", "JFK", "J"), Status: avail.Closed, Source: avail.SourceAVS},
		{Key: avail.NewKey("BA", "0175", depart, "LHR", "JFK", "M"), Status: avail.Open, Seats: 3, SeatsKnown: true, Source: avail.SourceAVS},
		{Key: avail.NewKey("AA", "0050", depart, "DFW", "LHR", "Y"), Status: avail.Waitlist, Source: avail.SourceAVS},
	}
	text := Build(in)
	if !strings.HasPrefix(text, MessageIdentifier) {
		t.Errorf("built message must carry the identifier:\n%s", text)
	}

	m := Parse(text, now())
	if m.HasErrors() {
		t.Fatalf("our own message did not reparse: %v\n%s", m.Diagnostics, text)
	}
	if len(m.Entries) != len(in) {
		t.Fatalf("round trip: %d entries, want %d\n%s", len(m.Entries), len(in), text)
	}
	got := map[avail.Key]avail.Entry{}
	for _, e := range m.Entries {
		got[e.Key] = e
	}
	for _, want := range in {
		g, ok := got[want.Key]
		if !ok {
			t.Errorf("%s missing after round trip", want.Key)
			continue
		}
		if g.Status != want.Status {
			t.Errorf("%s status = %q, want %q", want.Key, g.Status, want.Status)
		}
		if g.SeatsKnown != want.SeatsKnown || g.Seats != want.Seats {
			t.Errorf("%s seats = %d/%v, want %d/%v", want.Key, g.Seats, g.SeatsKnown, want.Seats, want.SeatsKnown)
		}
	}
	// Two flights means two groups, not four lines of repetition.
	if n := strings.Count(text, "BA0175"); n != 1 {
		t.Errorf("classes for one segment should share a flight line, saw %d:\n%s", n, text)
	}
}

// A message feeding the cache is the whole point.
func TestParsedMessageFeedsTheCache(t *testing.T) {
	c := avail.NewCache()
	c.Now = now
	m := Parse("AVS\nBA0175/27SEP/LHRJFK\nY/O4 J/C", now())
	for _, e := range m.Entries {
		c.Put(e)
	}
	depart := time.Date(2026, time.September, 27, 0, 0, 0, 0, time.UTC)

	d, why := c.Decide(avail.NewKey("BA", "0175", depart, "LHR", "JFK", "Y"), 2)
	if d != avail.FreeSale {
		t.Errorf("Y with four seats open should free-sell, got %q (%s)", d, why)
	}
	d, why = c.Decide(avail.NewKey("BA", "0175", depart, "LHR", "JFK", "J"), 1)
	if d != avail.Refuse {
		t.Errorf("J closed should refuse, got %q (%s)", d, why)
	}
	d, _ = c.Decide(avail.NewKey("BA", "0175", depart, "LHR", "JFK", "F"), 1)
	if d != avail.Ask {
		t.Errorf("a class with no broadcast should ask, got %q", d)
	}
}

func TestDefaultMapHoldsOnlyUnambiguousCodes(t *testing.T) {
	// The point of the short map: codes whose meaning is in the paid manual or
	// agreed bilaterally must not be guessed here.
	for _, code := range []string{"AS", "LA", "A", "N"} {
		if _, ok := DefaultStatusMap[code]; ok {
			t.Errorf("%q should not be given a default meaning", code)
		}
	}
	for _, code := range []string{"O", "C", "L", "R"} {
		if _, ok := DefaultStatusMap[code]; !ok {
			t.Errorf("%q should have a default meaning", code)
		}
	}
}
