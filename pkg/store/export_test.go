package store

import (
	"context"
	"testing"

	"github.com/adamf/jetway/pkg/pnr"
)

// Every backend exports what it holds, in id order, and stops when the
// caller says so.
func TestExportWalksTheBookInOrder(t *testing.T) {
	eachBackend(t, func(t *testing.T, s Store) {
		ex, ok := s.(Exporter)
		if !ok {
			t.Skip("store does not export")
		}
		ctx := context.Background()
		for _, loc := range []string{"EXP001", "EXP002", "EXP003"} {
			if err := s.CreatePNR(ctx, samplePNR(loc), []Event{{Type: "created"}}); err != nil {
				t.Fatal(err)
			}
		}
		var ids []string
		if err := ex.ExportPNRs(ctx, func(p *pnr.PNR) error { ids = append(ids, p.ID); return nil }); err != nil {
			t.Fatal(err)
		}
		if len(ids) < 3 {
			t.Fatalf("at least three records, got %d", len(ids))
		}
		for i := 1; i < len(ids); i++ {
			if ids[i] < ids[i-1] {
				t.Fatalf("export is in id order: %v", ids)
			}
		}
		stop := 0
		err := ex.ExportPNRs(ctx, func(*pnr.PNR) error { stop++; return context.Canceled })
		if err == nil || stop != 1 {
			t.Fatalf("the callback's error stops the export after one record: %d %v", stop, err)
		}
	})
}

// noExport is a records store that cannot stream its records.
type noExport struct{ Store }

// A split store exports the records of its records store, and none of
// its message store's. With a records store that cannot stream, it says
// so and gives no records.
func TestASplitStoreExportsItsRecords(t *testing.T) {
	ctx := context.Background()
	msgs, recs := NewMem(), NewMem()
	var s Store = Split{Messages: msgs, Records: recs}
	for _, loc := range []string{"SPL001", "SPL002"} {
		if err := s.CreatePNR(ctx, samplePNR(loc), []Event{{Type: "created"}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := msgs.CreatePNR(ctx, samplePNR("MSG001"), []Event{{Type: "created"}}); err != nil {
		t.Fatal(err)
	}
	ex, ok := s.(Exporter)
	if !ok {
		t.Fatal("a split store does not export")
	}
	var got []string
	if err := ex.ExportPNRs(ctx, func(p *pnr.PNR) error { got = append(got, p.RecordLocator); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "SPL001" || got[1] != "SPL002" {
		t.Errorf("the records store holds SPL001 and SPL002; the export gave %v", got)
	}
	n := 0
	err := Split{Messages: msgs, Records: noExport{recs}}.ExportPNRs(ctx, func(*pnr.PNR) error { n++; return nil })
	if err == nil || n != 0 {
		t.Errorf("a records store that cannot stream: %d records and error %v", n, err)
	}
}
