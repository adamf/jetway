package ssim

import (
	"strings"
	"testing"
)

func TestParseASMCancellation(t *testing.T) {
	m, err := Parse("ASM\nUTC\nCNL\nBA0117/16DEC\nLHR JFK")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m.Kind != KindASM || m.Action != ActionCancel {
		t.Errorf("Kind/Action = %s/%s", m.Kind, m.Action)
	}
	if m.Flight.Carrier != "BA" || m.Flight.Number != "0117" {
		t.Errorf("Flight = %+v", m.Flight)
	}
	if m.Period.From != "16DEC" || !m.Period.Single() {
		t.Errorf("Period = %+v, want the single date from the flight line", m.Period)
	}
	if len(m.Legs) != 1 || m.Legs[0].Board != "LHR" || m.Legs[0].Off != "JFK" {
		t.Errorf("Legs = %+v", m.Legs)
	}
	if m.HasErrors() {
		t.Errorf("unexpected errors: %v", m.Diagnostics)
	}
}

func TestParseSSMWithPeriodAndFrequency(t *testing.T) {
	m, err := Parse("SSM\nUTC\nNEW\nBA0117\n01JUL-15AUG 135\nLHR 0900 JFK 1200 744")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m.Kind != KindSSM || m.Action != ActionNew {
		t.Errorf("Kind/Action = %s/%s", m.Kind, m.Action)
	}
	if m.Period.From != "01JUL" || m.Period.To != "15AUG" || m.Period.Days != "135" {
		t.Errorf("Period = %+v", m.Period)
	}
	if m.Period.Single() {
		t.Error("a two-date period is not a single date")
	}
	l := m.Legs
	if len(l) != 1 || l[0].Depart != "0900" || l[0].Arrive != "1200" || l[0].Equipment != "744" {
		t.Errorf("Legs = %+v", l)
	}
}

func TestTimeModeIsCarried(t *testing.T) {
	m, err := Parse("ASM\nLT\nTIM\nLH0400/12JAN\nFRA 0800 LHR 0830")
	if err != nil {
		t.Fatal(err)
	}
	// Getting the mode wrong moves every flight in the message by the
	// station's offset, so it must survive parsing.
	if m.TimeMode != LocalTime {
		t.Errorf("TimeMode = %q, want LT", m.TimeMode)
	}
	// UTC is the default when nothing says otherwise.
	d, _ := Parse("ASM\nCNL\nLH0400/12JAN")
	if d.TimeMode != UTC {
		t.Errorf("default TimeMode = %q, want UTC", d.TimeMode)
	}
}

func TestActionSetsDifferBetweenSSMAndASM(t *testing.T) {
	// RIN is an ad hoc concept; SKD is a period concept. Using one in the
	// other's message means the sender and this profile disagree.
	if ActionReinstate.ValidFor(KindSSM) {
		t.Error("RIN is not an SSM action")
	}
	if !ActionReinstate.ValidFor(KindASM) {
		t.Error("RIN is an ASM action")
	}
	if !ActionSchedule.ValidFor(KindSSM) || ActionSchedule.ValidFor(KindASM) {
		t.Error("SKD belongs to SSM only")
	}

	m, err := Parse("SSM\nUTC\nRIN\nBA0117\n01JUL")
	if err != nil {
		t.Fatal(err)
	}
	var warned bool
	for _, d := range m.Diagnostics {
		if d.Code == "action_not_in_set" {
			warned = true
		}
	}
	if !warned {
		t.Errorf("an action from the wrong set must be flagged: %v", m.Diagnostics)
	}
	// Flagged, not refused: the message still decodes.
	if m.Action != ActionReinstate {
		t.Errorf("Action = %q, want it kept", m.Action)
	}
}

func TestUnrecognisedLinesAreKept(t *testing.T) {
	m, err := Parse("ASM\nUTC\nADM\nBA0117/16DEC\nSI SOMETHING A CARRIER INVENTED")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Fragments) != 1 {
		t.Fatalf("Fragments = %+v, want the unrecognised line kept", m.Fragments)
	}
	if !strings.Contains(m.Fragments[0], "SOMETHING A CARRIER INVENTED") {
		t.Errorf("fragment lost its content: %q", m.Fragments[0])
	}
	// A dialect gap is data, not an error.
	if m.HasErrors() {
		t.Errorf("an unrecognised line must not fail the message: %v", m.Diagnostics)
	}
}

func TestFlightKeyIgnoresLeadingZeros(t *testing.T) {
	// Carriers write the same flight both ways, and a schedule change that
	// does not match a held segment is a change nobody acts on.
	a := Flight{Carrier: "BA", Number: "0117"}
	b := Flight{Carrier: "BA", Number: "117"}
	if a.Key() != b.Key() {
		t.Errorf("%q and %q must match", a.Key(), b.Key())
	}
}

func TestRoundTrip(t *testing.T) {
	for _, text := range []string{
		"ASM\nUTC\nCNL\nBA0117/16DEC\nLHR JFK",
		"SSM\nUTC\nNEW\nBA0117\n01JUL-15AUG 135\nLHR 0900 JFK 1200 744",
		"ASM\nLT\nEQT\nLH0400/12JAN\nFRA 0800 LHR 0830 320",
	} {
		m, err := Parse(text)
		if err != nil {
			t.Fatalf("Parse(%q): %v", text, err)
		}
		built := m.Build()
		again, err := Parse(built)
		if err != nil {
			t.Fatalf("re-parse of %q: %v", built, err)
		}
		if again.Build() != built {
			t.Errorf("not a fixed point:\n first: %q\nsecond: %q", built, again.Build())
		}
		if again.Action != m.Action || again.Flight != m.Flight || again.Period != m.Period {
			t.Errorf("round trip lost detail:\n%+v\n%+v", m, again)
		}
		if len(again.Legs) != len(m.Legs) {
			t.Errorf("legs lost: %+v vs %+v", m.Legs, again.Legs)
		}
	}
}

func TestIsSchedule(t *testing.T) {
	if !IsSchedule("SSM\nUTC\nNEW\nBA0117") || !IsSchedule("ASM\nUTC\nCNL\nBA0117") {
		t.Error("SSM and ASM must be recognised")
	}
	if IsSchedule("SSR VGML BA HK1 LHRJFK0175Y15JUN") {
		t.Error("an AIRIMP line is not a schedule message")
	}
	if IsSchedule("") {
		t.Error("empty text is not a schedule message")
	}
	if _, err := Parse("MVT\nBA018/25"); err == nil {
		t.Error("Parse accepted a movement message")
	}
}
