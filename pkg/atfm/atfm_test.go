package atfm

import (
	"strings"
	"testing"
)

// The manual's Annex B examples, verbatim: SAM (1), SRM (1), SLC (1) and
// (2), FLS (1), DES (1). Each parses to its fields and builds back to the
// same text, TTO's nested fields intact.
func TestManualExamplesParseAndRoundTrip(t *testing.T) {
	sam := "-TITLE SAM\n-ARCID AMC101\n-IFPLID AA12345678\n-ADEP EGLL\n-ADES LMML\n-EOBD 160224\n-EOBT 0950\n-CTOT 1030\n-REGUL RMZ24M\n-TTO -PTID VEULE -TO 1050 -FL F300\n-TAXITIME 0020\n-REGCAUSE CE 81"
	m, err := Parse(sam)
	if err != nil {
		t.Fatal(err)
	}
	if m.Title != TitleSAM || m.ARCID != "AMC101" || m.IFPLID != "AA12345678" || m.ADEP != "EGLL" || m.ADES != "LMML" ||
		m.EOBD != "160224" || m.EOBT != "0950" || m.CTOT != "1030" || len(m.REGUL) != 1 || m.REGUL[0] != "RMZ24M" ||
		m.TAXITIME != "0020" || m.REGCAUSE == nil || m.REGCAUSE.Code != 'C' || m.REGCAUSE.Location != 'E' || m.REGCAUSE.IATA != "81" ||
		m.OtherValue("TTO") != "-PTID VEULE -TO 1050 -FL F300" {
		t.Errorf("SAM parsed as %+v (cause %v)", m, m.REGCAUSE)
	}
	out, err := Build(m)
	if err != nil || out != sam {
		t.Errorf("SAM rebuilt as\n%s\nwant\n%s\n%v", out, sam, err)
	}
	srm := "-TITLE SRM\n-ARCID AMC101\n-IFPLID AA12345678\n-ADEP EGLL\n-ADES LMML\n-EOBD 160224\n-EOBT 0950\n-NEWCTOT 1020\n-REGUL RMZ24M\n-TTO -PTID VEULE -TO 1025 -FL F300\n-TAXITIME 0020\n-REGCAUSE CE 81"
	if m, err := Parse(srm); err != nil || m.Title != TitleSRM || m.NEWCTOT != "1020" || m.CTOT != "" {
		t.Errorf("SRM: %+v %v", m, err)
	}
	slc := "-TITLE SLC\n-ARCID AMC101\n-IFPLID AA12345678\n-ADEP EGLL\n-ADES LMML\n-EOBD 080901\n-EOBT 0945\n-REASON OUTREG\n-TAXITIME 0020"
	m, err = Parse(slc)
	if err != nil || m.Title != TitleSLC || m.REASON != "OUTREG" || m.REGCAUSE != nil {
		t.Errorf("SLC: %+v %v", m, err)
	}
	if out, _ := Build(m); out != slc {
		t.Errorf("SLC rebuilt as\n%s", out)
	}
	slc2 := "-TITLE SLC\n-ARCID AMC101\n-IFPLID AA12345678\n-ADEP EGLL\n-ADES LMML\n-EOBD 080901\n-EOBT 0945\n-REASON VOID\n-COMMENT FLIGHT CANCELLED\n-TAXITIME 0020"
	if m, err := Parse(slc2); err != nil || m.REASON != "VOID" || m.COMMENT != "FLIGHT CANCELLED" {
		t.Errorf("SLC (2): %+v %v", m, err)
	}
	// The manual wraps the FLS comment over three lines; the value is whole.
	fls := "-TITLE FLS\n-ARCID AMC101\n-IFPLID AA12345678\n-ADEP EGLL\n-ADES LMML\n-EOBD 080901\n-EOBT 0945\n-REGUL LMMLA01\n-COMMENT AERODROME OR\nAIRSPACE OR POINT NOT\nAVAILABLE\n-TAXITIME 0020\n-REGCAUSE AA 83"
	m, err = Parse(fls)
	if err != nil || m.Title != TitleFLS || m.COMMENT != "AERODROME OR AIRSPACE OR POINT NOT AVAILABLE" || m.REGCAUSE.String() != "AA 83" {
		t.Errorf("FLS: %+v %v", m, err)
	}
	des := "-TITLE DES\n-ARCID AMC101\n-IFPLID AA12345678\n-ADEP EGLL\n-ADES LMML\n-EOBD 080901\n-EOBT 0945\n-TAXITIME 0020"
	if m, err := Parse(des); err != nil || m.Title != TitleDES || m.TAXITIME != "0020" {
		t.Errorf("DES: %+v %v", m, err)
	}
	// Fields run together on one line parse the same.
	if m, err := Parse("-TITLE REA -ARCID BAW117 -IFPLID AA00000001 -ADEP EGLL -ADES KJFK -EOBD 261126 -EOBT 0800 -MINLINEUP 0010"); err != nil || m.Title != TitleREA || m.OtherValue("MINLINEUP") != "0010" {
		t.Errorf("one-line REA: %+v %v", m, err)
	}
	if Looks(sam) != true || Looks("(DEP-BAW117-EGLL0800-KJFK)") {
		t.Error("Looks")
	}
	if _, err := Parse("-ARCID X"); err == nil || !strings.Contains(err.Error(), "TITLE") {
		t.Error("a message without a title is refused")
	}
}

// Annex D's correlation: weather at the arrival aerodrome is 84, ATC
// capacity en route 81, any cause at the departure aerodrome 89 save the
// aerodrome services (99) and non-ATC industrial action (98).
func TestRegulationCausesFollowAnnexD(t *testing.T) {
	cases := []struct {
		code, loc byte
		want      string
	}{
		{'W', 'A', "84"}, {'W', 'E', "81"}, {'W', 'D', "89"}, {'C', 'E', "81"}, {'C', 'A', "83"}, {'C', 'D', "89"},
		{'S', 'E', "82"}, {'T', 'A', "83"}, {'E', 'D', "99"}, {'N', 'A', "98"}, {'G', 'A', "83"}, {'M', 'E', "82"}, {'P', 'D', "89"},
	}
	for _, c := range cases {
		if got := NewCause(c.code, c.loc); got.IATA != c.want || got.String() != string([]byte{c.code, c.loc})+" "+c.want {
			t.Errorf("%c%c -> %s, want %s", c.code, c.loc, got.String(), c.want)
		}
	}
	if CauseName('W') != "weather" || CauseName('G') != "aerodrome capacity" {
		t.Error("cause names")
	}
	if _, err := ParseCause("XX"); err == nil {
		t.Error("a bare pair is not a cause")
	}
}
