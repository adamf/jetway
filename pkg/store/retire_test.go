package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/adamf/jetway/pkg/pnr"
)

// A populated book is converted in place: rows written at the old schema
// come through partitioned, with purge_at read off their last flight, and
// retiring a day drops its partition and everything that hung off it.
func TestRecordsArePartitionedByRetirementDay(t *testing.T) {
	dsn := os.Getenv("JETWAY_TEST_DSN")
	if dsn == "" {
		t.Skip("JETWAY_TEST_DSN not set")
	}
	ctx := context.Background()
	pg := freshDatabase(t, dsn, "jetway_retire_test")
	if err := migrateThrough(ctx, pg, 7); err != nil {
		t.Fatalf("migrate to 7: %v", err)
	}
	// Three records the old way: two flying 26NOV, one 27NOV, one with no
	// flight at all; events and a queue item hanging off the first.
	old := func(id, loc, depart string) {
		state := `{"id":"` + id + `","record_locator":"` + loc + `","status":"open","segments":[{"ref":1,"type":"air","carrier":"WN","flight_num":"2554","depart":"` + depart + `","wire_date":"26NOV","board":"BNA","off":"DCA","class":"Y","status":"HK","seats":1}]}`
		if depart == "" {
			state = `{"id":"` + id + `","record_locator":"` + loc + `","status":"open"}`
		}
		if _, err := pg.pool.Exec(ctx, `INSERT INTO pnr (id, record_locator, version, status, created_at, updated_at, state, node)
			VALUES ($1,$2,1,'open',now(),now(),$3::jsonb,'WN')`, id, loc, state); err != nil {
			t.Fatal(err)
		}
	}
	old("A1", "AAAAA1", "2025-11-26T00:00:00Z")
	old("A2", "AAAAA2", "2025-11-26T00:00:00Z")
	old("B1", "BBBBB1", "2025-11-27T00:00:00Z")
	old("N1", "NNNNN1", "")
	if _, err := pg.pool.Exec(ctx, `INSERT INTO pnr_event (id, pnr_id, seq, type, at, node) VALUES ('E1','A1',1,'created',now(),'WN')`); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.pool.Exec(ctx, `INSERT INTO queue_item (id, queue, pnr_id, code, reason, placed_at, placed_by, node)
		VALUES ('Q1','schedule_change','A1','schedule_cnl','test',now(),'test','WN')`); err != nil {
		t.Fatal(err)
	}

	if err := MigrateSchema(ctx, pg); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var relkind string
	if err := pg.pool.QueryRow(ctx, `SELECT relkind FROM pg_class WHERE relname = 'pnr'`).Scan(&relkind); err != nil || relkind != "p" {
		t.Fatalf("pnr relkind %q %v; want partitioned", relkind, err)
	}
	var n int
	if err := pg.pool.QueryRow(ctx, `SELECT count(*) FROM pnr`).Scan(&n); err != nil || n != 4 {
		t.Fatalf("rows after conversion: %d %v", n, err)
	}
	var pa time.Time
	if err := pg.pool.QueryRow(ctx, `SELECT purge_at FROM pnr WHERE id='A1'`).Scan(&pa); err != nil || !pa.Equal(time.Date(2025, 11, 26, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("purge_at backfilled from the last flight: %v %v", pa, err)
	}
	wn := pg.Node("WN")
	if rec, err := wn.GetPNR(ctx, "AAAAA1"); err != nil || rec.ID != "A1" {
		t.Fatalf("GetPNR after conversion: %v %v", rec, err)
	}

	// New writes land in daily partitions; a record's day is its flight plus
	// the grace, three days by default.
	day := time.Date(2025, 11, 26, 0, 0, 0, 0, time.UTC)
	rec := &pnr.PNR{RecordLocator: "CCCCC1", Status: pnr.StatusOpen, CreatedAt: day.AddDate(0, -1, 0),
		Passengers: []pnr.Passenger{{Ref: 1, Surname: "NEW", Given: "ONE", Title: "MR"}},
		Segments:   []pnr.Segment{{Ref: 1, Type: pnr.SegmentAir, Carrier: "WN", FlightNum: "0100", Depart: day, WireDate: "26NOV", Board: "BNA", Off: "MDW", Class: "Y", Status: "HK", Seats: 1}}}
	if err := wn.CreatePNR(ctx, rec, []Event{{Type: "created"}}); err != nil {
		t.Fatal(err)
	}
	var part string
	if err := pg.pool.QueryRow(ctx, `SELECT tableoid::regclass::text FROM pnr WHERE id=$1`, rec.ID).Scan(&part); err != nil || part != "pnr_p_20251129" {
		t.Errorf("a record flying 26NOV lives in the 29NOV partition: %q %v", part, err)
	}
	// Moving the flight moves the row.
	rec.Segments[0].Depart = day.AddDate(0, 0, 5)
	if err := wn.UpdatePNR(ctx, rec, rec.Version, []Event{{Type: "moved"}}); err != nil {
		t.Fatal(err)
	}
	if err := pg.pool.QueryRow(ctx, `SELECT tableoid::regclass::text FROM pnr WHERE id=$1`, rec.ID).Scan(&part); err != nil || part != "pnr_p_20251204" {
		t.Errorf("after the flight moved the row should too: %q %v", part, err)
	}
	if evs, err := wn.Events(ctx, rec.ID); err != nil || len(evs) != 2 {
		t.Errorf("events across partitions: %d %v", len(evs), err)
	}

	// Retire everything before 27NOV: the converted 26NOV rows (default
	// partition, deleted) go, their events and the queue item go, the 27NOV
	// record and the new one stay.
	got, err := pg.RetireBefore(ctx, time.Date(2025, 11, 27, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if got.Records != 2 || got.QueueItems != 1 {
		t.Errorf("retired %+v; want 2 default-partition records and 1 queue item", got)
	}
	if _, err := wn.GetPNR(ctx, "AAAAA1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a retired record should be gone: %v", err)
	}
	if _, err := wn.GetPNR(ctx, "BBBBB1"); err != nil {
		t.Errorf("a record flying later should stay: %v", err)
	}
	if _, err := wn.GetPNR(ctx, "CCCCC1"); err != nil {
		t.Errorf("the new record should stay: %v", err)
	}
	// Then the day the new record retires on: its partition is dropped.
	got, err = pg.RetireBefore(ctx, time.Date(2025, 12, 5, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if got.Partitions < 2 {
		t.Errorf("dropping a day should drop its record and event partitions: %+v", got)
	}
	if _, err := wn.GetPNR(ctx, "CCCCC1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the record whose day was dropped should be gone: %v", err)
	}
	if evs, _ := wn.Events(ctx, rec.ID); len(evs) != 0 {
		t.Errorf("its events should be gone with it: %d", len(evs))
	}
}
