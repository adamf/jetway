package pnl

import "testing"

func FuzzParse(f *testing.F) {
	f.Add(samplePNL)
	f.Add(sampleADL)
	f.Add("PNL\nBA0117/16DEC LHR PART1\n-JFK02Y\n1SMITH/JOHNMR .L/AB12CD\n1JONES/AMYMS\nENDPNL")
	f.Add("PNL\nBA0117/16DEC LHR PART1\n-JFK001Y\n1SMITH/JOHNMR .L/ABC123\n.R/VGML HK1\n.O/BA0175Y17DECJFKLAX\nENDPART1")
	f.Add("ADL\nBA0117/16DEC LHR PART1\n-JFK004Y\nADD\n2SMITH/JOHNMR/JANEMRS .L/ABC123\nDEL\n1JONES/AMYMS\nCHG\n1BROWN/BOBMR\nENDADL")
	f.Fuzz(func(t *testing.T, text string) {
		_ = IsNameList(text)
		_, _ = ParseName(text)
		m, err := Parse(text)
		if err != nil {
			return
		}
		if out, err := Build(m); err == nil {
			_, _ = Parse(out)
		}
		for _, g := range m.Groups {
			for _, n := range g.Names {
				_ = NameLine(n)
				_ = NameLines(n)
			}
		}
		_, _ = BuildParts(m.Kind, m.Flight, m.Date, m.Board, m.Groups)
	})
}
