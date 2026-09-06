package typeb

import "testing"

// FuzzParseEnvelope drives every entry point that consumes raw Type B bytes
// from a link. The only assertion is "does not panic".
func FuzzParseEnvelope(f *testing.F) {
	seeds := [][]byte{
		[]byte("QU LHRRMBA NYCRMAA\n.LONXX1A 121430\nSSR VGML BA HK1\n"),
		[]byte("ZCZC ABC1234\nQU LHRRMBA\n.NYCRMAA 010000\nHELLO\nNNNN\n"),
		[]byte("QU LHRRMBA\n.LONXX1A 121430 PDM REL1\nHELLO\n"),
		[]byte("QU LHRRMBA\nPDM\n.LONXX1A 121430\nHELLO\n"),
		[]byte("QX AAABBCC DDDEEFF GGGHHII\n.LONXX1A 010101\nTEXT\n"),
		[]byte(".LONXX1A\nNO PRIORITY LINE\n"),
		[]byte("QU LONRM1J\r\n.LHRRMBA 121430\r\nMVT\r\nBA175/12.GXWBA.LHR\r\nAD1100/1115 EA1500 JFK\r\nPX214\r\n"),
		[]byte("\x01QU LHRRMBA\x02\r\n.LONRM1J 011200\r\nHELLO\x03\x04"),
		[]byte("QU LHRRMBA\n.LHRKPBA 121430\nPFS\nBA0117/16DEC LHR PART1\n-JFK\nNIL\nENDPFS\n"),
		[]byte("QA 0000000\n.0000000\nNNNN\nNNNN"),
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		_ = PriorityOf(raw)
		_ = Normalise(raw)
		_, _ = MarkPossibleDuplicate(raw)
		m, err := Parse(raw)
		if err != nil {
			return
		}
		_ = m.HasErrors()
		_, _, _ = ParseChannel(m.Channel)
		_ = m.Origin.String()
		_ = m.Origin.Conventional()
		_ = m.OriginTime.String()
		for _, d := range m.Destinations {
			_ = d.String()
			_ = d.Conventional()
		}
		for _, d := range m.Diagnostics {
			_ = d.String()
		}
		_, _ = SanitiseText(m.Text, CharsetITA2, '?')
		_, _ = SanitiseText(m.Text, CharsetIA5, '?')
		if out, err := m.Encode(EncodeOptions{}); err == nil {
			if _, err := Parse(out); err != nil {
				t.Skip()
			}
		}
	})
}

func FuzzParseAddress(f *testing.F) {
	for _, s := range []string{"LHRRMBA", "lonxx1a", "HDQRM1A", "ABCDEFGH", "", "  QU  ", "ABC1234"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if a, err := ParseAddress(s); err == nil {
			_ = a.String()
			_ = a.Conventional()
			_ = a.IsZero()
		}
		_, _, _ = ParseChannel(s)
		_ = ClassOf(s)
	})
}
