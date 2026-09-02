package aftn

import (
	"strings"
	"testing"
)

// Annex 10 Volume II, chapter 4, Note 2 to 4.4.6: the page-copy example.
const annexExample = "ZCZC LPA183\nGG LGGGZRZX LGATKLMW\n201838 EGLLKLMW\n(ARR-KLM123-EGLL-LGAT1830)\nNNNN\n"

func TestParseAnnexExample(t *testing.T) {
	m, err := Parse([]byte(annexExample))
	if err != nil {
		t.Fatal(err)
	}
	if m.TransmissionID != "LPA183" || m.Priority != PriorityRegularity {
		t.Errorf("heading/priority: %+v", m)
	}
	if len(m.Addressees) != 2 || m.Addressees[0] != "LGGGZRZX" || m.Addressees[1] != "LGATKLMW" {
		t.Errorf("addressees %v", m.Addressees)
	}
	if m.FilingTime != "201838" || m.Originator != "EGLLKLMW" {
		t.Errorf("origin %q %q", m.FilingTime, m.Originator)
	}
	if m.Text != "(ARR-KLM123-EGLL-LGAT1830)" {
		t.Errorf("text %q", m.Text)
	}
	if !Looks([]byte(annexExample)) {
		t.Error("Looks rejects Annex 10's own example")
	}
}

func TestEncodeRoundTripsAndKeepsTheLimits(t *testing.T) {
	m := &Message{TransmissionID: "LPA183", Priority: PrioritySafety,
		Addressees: []string{"KJFKZQZX", "KJFKBAWX"}, FilingTime: "261200", Originator: "EGLLBAWO",
		Text: "(DEP-BAW117-EGLL1205-KJFK-DOF/251126)"}
	raw, err := m.Encode(EncodeOptions{CRLF: true})
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.HasPrefix(s, "ZCZC LPA183\r\nFF KJFKZQZX KJFKBAWX\r\n261200 EGLLBAWO\r\n") || !strings.HasSuffix(s, "\r\nNNNN\r\n") {
		t.Errorf("encoded:\n%q", s)
	}
	back, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if back.Text != m.Text || back.Originator != m.Originator || len(back.Addressees) != 2 {
		t.Errorf("round trip: %+v", back)
	}
	// A message without the circuit heading still parses: a terminal hands
	// over what it typed.
	if _, err := Parse([]byte("FF KJFKZQZX\n261200 EGLLBAWO\nHELLO\nNNNN")); err != nil {
		t.Errorf("headless message: %v", err)
	}
	for _, bad := range []Message{
		{Priority: "QU", Addressees: []string{"KJFKZQZX"}, FilingTime: "261200", Originator: "EGLLBAWO"},
		{Priority: PrioritySafety, Addressees: []string{"LHRRMBA"}, FilingTime: "261200", Originator: "EGLLBAWO"},
		{Priority: PrioritySafety, Addressees: []string{"KJFKZQZX"}, FilingTime: "12:00", Originator: "EGLLBAWO"},
		{Priority: PrioritySafety, Addressees: []string{"KJFKZQZX"}, FilingTime: "261200", Originator: "BAW"},
	} {
		if _, err := bad.Encode(EncodeOptions{}); err == nil {
			t.Errorf("encoded a malformed message: %+v", bad)
		}
	}
	long := *m
	long.Text = strings.Repeat("X", MaxLength)
	if _, err := long.Encode(EncodeOptions{}); err == nil {
		t.Error("encoded past the 2 100 character limit")
	}
	many := *m
	for i := 0; i < MaxAddressees; i++ {
		many.Addressees = append(many.Addressees, "KJFKZQZX")
	}
	if _, err := many.Encode(EncodeOptions{}); err == nil {
		t.Error("encoded more than 21 addressees")
	}
}

func TestLooksTellsAFTNFromTypeB(t *testing.T) {
	if Looks([]byte("QU LHRRMBA\n.LONRM1G 261200\nHELLO\n")) {
		t.Error("a Type B message was taken for AFTN")
	}
	if !Looks([]byte("FF KJFKZQZX KJFKBAWX\n261200 EGLLBAWO\n(DEP-BAW117-EGLL1205-KJFK)\nNNNN")) {
		t.Error("an AFTN message without a heading was not recognised")
	}
	if Looks([]byte("UNB+IATA:1+1G+BA+...")) {
		t.Error("EDIFACT was taken for AFTN")
	}
}
