package dcs

import "testing"

// FuzzParseMessages runs every departure-control text reader the gateway
// dispatches to, both through the classifier and directly.
func FuzzParseMessages(f *testing.F) {
	f.Add(psmORYtoGVA)
	f.Add("PSM\nFR5416/02OCT OPO PART1\n-SXB 1PAX/1SSR\nWCHR 001Y\nY CLASS 1PAX/1SSR\n1DEMAND006835/PAX1MR 12A\n WCHR\nENDPSM")
	f.Add("PSM\nCX123/03DEC HKG PART1\n-NRT NIL\n-YVR NIL\nSI\nREDUCED MEAL SERVICE\nENDPSM")
	f.Add("PFS\nBA0117/16DEC LHR PART1\n-JFK\nNIL\nENDPFS")
	f.Add("PFS\nBA0117/16DEC LHR PART1\n-JFK\nGOSH\n1SMITH/JOHNMR Y\nNOSH\n1JONES/AMYMS J .L/ABC123\nENDPFS")
	f.Add("PTM\nBA0117/16DEC LHR PART1\nBA0175/16DEC LHR 1Y 2B SMITH/JOHNMR\nENDPTM")
	f.Add("ETL\nBA0117/16DEC LHR PART1\n-JFK 150Y\nENDETL")
	f.Add("LDM\nBA0117/16.GBZHA.Y180.2/6\n-JFK.150/0/0.T2850.1/1200.2/1650.PAX/150.PAD/0\nSI NIL")
	f.Add("LDM\nVY5172/04.ECHQI.A320P.2/05\n-AMS.153/1/2.T1794.3/624.4/1170.PAX/154.PRF/0.DHC/0.B138/1794\nSI NIL")
	f.Add("CPM\nBA0117/16.GBZHA.C48Y312\n-11L/AKE12345BA/JFK/850/B\n-11R/AKE12346BA/JFK/850/B\n-BULK/JFK/120/B\nSI NIL")
	f.Fuzz(func(t *testing.T, text string) {
		_ = IsDepartureControl(text)
		_, _ = Parse(text)
		_, _ = ParsePFS(text)
		_, _ = ParsePTM(text)
		_, _ = ParsePSM(text)
		_, _ = ParseETL(text)
		_, _ = ParseLDM(text)
		_, _ = ParseCPM(text)
	})
}
