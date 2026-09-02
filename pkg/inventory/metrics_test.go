package inventory

import (
	"context"
	"strings"
	"testing"

	"github.com/adamf/jetway/pkg/metrics"
	"github.com/adamf/jetway/pkg/pnr"
)

func TestPublishedGaugesFollowTheSeats(t *testing.T) {
	inv := New("ZZ", func(carrier, flight, date, board string) (map[string]int, bool) {
		return map[string]int{"Y": 2}, true
	})
	reg := metrics.NewRegistry()
	inv.Publish(reg)
	if out := reg.String(); !strings.Contains(out, `jetway_inventory_sold_seats{carrier="ZZ"} 0`) {
		t.Fatalf("an empty inventory publishes zero:\n%s", out)
	}
	p := &pnr.PNR{Segments: []pnr.Segment{
		{Type: pnr.SegmentAir, Carrier: "ZZ", FlightNum: "1", WireDate: "26NOV", Board: "AAA", Off: "BBB", Class: "Y", Seats: 2, Status: "NN"},
		{Type: pnr.SegmentAir, Carrier: "ZZ", FlightNum: "1", WireDate: "26NOV", Board: "AAA", Off: "BBB", Class: "B", Seats: 1, Status: "NN"},
	}}
	if _, err := inv.Decide(context.Background(), p, nil); err != nil {
		t.Fatal(err)
	}
	out := reg.String()
	for _, want := range []string{
		`jetway_inventory_sold_seats{carrier="ZZ"} 2`,
		`jetway_inventory_waitlisted_seats{carrier="ZZ"} 1`,
		`jetway_inventory_full_cabins{carrier="ZZ"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in:\n%s", want, out)
		}
	}
	if d := metrics.Default.String(); !strings.Contains(d, `jetway_inventory_decisions_total{carrier="ZZ",status="KK"} 1`) ||
		!strings.Contains(d, `jetway_inventory_decisions_total{carrier="ZZ",status="US"} 1`) {
		t.Errorf("decisions are counted by status:\n%s", d)
	}
}
