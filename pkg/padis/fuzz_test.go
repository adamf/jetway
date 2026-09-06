package padis

import (
	"strings"
	"testing"

	"github.com/adamf/jetway/pkg/edifact"
	"github.com/adamf/jetway/pkg/pnr"
)

// FuzzParseMessages runs every PADIS message reader the gateway dispatches to
// over whatever interchange the syntax layer accepts.
func FuzzParseMessages(f *testing.F) {
	f.Add([]byte(strings.Join(guideExample1, "")))
	f.Add([]byte("UNB+UNOA:3+BA:ZZ+1A:ZZ+260601:1200+1'UNH+1+PAOREQ:96:1:IA'MSG+:31'ORG+1A:LON'TVL+160126:0900:160126:1200+LHR+JFK+BA+117:Y'TIF+SMITH+JOHNMR:A:1'UNT+6+1'UNZ+1+1'"))
	f.Add([]byte("UNB+UNOA:3+BA:ZZ+1A:ZZ+260601:1200+1'UNH+1+PAORES:96:1:IA'MSG+:32'RCI+BA:ABC123'TVL+160126:0900:160126:1200+LHR+JFK+BA+117:Y'RPI++HK'UNT+6+1'UNZ+1+1'"))
	f.Add([]byte("UNB+UNOA:3+BA:ZZ+1A:ZZ+260601:1200+1'UNH+1+TKCREQ:03:1:IA'MSG+:131'ORG+BA:LON'TKT+1252100000001:T:2'UNT+5+1'UNZ+1+1'"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		ic, err := edifact.Parse(raw, edifact.ParseOptions{})
		if err != nil {
			return
		}
		for _, m := range ic.Messages {
			_ = MessageFunction(m)
			if IsPNRGOV(m) || IsACKRES(m) {
				if gp, err := ParsePNRGOV(m); err == nil {
					if out, err := BuildPNRGOV(gp, BuildOptions{}); err == nil {
						_, _ = out.Encode(edifact.EncodeOptions{})
					}
				}
			}
			if IsDivide(m) {
				_, _ = ParseDivide(m)
			}
			if IsTicketControl(m) {
				_, _ = ParseTicketControl(m)
			}
			// The record grammar: what a PAOREQ/PAORES does to a PNR.
			rec := &pnr.PNR{}
			_ = Apply(rec, m, ApplyOptions{Inbound: true, Party: "1A", Self: "BA"})
			rec.Recompute()
			_, _ = BuildPAOREQ(rec, "BA", BuildOptions{})
			_, _ = BuildCancel(rec, "BA", nil, BuildOptions{})
		}
	})
}
