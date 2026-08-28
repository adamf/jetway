package pnr

import (
	"testing"
	"time"
)

func date(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func TestResolveDateForward(t *testing.T) {
	ref := date(2026, time.June, 1)
	got, err := ResolveDate("15JUN", ref)
	if err != nil {
		t.Fatalf("ResolveDate: %v", err)
	}
	if !got.Equal(date(2026, time.June, 15)) {
		t.Errorf("got %s, want 2026-06-15", got.Format("2006-01-02"))
	}
}

// The case that breaks naive implementations: a December date seen in January
// belongs to the coming December, not the one just past.
func TestResolveDateAcrossYearBoundary(t *testing.T) {
	got, err := ResolveDate("20DEC", date(2026, time.January, 5))
	if err != nil {
		t.Fatalf("ResolveDate: %v", err)
	}
	if !got.Equal(date(2026, time.December, 20)) {
		t.Errorf("got %s, want 2026-12-20", got.Format("2006-01-02"))
	}
}

// A January date seen in late December belongs to the next year.
func TestResolveDateJanuaryFromDecember(t *testing.T) {
	got, err := ResolveDate("05JAN", date(2026, time.December, 28))
	if err != nil {
		t.Fatalf("ResolveDate: %v", err)
	}
	if !got.Equal(date(2027, time.January, 5)) {
		t.Errorf("got %s, want 2027-01-05", got.Format("2006-01-02"))
	}
}

// Messages about a departure keep arriving for a while afterwards, so a date a
// few days in the past must resolve backwards rather than jumping a year.
func TestResolveDateRecentPast(t *testing.T) {
	got, err := ResolveDate("28MAY", date(2026, time.June, 1))
	if err != nil {
		t.Fatalf("ResolveDate: %v", err)
	}
	if !got.Equal(date(2026, time.May, 28)) {
		t.Errorf("got %s, want 2026-05-28", got.Format("2006-01-02"))
	}
}

func TestResolveDateLeapDay(t *testing.T) {
	got, err := ResolveDate("29FEB", date(2027, time.December, 1))
	if err != nil {
		t.Fatalf("ResolveDate: %v", err)
	}
	if !got.Equal(date(2028, time.February, 29)) {
		t.Errorf("got %s, want 2028-02-29", got.Format("2006-01-02"))
	}
	// 29 February must never silently become 1 March.
	if _, err := ResolveDate("29FEB2027", date(2027, time.January, 1)); err == nil {
		t.Error("29FEB2027 is not a real date and must be rejected")
	}
}

func TestResolveDateExplicitYear(t *testing.T) {
	got, err := ResolveDate("15JUN26", date(2020, time.January, 1))
	if err != nil {
		t.Fatalf("ResolveDate: %v", err)
	}
	if !got.Equal(date(2026, time.June, 15)) {
		t.Errorf("got %s, want 2026-06-15", got.Format("2006-01-02"))
	}
}

func TestResolveDateRejectsGarbage(t *testing.T) {
	ref := date(2026, time.June, 1)
	for _, in := range []string{"", "1JUN", "32JUN", "15XXX", "00JUN"} {
		if _, err := ResolveDate(in, ref); err == nil {
			t.Errorf("ResolveDate(%q) should have failed", in)
		}
	}
}

func TestFormatDateRoundTrip(t *testing.T) {
	ref := date(2026, time.June, 1)
	in := date(2026, time.August, 3)
	got, err := ResolveDate(FormatDate(in), ref)
	if err != nil {
		t.Fatalf("ResolveDate: %v", err)
	}
	if !got.Equal(in) {
		t.Errorf("round trip gave %s, want %s", got, in)
	}
}

func TestResolveTime(t *testing.T) {
	d := date(2026, time.June, 15)
	got, err := ResolveTime(d, "0830")
	if err != nil {
		t.Fatalf("ResolveTime: %v", err)
	}
	if got.Hour() != 8 || got.Minute() != 30 {
		t.Errorf("got %s", got)
	}
	if _, err := ResolveTime(d, "2560"); err == nil {
		t.Error("2560 must be rejected")
	}
}

// The property that makes the Feistel construction worth using: it is a
// bijection, so no two counters ever produce the same locator.
func TestLocatorAllocatorIsInjective(t *testing.T) {
	a := NewLocatorAllocator([]byte("test-secret"))
	seen := make(map[string]uint64, 200000)
	for i := uint64(0); i < 200000; i++ {
		loc := a.Allocate(i)
		if !ValidLocator(loc) {
			t.Fatalf("counter %d produced invalid locator %q", i, loc)
		}
		if prev, dup := seen[loc]; dup {
			t.Fatalf("collision: counters %d and %d both produced %q", prev, i, loc)
		}
		seen[loc] = i
	}
}

// Consecutive counters must not produce adjacent locators, or the scheme leaks
// booking volume and invites enumeration.
func TestLocatorAllocatorScramblesOrder(t *testing.T) {
	a := NewLocatorAllocator([]byte("test-secret"))
	adjacent := 0
	for i := uint64(0); i < 1000; i++ {
		x, y := a.Allocate(i), a.Allocate(i+1)
		if x[:4] == y[:4] {
			adjacent++
		}
	}
	if adjacent > 5 {
		t.Errorf("%d of 1000 consecutive pairs share a 4-character prefix; ordering is leaking", adjacent)
	}
}

func TestLocatorAllocatorIsDeterministic(t *testing.T) {
	a := NewLocatorAllocator([]byte("secret"))
	b := NewLocatorAllocator([]byte("secret"))
	if a.Allocate(12345) != b.Allocate(12345) {
		t.Error("same secret must give the same locator; restarts must not remap")
	}
	c := NewLocatorAllocator([]byte("other"))
	if a.Allocate(12345) == c.Allocate(12345) {
		t.Error("different secrets should give different locators")
	}
}

func TestLocatorAlphabetExcludesConfusables(t *testing.T) {
	for _, c := range "IO01" {
		for _, a := range LocatorAlphabet {
			if a == c {
				t.Errorf("alphabet must not contain %q", string(c))
			}
		}
	}
	if len(LocatorAlphabet) != 32 {
		t.Errorf("alphabet is %d characters; the space must stay a power of two", len(LocatorAlphabet))
	}
}

func TestNormaliseLocator(t *testing.T) {
	got, err := NormaliseLocator(" abc-23d ")
	if err != nil {
		t.Fatalf("NormaliseLocator: %v", err)
	}
	if got != "ABC23D" {
		t.Errorf("got %q, want ABC23D", got)
	}
	// Characters outside the alphabet must fail rather than be "corrected"
	// into somebody else's locator.
	for _, in := range []string{"ABC120", "ABCIOD", "ABC", "ABCDEFG"} {
		if _, err := NormaliseLocator(in); err == nil {
			t.Errorf("NormaliseLocator(%q) should have failed", in)
		}
	}
}

func TestRecomputeStatusAndRefs(t *testing.T) {
	p := &PNR{Segments: []Segment{
		{Carrier: "BA", Status: "HK"},
		{Carrier: "AA", Status: "XX"},
	}}
	p.Recompute()
	if p.Status != StatusOpen {
		t.Errorf("Status = %q, want open", p.Status)
	}
	if p.Segments[0].Ref != 1 || p.Segments[1].Ref != 2 {
		t.Error("segment refs must be renumbered densely")
	}
	p.Segments[0].Status = "XX"
	p.Recompute()
	if p.Status != StatusCancelled {
		t.Errorf("Status = %q, want cancelled once every segment is dead", p.Status)
	}
}

func TestRedaction(t *testing.T) {
	p := &PNR{
		SSRs: []SSR{
			{Code: "DOCS", Text: "P/GBR/123456789", Sensitive: true},
			{Code: "VGML", Text: "VEGETARIAN"},
		},
		Contacts: []Contact{{Type: "phone", Text: "+44 20 7777 7777"}},
	}
	r := p.Redacted()
	if r.SSRs[0].Text != "[redacted]" {
		t.Error("DOCS text must be redacted")
	}
	if r.SSRs[1].Text != "VEGETARIAN" {
		t.Error("a non-sensitive SSR must survive redaction")
	}
	if r.Contacts[0].Text != "[redacted]" {
		t.Error("contact details must be redacted")
	}
	// The original must be untouched.
	if p.SSRs[0].Text != "P/GBR/123456789" {
		t.Error("Redacted must not mutate the receiver")
	}
}

func TestAwaitingReply(t *testing.T) {
	p := &PNR{Segments: []Segment{{Status: "HK"}, {Status: "HN"}}}
	if !p.AwaitingReply() {
		t.Error("a segment at HN means we are still waiting")
	}
	p.Segments[1].Status = "HK"
	if p.AwaitingReply() {
		t.Error("all segments settled; nothing is outstanding")
	}
}
