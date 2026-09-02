package pnl

import (
	"strings"
	"testing"
)

// A family of six with a ticket each does not fit on one line. The list
// folds the item -- given names, then elements -- onto continuation lines
// inside the Type B limit, and reads it back as one name item.
func TestLongNameItemsFoldAndRoundTrip(t *testing.T) {
	n := Name{Party: 4, Surname: "HUDSON", Givens: []string{"CHRISTOPHERMR", "ELIZABETHMRS", "ALEXANDERMSTR", "CHARLOTTEMISS"},
		Elements: []string{".L/QXVMKU", ".R/TKNE HK1 974-2000024435C1", ".R/TKNE HK1 974-2000024436C1", ".R/CHLD HK1", ".R/WCHR HK1"}}
	parts, err := BuildParts(KindPNL, "WN0100", "26NOV", "BNA", []Group{{Dest: "MDW", Class: "Y", Names: []Name{n, {Party: 1, Surname: "LEE", Givens: []string{"MEIMS"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 {
		t.Fatalf("one part expected, got %d", len(parts))
	}
	for _, ln := range strings.Split(parts[0], "\n") {
		if len(ln) > maxNameLine {
			t.Errorf("line over %d: %q", maxNameLine, ln)
		}
	}
	if !strings.Contains(parts[0], "\n .R/") {
		t.Errorf("elements should continue on an indented line:\n%s", parts[0])
	}
	m, err := Parse(parts[0])
	if err != nil {
		t.Fatalf("Parse: %v\n%s", err, parts[0])
	}
	got := m.Groups[0].Names[0]
	if got.Party != 4 || len(got.Givens) != 4 || len(got.Elements) != 5 || got.Elements[4] != ".R/WCHR HK1" {
		t.Errorf("folded item did not round-trip: %+v", got)
	}
	if m.Groups[0].Names[1].Surname != "LEE" {
		t.Errorf("the name after the folded one was lost: %+v", m.Groups[0].Names)
	}
}

// Pagination counts the lines a folded item takes, not the item: a cabin of
// families with tickets still fits every part inside the Type B envelope.
func TestPartsCountFoldedLines(t *testing.T) {
	var names []Name
	for i := 0; i < 120; i++ {
		names = append(names, Name{Party: 3, Surname: "FAMILY" + string(rune('A'+i%26)),
			Givens:   []string{"CHRISTOPHERMR", "ELIZABETHMRS", "ALEXANDERMSTR"},
			Elements: []string{".L/ABC123", ".R/TKNE HK1 974-2000024435C1", ".R/TKNE HK1 974-2000024436C1", ".R/TKNE HK1 974-2000024437C1"}})
	}
	parts, err := BuildParts(KindPNL, "WN0100", "26NOV", "BNA", []Group{{Dest: "MDW", Class: "Y", Names: names}})
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for i, p := range parts {
		ls := strings.Split(strings.TrimRight(p, "\n"), "\n")
		if len(ls) > 60 {
			t.Errorf("part %d has %d lines", i+1, len(ls))
		}
		m, err := Parse(p)
		if err != nil {
			t.Fatalf("part %d: %v", i+1, err)
		}
		for _, g := range m.Groups {
			total += len(g.Names)
		}
	}
	if total != 120 || len(parts) < 7 {
		t.Errorf("%d names across %d parts", total, len(parts))
	}
}
