package edifact

import (
	"strings"
	"testing"
)

// A representative IATA reservation interchange. Segment structure is
// illustrative; the syntax layer must handle it regardless of semantics.
const samplePAORES = "UNB+UNOA:4+1A:ZZ+BA:ZZ+250815:1430+000000001++PAORES'" +
	"UNH+1+PAORES:96:1:IA'" +
	"MSG+:22'" +
	"ORG+1A:MUC+12345678+MUC+++A+EN'" +
	"TIF+SMITH:JOHN MR'" +
	"SSR+VGML:HK:1:BA'" +
	"TVL+150625:0800:150625:1100+LHR+JFK+BA+0175:Y'" +
	"RCI+BA:ABC123'" +
	"UNT+8+1'" +
	"UNZ+1+000000001'"

func TestParseInterchange(t *testing.T) {
	ic, err := Parse([]byte(samplePAORES), ParseOptions{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if ic.HasErrors() {
		t.Fatalf("unexpected errors: %v", ic.Diagnostics)
	}
	if len(ic.Messages) != 1 {
		t.Fatalf("Messages = %d, want 1", len(ic.Messages))
	}
	m := ic.Messages[0]
	if got := m.ID().String(); got != "PAORES:96:1:IA" {
		t.Errorf("message id = %q", got)
	}
	if got := ic.Sender().ID; got != "1A" {
		t.Errorf("sender = %q, want 1A", got)
	}
	if got := ic.Recipient().ID; got != "BA" {
		t.Errorf("recipient = %q, want BA", got)
	}
	if got := ic.ControlRef(); got != "000000001" {
		t.Errorf("control ref = %q", got)
	}
	if _, v := ic.SyntaxIdentifier(); v != 4 {
		t.Errorf("syntax version = %d, want 4", v)
	}
	if len(m.Segments) != 6 {
		t.Errorf("body segments = %d, want 6", len(m.Segments))
	}
	tvl, ok := m.First("TVL")
	if !ok {
		t.Fatal("TVL segment not found")
	}
	if got := tvl.Get(0, 1); got != "0800" {
		t.Errorf("TVL departure time = %q, want 0800", got)
	}
	if got := tvl.Value(1); got != "LHR" {
		t.Errorf("TVL board point = %q, want LHR", got)
	}
	if got := tvl.Get(4, 1); got != "Y" {
		t.Errorf("TVL class = %q, want Y", got)
	}
	// Out-of-range access must be safe, not panic: senders truncate freely.
	if got := tvl.Get(99, 99); got != "" {
		t.Errorf("out-of-range Get = %q, want empty", got)
	}
}

func TestRoundTripByteIdentical(t *testing.T) {
	ic, err := Parse([]byte(samplePAORES), ParseOptions{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	out, err := ic.Encode(EncodeOptions{})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if string(out) != samplePAORES {
		t.Errorf("round trip differs:\n got %q\nwant %q", out, samplePAORES)
	}
}

func TestUNAServiceStringAdvice(t *testing.T) {
	in := "UNA:+.? '\nUNB+UNOA:3+A+B+250101:0000+1'\nUNH+1+PAORES:96:1:IA'\nUNT+2+1'\nUNZ+1+1'\n"
	ic, err := Parse([]byte(in), ParseOptions{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !ic.HadUNA {
		t.Error("expected HadUNA")
	}
	if ic.HasErrors() {
		t.Errorf("diagnostics: %v", ic.Diagnostics)
	}
}

func TestUNACustomSeparators(t *testing.T) {
	// Non-default service characters: component '#', element '|', release '!',
	// terminator '~'.
	in := "UNA#|.! ~UNB|UNOA#3|A|B|250101#0000|1~UNH|1|PAORES#96#1#IA~UNT|2|1~UNZ|1|1~"
	ic, err := Parse([]byte(in), ParseOptions{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if ic.HasErrors() {
		t.Fatalf("diagnostics: %v", ic.Diagnostics)
	}
	if got := ic.Messages[0].ID().Type; got != "PAORES" {
		t.Errorf("message type = %q", got)
	}
	if ic.Syntax.ComponentSep != '#' || ic.Syntax.ElementSep != '|' || ic.Syntax.SegmentTerm != '~' {
		t.Errorf("syntax not taken from UNA: %+v", ic.Syntax)
	}
	// Re-encoding must emit UNA, or the receiver cannot decode it.
	out, err := ic.Encode(EncodeOptions{})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !strings.HasPrefix(string(out), "UNA#|.! ~") {
		t.Errorf("encoded output must lead with UNA: %q", out[:20])
	}
}

func TestReleaseCharacter(t *testing.T) {
	// "SMITH?+SON" is one component containing a literal '+'.
	// "A??B" is a literal '?'.
	in := "UNB+UNOA:3+A+B+250101:0000+1'UNH+1+X:1:1:IA'FTX+SMITH?+SON:A??B:C?:D?'E'UNT+3+1'UNZ+1+1'"
	ic, err := Parse([]byte(in), ParseOptions{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	ftx, ok := ic.Messages[0].First("FTX")
	if !ok {
		t.Fatal("FTX not found")
	}
	want := []string{"SMITH+SON", "A?B", "C:D'E"}
	got := []string(ftx.Elem(0).First())
	if len(got) != len(want) {
		t.Fatalf("components = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("component %d = %q, want %q", i, got[i], want[i])
		}
	}
	// Round-tripping must re-escape.
	out, err := ic.Encode(EncodeOptions{})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if string(out) != in {
		t.Errorf("release round trip:\n got %q\nwant %q", out, in)
	}
}

func TestEmptyElementsPreservedPositionally(t *testing.T) {
	in := "UNB+UNOA:3+A+B+250101:0000+1'UNH+1+X:1:1:IA'ORG+1A:MUC++MUC+++A'UNT+3+1'UNZ+1+1'"
	ic, err := Parse([]byte(in), ParseOptions{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	org, _ := ic.Messages[0].First("ORG")
	if n := len(org.Elements); n != 6 {
		t.Fatalf("elements = %d, want 6 (interior empties must be kept)", n)
	}
	if org.Value(1) != "" || org.Value(3) != "" || org.Value(4) != "" {
		t.Errorf("empty elements lost: %+v", org.Elements)
	}
	if org.Value(2) != "MUC" || org.Value(5) != "A" {
		t.Errorf("element positions shifted: %+v", org.Elements)
	}
}

func TestEncodeTruncatesTrailingEmpties(t *testing.T) {
	s := Seg("ORG", Comp("1A", "MUC", ""), Simple(""), Simple("X"), Simple(""), Simple(""))
	if got, want := s.String(), "ORG+1A:MUC++X'"; got != want {
		t.Errorf("String = %q, want %q", got, want)
	}
}

func TestUNTCountMismatchDetected(t *testing.T) {
	in := "UNB+UNOA:3+A+B+250101:0000+1'UNH+1+X:1:1:IA'TIF+A'UNT+9+1'UNZ+1+1'"
	ic, _ := Parse([]byte(in), ParseOptions{})
	if !hasCode(ic, "unt_count_mismatch") {
		t.Errorf("expected unt_count_mismatch, got %v", ic.Diagnostics)
	}
}

func TestUNZControlRefMismatchDetected(t *testing.T) {
	in := "UNB+UNOA:3+A+B+250101:0000+1'UNH+1+X:1:1:IA'UNT+2+1'UNZ+1+999'"
	ic, _ := Parse([]byte(in), ParseOptions{})
	if !hasCode(ic, "control_ref_mismatch") {
		t.Errorf("expected control_ref_mismatch, got %v", ic.Diagnostics)
	}
}

func TestUNZMessageCountMismatchDetected(t *testing.T) {
	in := "UNB+UNOA:3+A+B+250101:0000+1'UNH+1+X:1:1:IA'UNT+2+1'UNZ+7+1'"
	ic, _ := Parse([]byte(in), ParseOptions{})
	if !hasCode(ic, "unz_count_mismatch") {
		t.Errorf("expected unz_count_mismatch, got %v", ic.Diagnostics)
	}
}

func TestStrictModeReturnsError(t *testing.T) {
	in := "UNB+UNOA:3+A+B+250101:0000+1'UNH+1+X:1:1:IA'UNT+9+1'UNZ+1+1'"
	if _, err := Parse([]byte(in), ParseOptions{Strict: true}); err == nil {
		t.Error("expected strict mode to reject a miscounted UNT")
	}
	if _, err := Parse([]byte(in), ParseOptions{}); err != nil {
		t.Errorf("lenient mode must not fail: %v", err)
	}
}

// Syntax version 4 enables the repetition separator by default. An interchange
// that declares v4 without a UNA must still decode '*' as a repetition, which
// requires re-reading the input once UNB has been seen.
func TestSyntaxV4RepetitionWithoutUNA(t *testing.T) {
	in := "UNB+UNOA:4+A+B+250101:0000+1'UNH+1+X:1:1:IA'FTX+RED*GREEN*BLUE'UNT+3+1'UNZ+1+1'"
	ic, err := Parse([]byte(in), ParseOptions{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	ftx, _ := ic.Messages[0].First("FTX")
	e := ftx.Elem(0)
	if len(e) != 3 {
		t.Fatalf("repetitions = %d, want 3: %+v", len(e), e)
	}
	if e[0].Get(0) != "RED" || e[2].Get(0) != "BLUE" {
		t.Errorf("repetitions decoded wrong: %+v", e)
	}
	out, _ := ic.Encode(EncodeOptions{})
	if string(out) != in {
		t.Errorf("v4 repetition round trip:\n got %q\nwant %q", out, in)
	}
}

// The same bytes under syntax version 3 must treat '*' as ordinary data.
func TestSyntaxV3AsteriskIsData(t *testing.T) {
	in := "UNB+UNOA:3+A+B+250101:0000+1'UNH+1+X:1:1:IA'FTX+RED*GREEN'UNT+3+1'UNZ+1+1'"
	ic, err := Parse([]byte(in), ParseOptions{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	ftx, _ := ic.Messages[0].First("FTX")
	if got := ftx.Value(0); got != "RED*GREEN" {
		t.Errorf("v3 asterisk = %q, want literal RED*GREEN", got)
	}
}

func TestLineBreaksBetweenSegments(t *testing.T) {
	in := "UNB+UNOA:3+A+B+250101:0000+1'\r\nUNH+1+X:1:1:IA'\r\nTIF+SMITH'\r\nUNT+3+1'\r\nUNZ+1+1'\r\n"
	ic, err := Parse([]byte(in), ParseOptions{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if ic.HasErrors() {
		t.Errorf("line-wrapped input must parse cleanly: %v", ic.Diagnostics)
	}
	tif, _ := ic.Messages[0].First("TIF")
	if got := tif.Value(0); got != "SMITH" {
		t.Errorf("TIF = %q; line breaks leaked into data", got)
	}
}

func TestUnterminatedSegmentIsReportedNotDropped(t *testing.T) {
	in := "UNB+UNOA:3+A+B+250101:0000+1'UNH+1+X:1:1:IA'TIF+SMITH"
	ic, err := Parse([]byte(in), ParseOptions{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !hasCode(ic, "unterminated_segment") {
		t.Errorf("expected unterminated_segment, got %v", ic.Diagnostics)
	}
	if _, ok := ic.Messages[0].First("TIF"); !ok {
		t.Error("the partial segment must be retained, not discarded")
	}
}

func TestOrphanTrailers(t *testing.T) {
	ic, _ := Parse([]byte("UNB+UNOA:3+A+B+250101:0000+1'UNT+2+1'UNZ+1+1'"), ParseOptions{})
	if !hasCode(ic, "orphan_unt") {
		t.Errorf("expected orphan_unt, got %v", ic.Diagnostics)
	}
	ic2, _ := Parse([]byte("UNB+UNOA:3+A+B+250101:0000+1'UNH+1+X:1:1:IA'UNZ+1+1'"), ParseOptions{})
	if !hasCode(ic2, "missing_unt") {
		t.Errorf("expected missing_unt, got %v", ic2.Diagnostics)
	}
}

func TestSegmentOutsideMessage(t *testing.T) {
	ic, _ := Parse([]byte("UNB+UNOA:3+A+B+250101:0000+1'TIF+X'UNZ+0+1'"), ParseOptions{})
	if !hasCode(ic, "segment_outside_message") {
		t.Errorf("expected segment_outside_message, got %v", ic.Diagnostics)
	}
}

func TestFunctionalGroups(t *testing.T) {
	in := "UNB+UNOA:3+A+B+250101:0000+1'" +
		"UNG+PAORES+A+B+250101:0000+1+IA+96:1'" +
		"UNH+1+PAORES:96:1:IA'TIF+X'UNT+3+1'" +
		"UNH+2+PAORES:96:1:IA'TIF+Y'UNT+3+2'" +
		"UNE+2+1'UNZ+1+1'"
	ic, err := Parse([]byte(in), ParseOptions{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if ic.HasErrors() {
		t.Fatalf("diagnostics: %v", ic.Diagnostics)
	}
	if len(ic.Groups) != 1 {
		t.Fatalf("Groups = %d, want 1", len(ic.Groups))
	}
	if len(ic.Groups[0].Messages) != 2 {
		t.Errorf("group messages = %d, want 2", len(ic.Groups[0].Messages))
	}
	if len(ic.Messages) != 2 {
		t.Errorf("flat Messages = %d, want 2", len(ic.Messages))
	}
}

func TestCharsetViolationWarns(t *testing.T) {
	// UNOA has no lowercase.
	in := "UNB+UNOA:3+A+B+250101:0000+1'UNH+1+X:1:1:IA'FTX+hello'UNT+3+1'UNZ+1+1'"
	ic, _ := Parse([]byte(in), ParseOptions{})
	if !hasCode(ic, "charset_violation") {
		t.Errorf("expected charset_violation, got %v", ic.Diagnostics)
	}
	// It must be a warning, not an error: the message is still usable.
	if ic.HasErrors() {
		t.Errorf("charset deviation must not be fatal: %v", ic.Diagnostics)
	}
	// Under UNOB it is legal.
	in2 := strings.Replace(in, "UNOA", "UNOB", 1)
	ic2, _ := Parse([]byte(in2), ParseOptions{})
	if hasCode(ic2, "charset_violation") {
		t.Errorf("UNOB must accept lowercase: %v", ic2.Diagnostics)
	}
}

func TestBuildAndFinalize(t *testing.T) {
	ic := NewInterchange(UNBParams{
		CharsetID: "UNOA", SyntaxVersion: 3,
		Sender:    Party{ID: "BA", Qualifier: "ZZ"},
		Recipient: Party{ID: "1A", Qualifier: "ZZ"},
		Date:      "250815", Time: "1430", ControlRef: "42",
	})
	ic.AddMessage("1", MessageID{Type: "PAORES", Version: "96", Release: "1", ControllingAgency: "IA"},
		Seg("MSG", Comp("", "22")),
		Seg("RCI", Comp("BA", "ABC123")),
	)
	ic.Finalize()

	out, err := ic.Encode(EncodeOptions{})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	// Re-parsing our own output must produce no diagnostics at all. This is the
	// property that keeps us from shipping miscounted trailers to a partner.
	back, err := Parse(out, ParseOptions{Strict: true})
	if err != nil {
		t.Fatalf("re-parse of generated interchange: %v\noutput: %s", err, out)
	}
	if len(back.Diagnostics) != 0 {
		t.Errorf("generated interchange produced diagnostics: %v\n%s", back.Diagnostics, out)
	}
	if got := back.Messages[0].Trailer.Value(0); got != "4" {
		t.Errorf("UNT count = %q, want 4", got)
	}
}

func TestSegmentLimit(t *testing.T) {
	var b strings.Builder
	b.WriteString("UNB+UNOA:3+A+B+250101:0000+1'")
	for i := 0; i < 50; i++ {
		b.WriteString("TIF+X'")
	}
	segs, _, diags, err := Scan([]byte(b.String()), ScanOptions{MaxSegments: 10})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(segs) != 10 {
		t.Errorf("segments = %d, want 10", len(segs))
	}
	found := false
	for _, d := range diags {
		if d.Code == "segment_limit" {
			found = true
		}
	}
	if !found {
		t.Error("expected a segment_limit diagnostic")
	}
}

func TestScanNoSegments(t *testing.T) {
	if _, _, _, err := Scan([]byte("no terminators here"), ScanOptions{}); err == nil {
		t.Error("expected an error for input with no segment structure")
	}
}

func TestSyntaxValidateRejectsDuplicates(t *testing.T) {
	s := DefaultSyntax(3)
	s.ElementSep = ':'
	if err := s.Validate(); err == nil {
		t.Error("expected duplicate service characters to be rejected")
	}
}

// FuzzRoundTrip asserts the property a gateway depends on: anything we can
// decode, we can re-encode and decode again to the same structure. Drift here
// means replayed or relayed traffic silently changes shape.
func FuzzRoundTrip(f *testing.F) {
	f.Add(samplePAORES)
	f.Add("UNB+UNOA:3+A+B+250101:0000+1'UNH+1+X:1:1:IA'FTX+A?+B'UNT+3+1'UNZ+1+1'")
	f.Add("UNA:+.? 'UNB+UNOA:4+A+B+250101:0000+1'UNH+1+X:1:1:IA'FTX+A*B'UNT+3+1'UNZ+1+1'")
	f.Fuzz(func(t *testing.T, in string) {
		ic, err := Parse([]byte(in), ParseOptions{SkipCharsetCheck: true})
		if err != nil {
			return
		}
		out, err := ic.Encode(EncodeOptions{})
		if err != nil {
			return
		}
		ic2, err := Parse(out, ParseOptions{SkipCharsetCheck: true})
		if err != nil {
			t.Fatalf("re-parse of encoded output failed: %v\nin=%q\nout=%q", err, in, out)
		}
		out2, err := ic2.Encode(EncodeOptions{})
		if err != nil {
			t.Fatalf("re-encode failed: %v", err)
		}
		if string(out) != string(out2) {
			t.Fatalf("encoding is not idempotent:\nin=%q\nout1=%q\nout2=%q", in, out, out2)
		}
	})
}

func hasCode(ic *Interchange, code string) bool {
	for _, d := range ic.Diagnostics {
		if d.Code == code {
			return true
		}
	}
	return false
}

func TestSyntaxRejectsImplausibleServiceCharacters(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Syntax)
	}{
		{"alphanumeric component separator", func(s *Syntax) { s.ComponentSep = '0' }},
		{"letter element separator", func(s *Syntax) { s.ElementSep = 'A' }},
		{"space terminator", func(s *Syntax) { s.SegmentTerm = ' ' }},
		{"non-ascii release", func(s *Syntax) { s.ReleaseChar = 0xC3 }},
		{"bad decimal mark", func(s *Syntax) { s.DecimalMark = 'x' }},
	}
	for _, c := range cases {
		s := DefaultSyntax(3)
		c.mut(&s)
		if err := s.Validate(); err == nil {
			t.Errorf("%s: expected rejection", c.name)
		}
	}
}

// A UNA carrying corrupt service characters must fail cleanly rather than be
// accepted as an exotic syntax that shreds the rest of the interchange.
func TestCorruptUNARejected(t *testing.T) {
	if _, err := Parse([]byte("UNA0+01\xc3'UNB+UNOA:3+A+B+250101:0000+1'"), ParseOptions{}); err == nil {
		t.Error("expected a corrupt UNA to be rejected")
	}
}

// A UNB whose S001 is not a coherent syntax identifier must not drive a rescan.
// Inferring service characters from a malformed header makes decoding depend on
// its own output.
func TestIncoherentSyntaxIdentifierDoesNotDriveRescan(t *testing.T) {
	in := "UNB+:4:*?*'"
	ic, err := Parse([]byte(in), ParseOptions{SkipCharsetCheck: true})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if ic.Syntax.RepetitionEnabled {
		t.Error("repetition must stay disabled when S001 is not a syntax identifier")
	}
	out, _ := ic.Encode(EncodeOptions{})
	ic2, err := Parse(out, ParseOptions{SkipCharsetCheck: true})
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	out2, _ := ic2.Encode(EncodeOptions{})
	if string(out) != string(out2) {
		t.Errorf("not idempotent: %q vs %q", out, out2)
	}
}

// An interchange that arrived with an explicit UNA must keep it on re-encode.
// Whether the UNA can safely be dropped depends on how UNB decodes, which the
// service characters themselves determine; preserving it removes that
// circularity and keeps decoding a fixed point.
func TestExplicitUNAIsPreserved(t *testing.T) {
	in := "UNA:+.?*'UNB+UNOA:4+A+B+250101:0000+1'UNH+1+X:1:1:IA'UNT+2+1'UNZ+1+1'"
	ic, err := Parse([]byte(in), ParseOptions{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	out, err := ic.Encode(EncodeOptions{})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !strings.HasPrefix(string(out), "UNA") {
		t.Errorf("the UNA was dropped: %q", out)
	}
	if string(out) != in {
		t.Errorf("round trip:\n got %q\nwant %q", out, in)
	}
	// An interchange that never had one must not gain one.
	plain := "UNB+UNOA:3+A+B+250101:0000+1'UNH+1+X:1:1:IA'UNT+2+1'UNZ+1+1'"
	ic2, _ := Parse([]byte(plain), ParseOptions{})
	out2, _ := ic2.Encode(EncodeOptions{})
	if string(out2) != plain {
		t.Errorf("a UNA-less interchange changed:\n got %q\nwant %q", out2, plain)
	}
}
