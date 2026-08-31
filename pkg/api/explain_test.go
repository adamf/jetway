package api

import (
	"strings"
	"testing"
)

// An availability message must be explained as availability. Explaining it with
// the reservation grammar produced a wall of "unrecognised element" warnings
// for a message that was in fact understood and applied.
func TestExplainAvailability(t *testing.T) {
	raw := []byte("QU LONRM1J\r\n.DFWRMAA 290315\r\n" +
		"AVS\r\nAA0050/28SEP/DFWLHR\r\nF/O2 J/O5 Y/O3 M/O5 Z/C\r\n")

	e := Explain(raw)
	if !strings.Contains(e.Format, "AVS") {
		t.Errorf("format = %q, want an AVS format", e.Format)
	}
	if !strings.Contains(e.Summary, "5 entries") {
		t.Errorf("summary = %q, want the entry count", e.Summary)
	}
	for _, d := range e.Diagnostics {
		if d.Layer == "airimp" {
			t.Errorf("availability must not be run through the reservation grammar: %+v", d)
		}
		if d.Severity == "warn" && d.Code == "unrecognised_element" {
			t.Errorf("unexpected unrecognised element: %+v", d)
		}
	}
	if len(e.Parts) != 1 {
		t.Fatalf("parts = %d, want one per segment", len(e.Parts))
	}
	if !strings.Contains(e.Parts[0].Wire, "AA0050") {
		t.Errorf("part does not name the flight: %q", e.Parts[0].Wire)
	}
	if n := len(e.Parts[0].Fields); n != 5 {
		t.Errorf("fields = %d, want one per class", n)
	}
	// The envelope is still Type B and must still be shown.
	var sawOrigin bool
	for _, f := range e.Envelope {
		if f.Name == "Origin" && f.Value == "DFWRMAA" {
			sawOrigin = true
		}
	}
	if !sawOrigin {
		t.Errorf("the Type B envelope should still be explained: %+v", e.Envelope)
	}
}

// A booking must still be explained with the reservation grammar.
func TestExplainBookingIsUnaffected(t *testing.T) {
	raw := []byte("QU LHRRMBA\r\n.LONRM1J 121430\r\nSS\r\nBA0175Y15JUNLHRJFKNN1\r\n1SMITH/JOHNMR\r\n")
	e := Explain(raw)
	if !strings.Contains(e.Summary, "AIRIMP") {
		t.Errorf("summary = %q", e.Summary)
	}
	if len(e.Parts) != 2 {
		t.Errorf("parts = %d, want segment and name", len(e.Parts))
	}
}
