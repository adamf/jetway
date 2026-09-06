package airimp

import (
	"testing"

	"github.com/adamf/jetway/pkg/pnr"
)

// FuzzParseApply runs the AIRIMP reservation grammar and the record mapping
// that the gateway applies to a PNR for every inbound Type B booking message.
func FuzzParseApply(f *testing.F) {
	f.Add("SS\nBA0117Y16DECLHRJFKNN1\n1SMITH/JOHNMR\nRL BA/AB1234\nSSR VGML BA NN1\nOSI BA CTCM 447700900123\nTK OK16DEC/LON1A")
	f.Add("HK\nBA0117Y16DECLHRJFKHK1\n1SMITH/JOHNMR\n.L/ABC123\nRL 1A/XYZ789")
	f.Add("SSR VGML BA HK1 LHRJFK0117Y16DEC-1SMITH/JOHNMR\nSSR DOCS BA HK1/P/GBR/123456789/GBR/12JUL64/M/23OCT27/SMITH/JOHN-1SMITH/JOHNMR")
	f.Add("XX\nBA0117Y16DECLHRJFKXX1\n1SMITH/JOHNMR\nRL BA/AB1234")
	f.Add("AP LON 0207 000 0000\nRF AGENT\nRM FREE TEXT\nTK TL15DEC/LON1A\n-2SMITH/JOHNMR/JANEMRS\nOSI BA CTCE JOHN//SMITH.COM")
	f.Add("QU LHRRMBA\n.LONRM1J 010900\nSS\nBA0117Y16DECLHRJFKNN1\n1SMITH/JOHN1MR\nRL BA/AB0001\n")
	f.Fuzz(func(t *testing.T, text string) {
		m := Parse(text)
		_ = m.Intent()
		_ = m.Segments()
		_ = m.Names()
		_ = m.SSRs()
		_ = m.Locators()
		_ = m.Unknowns()
		rec := &pnr.PNR{}
		_ = Apply(rec, m, ApplyOptions{})
		rec.Recompute()
		_ = BuildSell(rec, "BA", ActionCode("NN"))
		_ = BuildReply(m, map[string]ActionCode{}, rec, "BA")
		_ = BuildCancel(rec, "BA", nil)
		for _, s := range rec.Segments {
			_ = s
		}
	})
}
