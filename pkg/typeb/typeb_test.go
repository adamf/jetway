package typeb

import (
	"strings"
	"testing"
)

func TestParseCanonical(t *testing.T) {
	raw := "QU LHRRMBA NYCRMAA\r\n.LONXX1A 121430\r\n\r\nSSR VGML BA HK1 LHRJFK0175Y15JUN\r\n-1SMITH/JOHNMR\r\n"
	m, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m.Priority != "QU" {
		t.Errorf("Priority = %q, want QU", m.Priority)
	}
	if len(m.Destinations) != 2 {
		t.Fatalf("Destinations = %d, want 2", len(m.Destinations))
	}
	if got := m.Destinations[0].String(); got != "LHRRMBA" {
		t.Errorf("Destinations[0] = %q", got)
	}
	if m.Destinations[0].Carrier != "BA" || m.Destinations[0].Location != "LHR" || m.Destinations[0].Department != "RM" {
		t.Errorf("address decomposition wrong: %+v", m.Destinations[0])
	}
	if got := m.Origin.String(); got != "LONXX1A" {
		t.Errorf("Origin = %q, want LONXX1A", got)
	}
	if m.Origin.Carrier != "1A" {
		t.Errorf("Origin.Carrier = %q, want 1A (alphanumeric designators must parse)", m.Origin.Carrier)
	}
	ot := m.OriginTime
	if !ot.Present || ot.Day != 12 || ot.Hour != 14 || ot.Minute != 30 {
		t.Errorf("OriginTime = %+v", ot)
	}
	want := "SSR VGML BA HK1 LHRJFK0175Y15JUN\n-1SMITH/JOHNMR"
	if m.Text != want {
		t.Errorf("Text = %q, want %q", m.Text, want)
	}
	if m.HasErrors() {
		t.Errorf("unexpected errors: %v", m.Diagnostics)
	}
}

func TestParseNetworkFraming(t *testing.T) {
	raw := "\x01ZCZC ABC1234\nQU LHRRMBA\n.NYCRMAA 010000\nHELLO\nNNNN\n\x03"
	m, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !m.Framed {
		t.Error("expected Framed")
	}
	if m.Channel != "ABC1234" {
		t.Errorf("Channel = %q", m.Channel)
	}
	if m.Text != "HELLO" {
		t.Errorf("Text = %q, want HELLO (framing and control chars must be stripped)", m.Text)
	}
}

func TestParseMultipleAddressLines(t *testing.T) {
	raw := "QU LHRRMBA NYCRMAA FRARMLH\nMUCRMLH SYDRMQF\n.LONXX1A 121430\nTEXT\n"
	m, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(m.Destinations) != 5 {
		t.Fatalf("Destinations = %d, want 5: %v", len(m.Destinations), m.Destinations)
	}
	if m.Destinations[4].String() != "SYDRMQF" {
		t.Errorf("last destination = %q", m.Destinations[4])
	}
}

// A continuation line that is not made entirely of addresses must be treated as
// text, not silently swallowed as routing.
func TestParseAmbiguousContinuationIsText(t *testing.T) {
	raw := "QU LHRRMBA\nSSR VGML BA HK1\n.LONXX1A 121430\nTEXT\n"
	m, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(m.Destinations) != 1 {
		t.Fatalf("Destinations = %d, want 1", len(m.Destinations))
	}
	if !strings.HasPrefix(m.Text, "SSR VGML") {
		t.Errorf("Text = %q, want the SSR line retained as text", m.Text)
	}
}

func TestParseMissingOriginTimeDiagnoses(t *testing.T) {
	m, err := Parse([]byte("QU LHRRMBA\n.LONXX1A\nTEXT\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !hasCode(m, "missing_origin_time") {
		t.Errorf("expected missing_origin_time diagnostic, got %v", m.Diagnostics)
	}
	if m.HasErrors() {
		t.Errorf("a missing time group should warn, not error: %v", m.Diagnostics)
	}
}

func TestParseBadAddressIsRecovered(t *testing.T) {
	m, err := Parse([]byte("QU LHRRMBA BAD! NYCRMAA\n.LONXX1A 121430\nTEXT\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(m.Destinations) != 2 {
		t.Errorf("want the two good addresses kept, got %v", m.Destinations)
	}
	if !hasCode(m, "bad_address") {
		t.Errorf("expected bad_address diagnostic, got %v", m.Diagnostics)
	}
}

func TestParseRawPreserved(t *testing.T) {
	raw := []byte("QU LHRRMBA\r\n.LONXX1A 121430\r\nTEXT\r\n")
	m, _ := Parse(raw)
	if string(m.Raw) != string(raw) {
		t.Error("Raw must hold the exact input bytes for replay")
	}
	raw[0] = 'X'
	if m.Raw[0] == 'X' {
		t.Error("Raw must be a defensive copy")
	}
}

func TestParseEmpty(t *testing.T) {
	if _, err := Parse([]byte("  \r\n \n")); err != ErrEmpty {
		t.Errorf("err = %v, want ErrEmpty", err)
	}
}

func TestEncodeRoundTrip(t *testing.T) {
	in := "QU LHRRMBA NYCRMAA\n.LONXX1A 121430\nSSR VGML BA HK1 LHRJFK0175Y15JUN\n"
	m, err := Parse([]byte(in))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	out, err := m.Encode(EncodeOptions{Charset: CharsetITA2})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if string(out) != in {
		t.Errorf("round trip:\n got %q\nwant %q", out, in)
	}
}

func TestEncodeWrapsAddressLines(t *testing.T) {
	m := &Message{Priority: "QU", Origin: mustAddr(t, "LONXX1A"), OriginTime: OriginTime{12, 14, 30, true}, Text: "X"}
	for _, a := range []string{"LHRRMBA", "NYCRMAA", "FRARMLH", "MUCRMLH", "SYDRMQF", "HKGRMCX"} {
		m.Destinations = append(m.Destinations, mustAddr(t, a))
	}
	out, err := m.Encode(EncodeOptions{MaxLineLength: 32})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	for _, l := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if len(l) > 32 {
			t.Errorf("line exceeds limit (%d): %q", len(l), l)
		}
	}
	// Wrapping must not split or drop an address.
	reparsed, err := Parse(out)
	if err != nil {
		t.Fatalf("reparse: %v", err)
	}
	if len(reparsed.Destinations) != 6 {
		t.Errorf("after wrap+reparse got %d destinations, want 6: %v", len(reparsed.Destinations), reparsed.Destinations)
	}
}

func TestEncodeRejectsOverlongTextLine(t *testing.T) {
	m := &Message{
		Priority: "QU", Destinations: []Address{mustAddr(t, "LHRRMBA")},
		Origin: mustAddr(t, "LONXX1A"), Text: strings.Repeat("A", 100),
	}
	if _, err := m.Encode(EncodeOptions{MaxLineLength: 64}); err == nil {
		t.Error("expected an error for an overlong text line")
	}
}

func TestCharsetRejectsLowercase(t *testing.T) {
	if CharsetITA2.Valid("hello") {
		t.Error("ITA2 must reject lowercase")
	}
	if !CharsetITA2.Valid("HELLO/WORLD-1(2)") {
		t.Error("ITA2 must accept conventional punctuation")
	}
	if CharsetITA2.Valid("A*B") {
		t.Error("ITA2 must reject *")
	}
	if !CharsetIA5.Valid("A*B") {
		t.Error("IA5 must accept *")
	}
}

func TestSanitiseText(t *testing.T) {
	got, n := SanitiseText("smith/johné", CharsetITA2, '?')
	if got != "SMITH/JOHN?" || n != 1 {
		t.Errorf("SanitiseText = %q, %d", got, n)
	}
}

func TestNormaliseCollapsesLineEndings(t *testing.T) {
	if got := string(Normalise([]byte("A\r\nB\rC\nD"))); got != "A\nB\nC\nD" {
		t.Errorf("Normalise = %q", got)
	}
}

func hasCode(m *Message, code string) bool {
	for _, d := range m.Diagnostics {
		if d.Code == code {
			return true
		}
	}
	return false
}

func mustAddr(t *testing.T, s string) Address {
	t.Helper()
	a, err := ParseAddress(s)
	if err != nil {
		t.Fatalf("ParseAddress(%q): %v", s, err)
	}
	return a
}

// IATA's Type B Messaging whitepaper gives the format limit as 60 lines of 63
// characters. Truncating an over-long message would silently change what a
// partner receives, so the encoder refuses instead.
func TestEncodeEnforcesStandardLimits(t *testing.T) {
	if DefaultLineLength != 63 {
		t.Errorf("DefaultLineLength = %d, want 63", DefaultLineLength)
	}
	if DefaultMaxLines != 60 {
		t.Errorf("DefaultMaxLines = %d, want 60", DefaultMaxLines)
	}
	base := &Message{
		Priority: "QU", Destinations: []Address{mustAddr(t, "LHRRMBA")},
		Origin: mustAddr(t, "LONXX1A"), OriginTime: OriginTime{12, 14, 30, true},
	}

	m := *base
	m.Text = strings.Repeat("A", 64)
	if _, err := m.Encode(EncodeOptions{}); err == nil {
		t.Error("a 64-character line exceeds the 63-character limit and must be refused")
	}
	m.Text = strings.Repeat("A", 63)
	if _, err := m.Encode(EncodeOptions{}); err != nil {
		t.Errorf("a 63-character line is legal: %v", err)
	}

	lines := make([]string, 61)
	for i := range lines {
		lines[i] = "X"
	}
	m = *base
	m.Text = strings.Join(lines, "\n")
	if _, err := m.Encode(EncodeOptions{}); err == nil {
		t.Error("61 lines exceeds the 60-line limit and must be refused")
	}
	m.Text = strings.Join(lines[:60], "\n")
	if _, err := m.Encode(EncodeOptions{}); err != nil {
		t.Errorf("60 lines is legal: %v", err)
	}
	// The limit is a default, not a hard rule: some links differ.
	m.Text = strings.Join(lines, "\n")
	if _, err := m.Encode(EncodeOptions{MaxLines: -1}); err != nil {
		t.Errorf("MaxLines:-1 must disable the check: %v", err)
	}
}
