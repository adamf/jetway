package acars

import "testing"

func FuzzParse(f *testing.F) {
	f.Add("OUT\nBA117/26.GBZHA.LHR\n1207 JFK\nFOB 45000")
	f.Add("OFF\nBA117/26.GBZHA.LHR\n1219")
	f.Add("ON\nBA117/26.GBZHA.JFK\n1530")
	f.Add("IN\nBA117/26.GBZHA.JFK\n1541 GATE B22")
	f.Add("MVT\nBA117/26.GBZHA.LHR\nAD1207/1219")
	f.Fuzz(func(t *testing.T, text string) {
		_ = IsOOOI(text)
		m, err := Parse(text)
		if err != nil {
			return
		}
		if out, err := Build(m); err == nil {
			_, _ = Parse(out)
		}
	})
}
