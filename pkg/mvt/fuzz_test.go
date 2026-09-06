package mvt

import "testing"

func FuzzParse(f *testing.F) {
	f.Add("MVT\nSD200/21.PMDFG.CDG\nAD1100/1115 EA1500 FRA\nDL72/0015\nPX112\nSI DEICING")
	f.Add("MVT\nSD200/21.PMDFG.CDG\nAD211100/211115 EA1500 FRA\nDL72/0015\nPX112\nSI DEICING")
	f.Add("MVT\nSD200/22.PMDFG.FRA\nAA1515/1520\nFLD22")
	f.Add("MVT\nSD200/22.PMDFG.CDG\nED221125\nDL72/0025")
	f.Add("MVT\nSD200/22.PMDFG.CDG\nNI221150\nSI ENGINE TROUBLE")
	f.Add("COR MVT\nBA117/26.GBZHA.LHR\nAD1207/1219 EO1235 EA1530 JFK\nDL64/72/0015/0020 EDL11/0005\nDLA1A/2B\nPX214/3 RR1234\nDR7100")
	f.Add("DIV\nBA117/26.GBZHA.LHR\nAA1515/1520 FRA\nSI WEATHER")
	f.Fuzz(func(t *testing.T, text string) {
		_ = IsMovement(text)
		m, err := Parse(text)
		if err != nil {
			return
		}
		if out, err := m.Build(); err == nil {
			_, _ = Parse(out)
		}
	})
}
