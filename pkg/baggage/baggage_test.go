package baggage

import "testing"

// The minimum BSM as the freely published reproduction of the practice
// prints it: version, outbound flight, one tag.
const minimumBSM = `BSM
.V/1TZRH
.F/SR101/18APR/JFK/F
.N/0085123456003
ENDBSM`

// The worked BPM: a bag loaded at LHR with its passenger and seat.
const sampleBPM = `BPM
.V/1TLHR
.F/BA117/03OCT/SEL/J
.N/0085123456002
.P/SMITH/JOHN
.S/Y/10A/C
ENDBPM`

func TestParseMinimumBSM(t *testing.T) {
	m, err := Parse(minimumBSM)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m.Kind != KindBSM || m.Version != "1TZRH" {
		t.Errorf("header = %+v", m)
	}
	if m.Outbound == nil || m.Outbound.Flight != "SR101" || m.Outbound.Date != "18APR" ||
		m.Outbound.City != "JFK" || m.Outbound.Class != "F" {
		t.Errorf("outbound = %+v", m.Outbound)
	}
	if len(m.Tags) != 1 || m.Tags[0].Number != "0085123456" || m.Tags[0].Count != 3 {
		t.Errorf("tags = %+v: the last three digits are the consecutive count", m.Tags)
	}
}

func TestParseBPMWithPassenger(t *testing.T) {
	m, err := Parse(sampleBPM)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m.Kind != KindBPM || m.Surname != "SMITH" || len(m.Givens) != 1 || m.Givens[0] != "JOHN" {
		t.Errorf("passenger = %q %v", m.Surname, m.Givens)
	}
	if len(m.Elements) != 1 || m.Elements[0] != ".S/Y/10A/C" {
		t.Errorf("unmodelled elements = %v, want the reconciliation carried verbatim", m.Elements)
	}
}

func TestBuildRoundTrips(t *testing.T) {
	for _, text := range []string{minimumBSM, sampleBPM} {
		m, err := Parse(text)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		built, err := Build(m)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if built != text {
			t.Errorf("round trip changed the message.\nwant:\n%s\ngot:\n%s", text, built)
		}
	}
}

func TestChangeAndDelete(t *testing.T) {
	m, err := Parse("BSM\nDEL\n.V/1LLGW\n.F/U2123/01OCT/AMS/Y\n.N/0999000111001\nENDBSM")
	if err != nil {
		t.Fatal(err)
	}
	if m.Change != "DEL" {
		t.Errorf("change = %q, want DEL", m.Change)
	}
}

func TestTagWithoutAnyBagIsRefused(t *testing.T) {
	if _, err := Parse("BSM\n.V/1LLGW\nENDBSM"); err == nil {
		t.Error("a bag message with no tag parsed anyway")
	}
	if _, err := Build(&Message{Kind: KindBSM}); err == nil {
		t.Error("a bag message with no tag built anyway")
	}
}

func TestIsBaggage(t *testing.T) {
	if !IsBaggage(minimumBSM) || !IsBaggage(sampleBPM) {
		t.Error("the samples are not recognised")
	}
	if IsBaggage("MVT\nBA117/03OCT.GABCD.LHR") {
		t.Error("a movement is not a bag message")
	}
}
