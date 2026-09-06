package atfm

import "testing"

func FuzzParse(f *testing.F) {
	f.Add("-TITLE SAM\n-ARCID AMC101\n-IFPLID AA12345678\n-ADEP EGLL\n-ADES LMML\n-EOBD 160224\n-EOBT 0950\n-CTOT 1030\n-REGUL RMZ24M\n-TTO -PTID VEULE -TO 1050 -FL F300\n-TAXITIME 0015\n-REGCAUSE CE 81\n")
	f.Add("-TITLE SRM\n-ARCID AMC101\n-IFPLID AA12345678\n-ADEP EGLL\n-ADES LMML\n-EOBD 160224\n-EOBT 0950\n-NEWCTOT 1020\n-REGUL RMZ24M\n-TTO -PTID VEULE -TO 1025 -FL F300\n-TAXITIME 0015\n-REGCAUSE CE 81\n")
	f.Add("-TITLE SLC\n-ARCID AMC101\n-ADEP EGLL\n-ADES LMML\n-EOBD 160224\n-EOBT 0950\n-REASON FLIGHT PLAN CANCELLED\n")
	f.Add("-TITLE FLS\n-ARCID AMC101\n-ADEP EGLL\n-ADES LMML\n-EOBD 160224\n-EOBT 0950\n-COMMENT NO SLOT\n")
	f.Fuzz(func(t *testing.T, text string) {
		_ = Looks(text)
		_, _ = ParseCause(text)
		m, err := Parse(text)
		if err != nil {
			return
		}
		_ = m.OtherValue("TTO")
		if out, err := Build(m); err == nil {
			_, _ = Parse(out)
		}
	})
}
