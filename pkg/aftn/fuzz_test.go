package aftn

import "testing"

func FuzzParse(f *testing.F) {
	f.Add([]byte(annexExample))
	f.Add([]byte("GG LGGGZRZX LGATKLMW\n201838 EGLLKLMW\n(FPL-UAL1447-IS\n-B738/M-SDE2E3FGHIRWXY/LB1\n-KORD1200\n-N0450F350 DCT\n-KSFO0400 KOAK\n-PBN/A1B1 DOF/251126)\nNNNN\n"))
	f.Add([]byte("ZCZC\nSS EGLLZPZX\n010000 EGLLBAWO\n-TITLE SAM\n-ARCID BAW117\nNNNN"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		_ = Looks(raw)
		m, err := Parse(raw)
		if err != nil {
			return
		}
		for _, o := range []EncodeOptions{{}, {CRLF: true}} {
			if out, err := m.Encode(o); err == nil {
				_, _ = Parse(out)
			}
		}
	})
}
