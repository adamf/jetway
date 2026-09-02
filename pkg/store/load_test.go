package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/adamf/jetway/pkg/pnr"
)

func loadFixture(n int, prefix string) []*pnr.PNR {
	day := time.Date(2025, 11, 26, 0, 0, 0, 0, time.UTC)
	out := make([]*pnr.PNR, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, &pnr.PNR{
			RecordLocator: fmt.Sprintf("%s%04d", prefix, i),
			Status:        pnr.StatusTicketed,
			CreatedAt:     day.AddDate(0, 0, -30),
			Passengers:    []pnr.Passenger{{Ref: 1, Surname: "LOADED", Given: "TEST", Title: "MR"}},
			Segments: []pnr.Segment{{Ref: 1, Type: pnr.SegmentAir, Carrier: "WN", FlightNum: "2554",
				Depart: day, WireDate: "26NOV", Board: "BNA", Off: "DCA", Class: "Y", Status: "HK", Seats: 1}},
			Locators: []pnr.ExternalLocator{{Owner: "1G", Value: "GDS" + fmt.Sprintf("%03d", i)}},
		})
	}
	return out
}

// A batch lands whole: every record readable by locator, by flight and by
// the channel's locator, each at version 1 with one loaded event.
func testLoadPNRs(t *testing.T, s Store) {
	ctx := context.Background()
	recs := loadFixture(25, "LD")
	if err := s.LoadPNRs(ctx, recs, "fill"); err != nil {
		t.Fatalf("LoadPNRs: %v", err)
	}
	got, err := s.GetPNR(ctx, "LD0007")
	if err != nil {
		t.Fatalf("GetPNR after load: %v", err)
	}
	if got.Version != 1 || got.Passengers[0].Surname != "LOADED" || got.Status != pnr.StatusTicketed {
		t.Errorf("loaded record came back wrong: %+v", got)
	}
	byFlight, err := s.FindPNRsByFlight(ctx, "WN2554", "26NOV", 100)
	if err != nil || len(byFlight) != 25 {
		t.Errorf("FindPNRsByFlight after load: %d records, %v", len(byFlight), err)
	}
	if ext, err := s.FindPNRByExternalLocator(ctx, "1G", "GDS003"); err != nil || ext.RecordLocator != "LD0003" {
		t.Errorf("FindPNRByExternalLocator after load: %v %v", ext, err)
	}
	events, err := s.Events(ctx, got.ID)
	if err != nil || len(events) != 1 || events[0].Type != "loaded" || events[0].Actor != "fill" {
		t.Errorf("a loaded record should carry one loaded event by the actor: %+v %v", events, err)
	}
	// The sold-seat count reads the same records back as an inventory would.
	sold, err := s.SoldSeats(ctx, "WN", "26NOV")
	if err != nil || len(sold) != 1 || sold[0].FlightNum != "2554" || sold[0].Board != "BNA" || sold[0].Class != "Y" || sold[0].Status != "HK" || sold[0].Seats != 25 {
		t.Errorf("SoldSeats after load: %+v %v", sold, err)
	}
	if none, _ := s.SoldSeats(ctx, "WN", "27NOV"); len(none) != 0 {
		t.Errorf("SoldSeats on a day with no flights: %+v", none)
	}
	// A locator already held fails the whole batch and stores nothing.
	again := loadFixture(3, "LE")
	again[1].RecordLocator = "LD0007"
	if err := s.LoadPNRs(ctx, again, "fill"); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("a duplicate in the batch should be ErrDuplicate, got %v", err)
	}
	if _, err := s.GetPNR(ctx, "LE0000"); err == nil {
		t.Errorf("a failed batch must store nothing, but LE0000 exists")
	}
}

func TestMemLoadPNRs(t *testing.T) { testLoadPNRs(t, NewMem()) }

func TestPostgresLoadPNRs(t *testing.T) {
	dsn := os.Getenv("JETWAY_TEST_DSN")
	if dsn == "" {
		t.Skip("JETWAY_TEST_DSN not set")
	}
	ctx := context.Background()
	pg, err := OpenPostgres(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	if _, err := pg.pool.Exec(ctx, `TRUNCATE pnr CASCADE`); err != nil {
		t.Fatal(err)
	}
	testLoadPNRs(t, pg.Node("LOADT"))
}
