package gateway

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/adamf/jetway/pkg/store"
)

// fuzzGateway is a gateway with an in-memory store and one Type B peer, the
// shape every network listener hands messages to.
func fuzzGateway() (*Gateway, *Peer) {
	st := store.NewMem()
	st.MaxMessages = 256
	g := New(Identity{Designator: "1J", TTYAddress: "LONRM1J", Name: "fuzz", AFTNAddress: "EGLLZPZX"},
		st, NewBus(16), slog.New(slog.NewTextHandler(io.Discard, nil)), []byte("fuzz-secret"))
	peer := &Peer{Name: "BA", Carrier: "BA", Format: store.FormatTypeB, TTYAddress: "LHRRMBA"}
	g.AddPeer(peer)
	return g, peer
}

var gatewaySeeds = []string{
	"QU LONRM1J\n.LHRRMBA 121430\nSS\nBA0117Y16DECLHRJFKNN1\n1SMITH/JOHNMR\nRL BA/AB1234\n",
	"QU LONRM1J\r\n.LHRRMBA 121430\r\nMVT\r\nBA175/12.GXWBA.LHR\r\nAD1100/1115 EA1500 JFK\r\nPX214\r\n",
	"QU LONRM1J\n.LHRRMBA 121430\nASM\nUTC\nCNL\nBA0117/16DEC\n",
	"QU LONRM1J\n.LHRRMBA 121430\nAVS\nBA0175/27SEP/LHRJFK\nY/O J/C M/L\n",
	"QU LHRKPBA LHRRSBA\n.LHRRMBA 121430\nPNL\nBA0117/16DEC LHR PART1\n-JFK001Y\n1SMITH/JOHNMR .L/ABC123\nENDPNL\n",
	"QU LHRRMBA\n.LHRKPBA 121430\nPFS\nBA0117/16DEC LHR PART1\n-JFK\nNIL\nENDPFS\n",
	"QU LONRM1J\n.LHRRMBA 121430\nBSM\n.V/1LLGW\n.F/BA0117/16DEC/JFK/Y\n.N/0125123456001\nENDBSM\n",
	"QU LONRM1J\n.LHRRMBA 121430\nOUT\nBA117/26.GBZHA.LHR\n1207 JFK\n",
	"QU LONRM1J\n.LHRRMBA 121430\nLDM\nBA0117/16.GBZHA.Y180.2/6\n-JFK.150/0/0.T2850.1/1200.2/1650.PAX/150.PAD/0\nSI NIL\n",
	"UNB+UNOA:3+BA:ZZ+1J:ZZ+260601:1200+1'UNH+1+PAOREQ:96:1:IA'MSG+:31'ORG+BA:LON'TVL+160126:0900:160126:1200+LHR+JFK+BA+117:Y'TIF+SMITH+JOHNMR:A:1'UNT+6+1'UNZ+1+1'",
	"UNB+UNOA:3+AA:ZZ+1J:ZZ+260829:1200+IC0001'UNH+1+CONTRL:D:3:UN'UCI+IC0001+AA:ZZ+1J:ZZ+7'UNT+3+1'UNZ+1+IC0001'",
	"UNB+UNOA:4+ZZAIRLINE+CUSTOMS+130620:0900+000000001'UNH+1+PAXLST:02:1:UN:IATA'BGM+745'UNT+3+1'UNZ+1+000000001'",
	"ZCZC LPA183\nGG LGGGZRZX LGATKLMW\n201838 EGLLKLMW\n(ARR-KLM123-EGLL-LGAT1830)\nNNNN\n",
	"GG EGLLZPZX\n010000 EGLLBAWO\n-TITLE SAM\n-ARCID BAW117\n-ADEP EGLL\n-ADES KJFK\n-EOBD 160224\n-EOBT 0950\n-CTOT 1030\n",
}

// FuzzDecode drives the classifier and every parser it dispatches to, which
// is exactly what a frame from any listener reaches.
func FuzzDecode(f *testing.F) {
	for _, s := range gatewaySeeds {
		f.Add([]byte(s))
	}
	g, peer := fuzzGateway()
	f.Fuzz(func(t *testing.T, raw []byte) {
		for _, opts := range []IngestOptions{{}, {FromFile: true}} {
			_, _ = g.decode(peer, &store.Message{Raw: raw}, opts)
		}
	})
}

// FuzzIngest runs the whole inbound pipeline: capture, decode, apply, reply.
func FuzzIngest(f *testing.F) {
	for _, s := range gatewaySeeds {
		f.Add([]byte(s))
	}
	g, _ := fuzzGateway()
	f.Fuzz(func(t *testing.T, raw []byte) {
		_, _ = g.IngestWith(context.Background(), "BA", raw, IngestOptions{HoldReply: true})
	})
}
