package ats

import "testing"

func FuzzParse(f *testing.F) {
	f.Add(faaFPL)
	f.Add(faaInternationalFPL)
	f.Add("(DEP-ABC123/A0254-NZAA2347-VTBS-DOF/091120)")
	f.Add("(DLA-ABC123-NZAA2345-VTBS-DOF/091120)")
	f.Add("(CNL-ABC123-NZAA2300-VTBS-DOF/091120)")
	f.Add("(ARR-CSA406-LHBP-LKPR-LKTB0940)")
	f.Add("(CHG-BAW117-EGLL1200-KJFK-DOF/251126-13/EGLL1300)")
	f.Add("(FPL-UAL1447-IS\n-B738/M-SDE2E3FGHIRWXY/LB1\n-KORD1200\n-N0450F350 DCT\n-KSFO0400 KOAK\n-PBN/A1B1 DOF/251126)")
	f.Fuzz(func(t *testing.T, text string) {
		_ = Looks(text)
		m, err := Parse(text)
		if err != nil {
			return
		}
		_ = m.OtherValue("DOF")
		if out, err := Build(m); err == nil {
			_, _ = Parse(out)
		}
	})
}
