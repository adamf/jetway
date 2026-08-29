package typeb

import "testing"

func TestPriorityBands(t *testing.T) {
	for code, want := range map[string]PriorityClass{
		"QS": PriorityUrgent, "QU": PriorityUrgent, "QP": PriorityUrgent,
		"QD": PriorityDeferred, "QN": PriorityDeferred,
		"QK": PriorityNormal, "QX": PriorityNormal,
	} {
		if got := ClassOf(code); got != want {
			t.Errorf("ClassOf(%q) = %v, want %v", code, got, want)
		}
	}
	// An unrecognised code must never be starved behind bulk traffic.
	if ClassOf("QZ") != PriorityNormal || ClassOf("") != PriorityNormal {
		t.Error("unknown priority codes must be treated as normal")
	}
	if PriorityUrgent >= PriorityNormal || PriorityNormal >= PriorityDeferred {
		t.Error("bands must sort urgent, normal, deferred")
	}
}

func TestPriorityOf(t *testing.T) {
	cases := map[string]string{
		"QU LHRRMBA\n.LONXX1A 121430\nHELLO\n":           "QU",
		"ZCZC ABC1234\nQD LHRRMBA\n.LONXX1A 121430\nX\n": "QD",
		"\n\n  QN LHRRMBA\n.LONXX1A 121430\nX\n":         "QN",
		".LONXX1A 121430\nheadless message\n":            "",
		"":                                               "",
	}
	for raw, want := range cases {
		if got := PriorityOf([]byte(raw)); got != want {
			t.Errorf("PriorityOf(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestParseChannel(t *testing.T) {
	c, n, ok := ParseChannel("ABC1234")
	if !ok || c != "ABC" || n != 1234 {
		t.Errorf("ParseChannel = %q, %d, %v", c, n, ok)
	}
	if _, n, ok := ParseChannel("0042"); !ok || n != 42 {
		t.Errorf("a bare number is a sequence: %d %v", n, ok)
	}
	if _, _, ok := ParseChannel("NOTASEQUENCE"); ok {
		t.Error("a token with no digits is not a sequence")
	}
}

func TestCheckSequence(t *testing.T) {
	if _, differs := CheckSequence(10, 11, 0); differs {
		t.Error("the next number in order is not a gap")
	}

	g, differs := CheckSequence(10, 14, 0)
	if !differs || g.Missing != 3 || g.Repeat {
		t.Errorf("10 -> 14 = %+v, want 3 missing", g)
	}

	// Backwards means a retransmission or a counter that restarted, which is a
	// different problem from a hole and must not be reported as one.
	g, differs = CheckSequence(10, 7, 0)
	if !differs || !g.Repeat || g.Missing != 0 {
		t.Errorf("10 -> 7 = %+v, want a repeat", g)
	}

	// A counter that rolls over is not a message loss.
	if _, differs := CheckSequence(9999, 1, 9999); differs {
		t.Error("a clean rollover must not report a gap")
	}
	// 9998 then 2 skips 9999 and 1: two messages, not one.
	g, differs = CheckSequence(9998, 2, 9999)
	if !differs || g.Missing != 2 {
		t.Errorf("rollover with a hole = %+v, want 2 missing", g)
	}
	// Without a stated wrap the rollover reads as a large gap, which is the
	// honest answer when the counter width is unknown.
	if g, _ := CheckSequence(9999, 1, 0); !g.Repeat {
		t.Errorf("unknown wrap should not silently absorb a rollover: %+v", g)
	}
}
