package gateway

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/adamf/jetway/pkg/store"
	"github.com/adamf/jetway/pkg/edifact"
	"github.com/adamf/jetway/pkg/pnr"
	"github.com/adamf/jetway/pkg/typeb"
)

func benchNode(b *testing.B) *Gateway {
	b.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := store.NewMem()
	gw := New(Identity{Designator: "1J", TTYAddress: "LONRM1J", Name: "jetway"},
		st, NewBus(1), log, []byte("secret"))
	gw.AddPeer(&Peer{Name: "BA", Carrier: "BA", Format: store.FormatTypeB, TTYAddress: "LHRRMBA"})
	gw.AddPeer(&Peer{Name: "AA", Carrier: "AA", Format: store.FormatEDIFACT})
	gw.Sender = SenderFunc(func(ctx context.Context, peer string, raw []byte) error { return nil })
	return gw
}

// seed fills the store with records so a scan has something to walk.
func seed(b *testing.B, gw *Gateway, n int) {
	b.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	for i := 0; i < n; i++ {
		rec := &pnr.PNR{
			RecordLocator: fmt.Sprintf("R%05d", i), Status: pnr.StatusOpen,
			CreatedAt: now, UpdatedAt: now,
			Passengers: []pnr.Passenger{{Ref: 1, Surname: "PAX", Given: "T"}},
			Segments: []pnr.Segment{{Ref: 1, Type: pnr.SegmentAir, Carrier: "BA",
				FlightNum: "0117", Status: "HK", Seats: 1, WireDate: "16DEC"}},
		}
		if err := gw.Store.CreatePNR(ctx, rec, nil); err != nil {
			b.Fatal(err)
		}
	}
}

func typeBSell(n int) []byte {
	return []byte(fmt.Sprintf(
		"QU LONRM1J\n.LHRRMBA %06d\nSS\nBA0117Y16DECLHRJFKNN1\n1SMITH/JOHN%dMR\nRL BA/AB%04d\n",
		n%999999, n, n%9999))
}

// BenchmarkIngestTypeB measures the whole inbound path on a teletype message:
// capture, classify, decode, dedupe, apply, respond.
func BenchmarkIngestTypeB(b *testing.B) {
	gw := benchNode(b)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := gw.Ingest(ctx, "BA", typeBSell(i)); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkParseTypeB isolates the codec from the store and the pipeline.
func BenchmarkParseTypeB(b *testing.B) {
	raw := typeBSell(1)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := typeb.Parse(raw); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseEDIFACT(b *testing.B) {
	raw := []byte("UNB+UNOA:3+AA:ZZ+1J:ZZ+260829:1200+IC0001'" +
		"UNH+1+PAORES:96:1:IA'MSG+:22'ORG+AA'RCI+AA:ABC123'" +
		"TIF+SMITH+JOHN:A:1'TVL+161226::161226+LHR+JFK+BA+0117:Y'RPI+1+HK'" +
		"UNT+9+1'UNZ+1+IC0001'")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := edifact.Parse(raw, edifact.ParseOptions{}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkFindTicketScan shows what the record scan costs as the store grows.
// It is the shape of several lookups on the hot path.
func BenchmarkFindTicketScan(b *testing.B) {
	for _, n := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("records=%d", n), func(b *testing.B) {
			gw := benchNode(b)
			ctx := context.Background()
			seed(b, gw, n)
			gw.ScheduleScanLimit = n
			number, _ := gw.nextTicketNumber(ctx, "125")
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, _, err := gw.findTicket(ctx, number); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
