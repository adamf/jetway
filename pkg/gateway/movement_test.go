package gateway

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/adamf/jetway/pkg/store"
)

// An MVT arriving on a link must be recognised before the reservation grammar
// gets it, applied without touching any record, and published for whatever is
// watching the sky.
func TestMovementMessageIngest(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := store.NewMem()
	gw := New(Identity{Designator: "1J", TTYAddress: "LONRM1J", Name: "jetway"},
		st, NewBus(100), log, []byte("secret"))
	gw.AddPeer(&Peer{Name: "BA", Carrier: "BA", Format: store.FormatTypeB, TTYAddress: "LHRRMBA"})
	gw.Sender = SenderFunc(func(ctx context.Context, peer string, raw []byte) error { return nil })

	sub, cancel := gw.Bus.Subscribe()
	defer cancel()

	raw := []byte("QU LONRM1J\r\n.LHRRMBA 121430\r\n" +
		"MVT\r\nBA175/12.GXWBA.LHR\r\nAD1100/1115 EA1500 JFK\r\nPX214\r\n")
	res, err := gw.Ingest(context.Background(), "BA", raw)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if res.Status != store.StatusApplied {
		t.Fatalf("an MVT should apply cleanly; status = %s", res.Status)
	}

	m, err := st.GetMessage(context.Background(), res.MessageID)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if m.Kind != "MVT/BA175" {
		t.Errorf("Kind = %q, want MVT/BA175", m.Kind)
	}

	// No record may be created: an MVT is about an aircraft, not a booking.
	recs, err := st.ListPNRs(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListPNRs: %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("a movement message created %d records", len(recs))
	}

	// And it must reach the bus, where the globe watches from.
	for {
		select {
		case ev := <-sub:
			if ev.Type == EvMovement {
				return
			}
		default:
			t.Fatal("no movement event was published")
		}
	}
}
