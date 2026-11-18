package inventory

import (
	"context"
	"testing"

	"github.com/adamf/jetway/pkg/gateway"
	"github.com/adamf/jetway/pkg/pnr"
)

// A seat stays on sale in the carrier's own channel after it has closed
// in a dearer one. Ten seats, fares of 300, 200 and 100: EMSR-b protects
// three seats for Y and six for Y and M, so K is authorised four and M
// seven. With K sold out, M is still open. An M request over the
// carrier's own channel is worth 200 and sells. The same request through
// a system that costs the carrier 120 is worth 80, which does not cover
// M's fare: it is nested as K, and K is closed.
func TestAChannelsCostNestsTheRequestLower(t *testing.T) {
	capacity := func(carrier, flight, date, board string) (map[string]int, bool) {
		return map[string]int{"Y": 10}, true
	}
	inv := New("WN", capacity)
	fares := map[string]float64{"Y": 300, "M": 200, "K": 100}
	rm := &Controller{Capacity: capacity, Forecast: func(carrier, flightNum, wireDate, board, compartment string, seats int) []ClassDemand {
		return []ClassDemand{{Class: "Y", Fare: 300, Mean: 3}, {Class: "M", Fare: 200, Mean: 3}, {Class: "K", Fare: 100, Mean: 10}}
	}}
	rm.Value = func(p *pnr.PNR, asked []*pnr.Segment, peer *gateway.Peer) (float64, bool) {
		v := 0.0
		for _, s := range asked {
			v += fares[s.Class] * float64(s.Seats)
		}
		if peer != nil && peer.Name == "1G" {
			v -= 120 * float64(asked[0].Seats)
		}
		return v, true
	}
	inv.Levels = rm.Levels
	inv.Network = rm
	if l := rm.Levels("WN", "2554", "26NOV", "BNA", "Y"); len(l) != 3 || l[1].Authorized != 7 || l[2].Authorized != 4 {
		t.Fatalf("ladder %+v", l)
	}
	ask := func(class string, peer *gateway.Peer) string {
		s := seg("2554", class, "HN", 1)
		out, err := inv.Decide(context.Background(), &pnr.PNR{Segments: []pnr.Segment{s}}, peer)
		if err != nil {
			t.Fatal(err)
		}
		return out[s.Key()]
	}
	gds := &gateway.Peer{Name: "1G"}
	for i := 0; i < 4; i++ {
		if got := ask("K", nil); got != "KK" {
			t.Fatalf("K sell %d: %s", i, got)
		}
	}
	if got := ask("K", nil); got != "UC" {
		t.Fatalf("K is sold out: %s", got)
	}
	if got := ask("M", gds); got != "UC" {
		t.Errorf("M through a channel that costs more than the step to K is nested as K, which is closed: %s", got)
	}
	if got := ask("M", nil); got != "KK" {
		t.Errorf("M in the carrier's own channel sells: %s", got)
	}
	// Y is worth 180 through the system: that covers K's fare and not
	// M's, so it too is nested as K.
	if got := ask("Y", gds); got != "UC" {
		t.Errorf("Y at 300 less 120 does not cover M's 200: %s", got)
	}
	// A cheaper channel: 300 less 60 covers M, and M is open.
	rm.Value = func(p *pnr.PNR, asked []*pnr.Segment, peer *gateway.Peer) (float64, bool) {
		return fares[asked[0].Class] - 60, true
	}
	if got := ask("Y", gds); got != "KK" {
		t.Errorf("Y worth 240 nests as M, which is open: %s", got)
	}
}
