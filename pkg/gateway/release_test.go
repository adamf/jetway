package gateway

import (
	"context"
	"testing"

	"github.com/adamf/jetway/pkg/pnr"
)

type fakeReleaser struct {
	got []string
}

func (f *fakeReleaser) Decide(ctx context.Context, p *pnr.PNR, peer *Peer) (map[string]string, error) {
	return nil, nil
}
func (f *fakeReleaser) Release(ctx context.Context, s pnr.Segment, was string) {
	f.got = append(f.got, s.Key()+"<-"+was)
}

// A message that turns a holding into XX tells the inventory what it held;
// a segment that was already XX, or one still live, is not released.
func TestReleaseCancelledTellsTheInventoryWhatWasHeld(t *testing.T) {
	fr := &fakeReleaser{}
	g := &Gateway{Responder: fr}
	rec := &pnr.PNR{Segments: []pnr.Segment{
		{Ref: 1, Type: pnr.SegmentAir, Carrier: "WN", FlightNum: "2554", WireDate: "26NOV", Board: "BNA", Off: "DCA", Class: "Y", Status: "XX", Seats: 2},
		{Ref: 2, Type: pnr.SegmentAir, Carrier: "WN", FlightNum: "2555", WireDate: "26NOV", Board: "DCA", Off: "BNA", Class: "Y", Status: "XX", Seats: 2},
		{Ref: 3, Type: pnr.SegmentAir, Carrier: "WN", FlightNum: "2556", WireDate: "27NOV", Board: "BNA", Off: "MDW", Class: "Y", Status: "HK", Seats: 2},
	}}
	before := map[string]string{rec.Segments[0].Key(): "HK", rec.Segments[1].Key(): "XX", rec.Segments[2].Key(): "HK"}
	g.releaseCancelled(context.Background(), rec, before)
	if len(fr.got) != 1 || fr.got[0] != rec.Segments[0].Key()+"<-HK" {
		t.Errorf("released %v; want only the segment that went from HK to XX", fr.got)
	}
	plain := &Gateway{Responder: &Inventory{}}
	plain.releaseCancelled(context.Background(), rec, before) // a Responder that is no Releaser is simply not told
}
