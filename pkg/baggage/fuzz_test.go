package baggage

import "testing"

func FuzzParse(f *testing.F) {
	f.Add(minimumBSM)
	f.Add(sampleBPM)
	f.Add("BSM\nDEL\n.V/1LLGW\n.F/U2123/01OCT/AMS/Y\n.N/0999000111001\nENDBSM")
	f.Add("BSM\n.V/1LLGW\n.F/BA0117/16DEC/JFK/Y\n.O/BA0175/16DEC/LAX/Y\n.N/0125123456001\n.P/SMITH/JOHN\n.L/ABC123\n.S/Y/12A/C/1\n.W/K/1/23\nENDBSM")
	f.Add("BUM\n.V/1LLHR\n.F/BA0117/16DEC/JFK\n.N/0125123456001\n.P/SMITH/JOHN\nENDBUM")
	f.Add("AHL JFKBA10231\n.NM/SMITH\n.IT/JOHN\n.CT/0125123456\n.FD/BA0117/16DEC\n.RT/LHRJFK\n.BI/1\n.TN/0125123456001\n.CL/22\n.TP/LHR\n.AG/JFKBA")
	f.Add("OHD LHRBA10231\n.NM/SMITH\n.TN/0125123456001\n.CT/BLK/SAMSONITE\n.FD/BA0117/16DEC\n.RT/LHRJFK\n.AG/LHRBA")
	f.Add("FWD LHRBA10231\n.TN/0125123456001\n.FD/BA0175/17DEC\n.RT/LHRJFK\n.AG/LHRBA")
	f.Fuzz(func(t *testing.T, text string) {
		_ = IsBaggage(text)
		_ = IsTracing(text)
		if m, err := Parse(text); err == nil {
			if out, err := Build(m); err == nil {
				_, _ = Parse(out)
			}
		}
		if tf, err := ParseTracing(text); err == nil {
			if out, err := BuildTracing(tf); err == nil {
				if tf2, err := ParseTracing(out); err == nil {
					_ = Match(tf, tf2)
				}
			}
		}
	})
}
