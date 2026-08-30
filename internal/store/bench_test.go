package store

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/adamf/jetway/internal/ulid"
	"github.com/adamf/jetway/pkg/pnr"
)

// benchStore returns the Postgres backend, or skips. These numbers decide how
// many gateway processes a given database can feed, so they have to come from
// a real database rather than the in-memory stand-in.
func benchStore(b *testing.B) *Postgres {
	b.Helper()
	dsn := os.Getenv("JETWAY_TEST_DSN")
	if dsn == "" {
		b.Skip("JETWAY_TEST_DSN not set")
	}
	ctx := context.Background()
	pg, err := OpenPostgres(ctx, dsn)
	if err != nil {
		b.Fatalf("connect: %v", err)
	}
	b.Cleanup(func() { _ = pg.Close() })
	if err := MigrateSchema(ctx, pg); err != nil {
		b.Fatalf("migrate: %v", err)
	}
	return pg
}

// benchRun makes locators unique per process, and benchSeq per row.
//
// The counter has to be monotonic across the whole run rather than the loop
// index: the testing package calls a benchmark repeatedly with a growing N, so
// an index restarting at zero recreates the previous round's rows and every
// locator collides.
var (
	benchRun = time.Now().UnixNano()
	benchSeq atomic.Int64
)

func benchRecord(i int) *pnr.PNR {
	now := time.Now().UTC()
	return &pnr.PNR{
		RecordLocator: fmt.Sprintf("B%05X", i), Status: pnr.StatusOpen,
		CreatedAt: now, UpdatedAt: now,
		Passengers: []pnr.Passenger{{Ref: 1, Surname: "PAX", Given: "T"}},
		Segments: []pnr.Segment{{Ref: 1, Type: pnr.SegmentAir, Carrier: "BA",
			FlightNum: "0117", Status: "HK", Seats: 1, WireDate: "16DEC", Depart: now}},
	}
}

func BenchmarkPostgresAppendMessage(b *testing.B) {
	s := benchStore(b)
	ctx := context.Background()
	raw := []byte("QU LONRM1J\n.LHRRMBA 121430\nSS\nBA0117Y16DECLHRJFKNN1\n1SMITH/JOHNMR\n")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m := &Message{
			ID: ulid.New(), Direction: Inbound, At: time.Now().UTC(),
			Transport: "link", Peer: "BA", Format: FormatTypeB,
			Raw: raw, SHA256: "x", Size: len(raw), Status: StatusReceived,
		}
		if err := s.AppendMessage(ctx, m); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPostgresCreatePNR(b *testing.B) {
	s := benchStore(b)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := benchRecord(i)
		rec.RecordLocator = fmt.Sprintf("%d-%d", benchRun, benchSeq.Add(1))
		if err := s.CreatePNR(ctx, rec, []Event{{Type: "created", At: time.Now().UTC()}}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPostgresGetPNR(b *testing.B) {
	s := benchStore(b)
	ctx := context.Background()
	rec := benchRecord(999999)
	rec.RecordLocator = fmt.Sprintf("G%d", benchRun)
	_ = s.CreatePNR(ctx, rec, nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.GetPNR(ctx, fmt.Sprintf("G%d", benchRun)); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPostgresUpdatePNR is the write that carries optimistic concurrency,
// which is the one every applied message performs.
func BenchmarkPostgresUpdatePNR(b *testing.B) {
	s := benchStore(b)
	ctx := context.Background()
	// A fresh record per round: the testing package reruns with a growing N,
	// and reusing the locator would collide with the previous round's row.
	locator := fmt.Sprintf("U%d-%d", benchRun, benchSeq.Add(1))
	rec := benchRecord(888888)
	rec.RecordLocator = locator
	if err := s.CreatePNR(ctx, rec, nil); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cur, err := s.GetPNR(ctx, locator)
		if err != nil {
			b.Fatal(err)
		}
		if err := s.UpdatePNR(ctx, cur, cur.Version, []Event{{Type: "touch", At: time.Now().UTC()}}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPostgresAppendParallel shows whether capture scales with
// concurrency or serialises. Capture is on every message's critical path, so
// its parallel behaviour sets the ceiling for a single gateway process.
func BenchmarkPostgresAppendParallel(b *testing.B) {
	s := benchStore(b)
	raw := []byte("QU LONRM1J\n.LHRRMBA 121430\nSS\nBA0117Y16DECLHRJFKNN1\n1SMITH/JOHNMR\n")
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		ctx := context.Background()
		for pb.Next() {
			m := &Message{
				ID: ulid.New(), Direction: Inbound, At: time.Now().UTC(),
				Transport: "link", Peer: "BA", Format: FormatTypeB,
				Raw: raw, SHA256: "x", Size: len(raw), Status: StatusReceived,
			}
			if err := s.AppendMessage(ctx, m); err != nil {
				b.Fatal(err)
			}
		}
	})
}
