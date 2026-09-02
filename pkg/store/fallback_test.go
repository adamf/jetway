package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/adamf/jetway/pkg/pnr"
)

// A daily partition that cannot be created must not stop the write. The
// recorded day found this: the default partition held 1.4 million rows of
// the day another system's first booking tried to partition, the catalogue
// could never make the partition, and every booking died on it for hours.
// The write goes to the default partition instead, which RetireBefore
// clears as it does the daily ones.
func TestWriteSurvivesAPartitionThatCannotBeMade(t *testing.T) {
	dsn := os.Getenv("JETWAY_TEST_DSN")
	if dsn == "" {
		t.Skip("JETWAY_TEST_DSN not set")
	}
	ctx := context.Background()
	pg := freshDatabase(t, dsn, "jetway_fallback_test")
	if err := MigrateSchema(ctx, pg); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	day := time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)
	rec := &pnr.PNR{RecordLocator: "CCCCC1", Status: pnr.StatusOpen, CreatedAt: day.AddDate(0, -1, 0),
		Passengers: []pnr.Passenger{{Ref: 1, Surname: "NEW", Given: "ONE", Title: "MR"}},
		Segments:   []pnr.Segment{{Ref: 1, Type: pnr.SegmentAir, Carrier: "WN", FlightNum: "0100", Depart: day, WireDate: "26NOV", Board: "BNA", Off: "MDW", Class: "Y", Status: "HK", Seats: 1}}}
	// A row of the record's purge day already sits in the default
	// partition: no partition for the day can be made while it does.
	purgeDay := pg.purgeAt(rec)
	if _, err := pg.pool.Exec(ctx, `INSERT INTO pnr_default (id, record_locator, version, status, created_at, updated_at, state, node, purge_at)
		VALUES ('STRAY1', 'STRAY1', 1, 'open', now(), now(), '{}', 'WN', $1)`, purgeDay); err != nil {
		t.Fatalf("seed the default partition: %v", err)
	}
	wn := pg.Node("WN")
	wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := wn.CreatePNR(wctx, rec, []Event{{Type: "created"}}); err != nil {
		t.Fatalf("the write must survive the partition failure: %v", err)
	}
	var part string
	if err := pg.pool.QueryRow(ctx, `SELECT tableoid::regclass::text FROM pnr WHERE id=$1`, rec.ID).Scan(&part); err != nil || part != "pnr_default" {
		t.Fatalf("the row lands in the default partition: %q %v", part, err)
	}
	if got, err := wn.GetPNR(ctx, "CCCCC1"); err != nil || got.ID != rec.ID {
		t.Fatalf("and reads back: %v %v", got, err)
	}
	// The failure is remembered: the next write of the day does not wait
	// on the catalogue again.
	start := time.Now()
	rec2 := *rec
	rec2.ID, rec2.RecordLocator, rec2.Version = "", "CCCCC2", 0
	if err := wn.CreatePNR(ctx, &rec2, []Event{{Type: "created"}}); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("a remembered failure should not be retried at once: %v", time.Since(start))
	}
	pg.parts.mu.Lock()
	_, failed := pg.parts.failed[purgeDay.Format("20060102")]
	pg.parts.mu.Unlock()
	if !failed {
		t.Fatal("the failed day should be remembered")
	}
}
