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
