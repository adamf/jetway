package edifact

import (
	"strings"
	"testing"
)

func subjectInterchange(t *testing.T) *Interchange {
	t.Helper()
	ic := NewInterchange(UNBParams{
		CharsetID: "UNOA", SyntaxVersion: 3,
		Sender:    Party{ID: "AA", Qualifier: "ZZ"},
		Recipient: Party{ID: "1J", Qualifier: "ZZ"},
		Date:      "260829", Time: "1200", ControlRef: "IC0001",
	})
	ic.AddMessage("1", MessageID{Type: "PAORES", Version: "96", Release: "1", ControllingAgency: "IA"},
		Seg("MSG", Comp("", "11")))
	ic.Finalize()
	return ic
}

func TestCheckAcknowledgesACleanInterchange(t *testing.T) {
	ic := subjectInterchange(t)
	r := Check(ic)

	if r.Action != ActionAcknowledged {
		t.Errorf("Action = %q, want 7", r.Action)
	}
	if r.Error != "" {
		t.Errorf("a clean interchange must report no syntax error, got %q", r.Error)
	}
	if r.ControlRef != "IC0001" {
		t.Errorf("ControlRef = %q, want the subject's UNB 0020", r.ControlRef)
	}
	if len(r.Messages) != 1 || r.Messages[0].Action != ActionAcknowledged {
		t.Fatalf("expected one acknowledged message, got %+v", r.Messages)
	}
	if r.Messages[0].ID.Type != "PAORES" {
		t.Errorf("message identifier not carried through: %+v", r.Messages[0].ID)
	}
}

func TestCheckRejectsAndNamesTheError(t *testing.T) {
	// A UNZ that disagrees with its UNB is exactly what CONTRL exists to say.
	raw := []byte("UNB+UNOA:3+AA:ZZ+1J:ZZ+260829:1200+IC0001'" +
		"UNH+1+PAORES:96:1:IA'MSG+:11'UNT+3+1'" +
		"UNZ+1+WRONGREF'")
	ic, err := Parse(raw, ParseOptions{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !ic.HasErrors() {
		t.Fatal("fixture is meant to contain a control reference mismatch")
	}

	r := Check(ic)
	if r.Action != ActionRejected {
		t.Errorf("Action = %q, want 4", r.Action)
	}
	if r.Error != ReferencesDoNotMatch {
		t.Errorf("Error = %q (%s), want 28", r.Error, r.Error.Meaning())
	}
	if r.ServiceTag != TagUNZ {
		t.Errorf("ServiceTag = %q, want UNZ", r.ServiceTag)
	}
	// A rejected interchange must not also list its messages as acknowledged.
	if len(r.Messages) != 0 {
		t.Errorf("a rejection must not acknowledge messages inside it: %+v", r.Messages)
	}
}

func TestCheckFallsBackToUnspecified(t *testing.T) {
	ic := &Interchange{}
	ic.diag(Error, 0, -1, "something_we_never_mapped", "invented")
	r := Check(ic)
	if r.Error != UnspecifiedError {
		t.Errorf("Error = %q, want 18; an unmapped diagnostic must not invent a code", r.Error)
	}
}

func TestBuildProducesTheDocumentedStructure(t *testing.T) {
	r := Check(subjectInterchange(t))
	out, err := r.Build(CONTRLOptions{
		Sender:     Party{ID: "1J", Qualifier: "ZZ"},
		Recipient:  Party{ID: "AA", Qualifier: "ZZ"},
		ControlRef: "CT0001", Date: "260829", Time: "1201", SyntaxVersion: 3,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	raw, err := out.Encode(EncodeOptions{})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	s := string(raw)

	// The message identifier is fixed by the standard.
	if !strings.Contains(s, "UNH+1+CONTRL:D:3:UN'") {
		t.Errorf("CONTRL message identifier missing:\n%s", s)
	}
	// UCI: subject reference, sender, recipient, action.
	if !strings.Contains(s, "UCI+IC0001+AA:ZZ+1J:ZZ+7'") {
		t.Errorf("UCI not as specified:\n%s", s)
	}
	if !strings.Contains(s, "UCM+1+PAORES:96:1:IA+7'") {
		t.Errorf("UCM not as specified:\n%s", s)
	}
	// A CONTRL travels back the way the interchange came.
	if !strings.Contains(s, "UNB+UNOA:3+1J:ZZ+AA:ZZ+260829:1201+CT0001'") {
		t.Errorf("CONTRL envelope should reverse the parties:\n%s", s)
	}
}

func TestBuildSelectsReleaseFromSyntaxVersion(t *testing.T) {
	r := Receipt(subjectInterchange(t))
	for _, c := range []struct {
		syntax int
		want   string
	}{{3, "CONTRL:D:3:UN"}, {4, "CONTRL:D:4:UN"}} {
		out, err := r.Build(CONTRLOptions{SyntaxVersion: c.syntax, ControlRef: "X"})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := out.Encode(EncodeOptions{})
		if !strings.Contains(string(raw), c.want) {
			t.Errorf("syntax %d produced %q, want %s", c.syntax, raw, c.want)
		}
	}
}

func TestReceiptSaysNothingAboutSyntax(t *testing.T) {
	r := Receipt(subjectInterchange(t))
	if r.Action != ActionReceived {
		t.Errorf("Action = %q, want 8", r.Action)
	}
	if len(r.Messages) != 0 {
		t.Error("a receipt reports on the interchange only, not the messages inside it")
	}
}

func TestCONTRLRoundTrip(t *testing.T) {
	original := &Report{
		ControlRef: "IC0042",
		Sender:     Party{ID: "AA", Qualifier: "ZZ"},
		Recipient:  Party{ID: "1J", Qualifier: "ZZ"},
		Action:     ActionRejected,
		Messages: []MessageReport{{
			Reference: "7",
			ID:        MessageID{Type: "PAOREQ", Version: "96", Release: "1", ControllingAgency: "IA"},
			Action:    ActionRejected,
			Error:     InvalidValue,
			Segments: []SegmentReport{{
				Position: 4, Error: NotSupportedHere,
				Elements: []ElementReport{
					{Error: MissingRequired, Position: 2},
					{Error: DataElementTooLong, Position: 3, Component: 2},
				},
			}},
		}},
	}
	out, err := original.Build(CONTRLOptions{
		Sender: Party{ID: "1J"}, Recipient: Party{ID: "AA"}, ControlRef: "CT9",
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	raw, err := out.Encode(EncodeOptions{})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	back, err := Parse(raw, ParseOptions{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(back.Messages) != 1 {
		t.Fatalf("expected one message, got %d", len(back.Messages))
	}
	got, err := ParseCONTRL(back.Messages[0])
	if err != nil {
		t.Fatalf("ParseCONTRL: %v", err)
	}

	if got.ControlRef != original.ControlRef || got.Action != original.Action {
		t.Errorf("interchange level lost: %+v", got)
	}
	if len(got.Messages) != 1 {
		t.Fatalf("message level lost: %+v", got.Messages)
	}
	m := got.Messages[0]
	if m.Reference != "7" || m.ID.Type != "PAOREQ" || m.Action != ActionRejected || m.Error != InvalidValue {
		t.Errorf("message report lost: %+v", m)
	}
	if len(m.Segments) != 1 {
		t.Fatalf("segment level lost: %+v", m.Segments)
	}
	if m.Segments[0].Position != 4 || m.Segments[0].Error != NotSupportedHere {
		t.Errorf("segment report lost: %+v", m.Segments[0])
	}
	els := m.Segments[0].Elements
	if len(els) != 2 {
		t.Fatalf("element level lost: %+v", els)
	}
	if els[0].Error != MissingRequired || els[0].Position != 2 || els[0].Component != 0 {
		t.Errorf("element 0 = %+v", els[0])
	}
	// A component position must survive: it is the difference between "this
	// element is wrong" and "the second half of this composite is wrong".
	if els[1].Error != DataElementTooLong || els[1].Position != 3 || els[1].Component != 2 {
		t.Errorf("element 1 = %+v", els[1])
	}
}

func TestParseCONTRLRejectsOtherMessages(t *testing.T) {
	ic := subjectInterchange(t)
	if _, err := ParseCONTRL(ic.Messages[0]); err == nil {
		t.Error("ParseCONTRL accepted a PAORES")
	}
	if IsCONTRL(ic.Messages[0]) {
		t.Error("IsCONTRL matched a PAORES")
	}
}

func TestCodeListsMatchThePublishedStandard(t *testing.T) {
	// Spot-checks against the UNSM CONTRL D.3 code lists. If these drift, the
	// gateway is telling partners something the standard does not define.
	if ActionRejected != "4" || ActionAcknowledged != "7" || ActionReceived != "8" {
		t.Error("0083 action codes do not match the standard")
	}
	for code, want := range map[SyntaxError]string{
		"2":  "syntax version or level not supported",
		"13": "missing",
		"26": "duplicate detected",
		"28": "references do not match",
		"29": "control count does not match number of instances received",
		"39": "data element too long",
	} {
		if got := code.Meaning(); got != want {
			t.Errorf("0085 code %s = %q, want %q", code, got, want)
		}
	}
	if SyntaxError("999").Meaning() != "" {
		t.Error("an unknown code must not claim a meaning")
	}
}
