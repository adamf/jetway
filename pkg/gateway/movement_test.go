package gateway

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

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

// A movement in transit is still a movement. The switch relays an MVT
// addressed to somebody else -- and its bus must carry the movement event,
// because the network's switch is where an operations display watches the
// whole sky from. Before this, only the addressee's bus flew aircraft, and a
// display on the switch saw message counts but an empty sky.
func TestRelayPublishesTransitMovements(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := store.NewMem()
	gw := New(Identity{Designator: "1X", TTYAddress: "XCHRM1X", Name: "switch"},
		st, NewBus(64), log, []byte("relay-mvt"))
	gw.Relay = true
	gw.AddPeer(&Peer{Name: "BA", Carrier: "BA", Format: store.FormatTypeB, TTYAddress: "LHRRMBA"})
	gw.AddPeer(&Peer{Name: "1G", Carrier: "1G", Format: store.FormatTypeB, TTYAddress: "LONDD1G"})
	gw.Sender = SenderFunc(func(ctx context.Context, peer string, raw []byte) error { return nil })

	sub, unsub := gw.Bus.Subscribe()
	defer unsub()

	raw := []byte("QU LONDD1G\n.LHRRMBA 010900\nMVT\nBA117/01.GABCD.LHR\nAD0900/0912 EA1740 JFK\nPX140\n")
	if _, err := gw.Ingest(ctx, "BA", raw); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-sub:
			if ev.Type != EvMovement {
				continue
			}
			p, ok := ev.Data.(map[string]any)
			if !ok {
				t.Fatalf("movement payload is %T", ev.Data)
			}
			if p["flight"] != "BA117" || p["station"] != "LHR" {
				t.Errorf("movement event = %v", p)
			}
			if p["ea_airport"] != "JFK" {
				t.Errorf("the estimate did not ride along: %v", p)
			}
			return
		case <-deadline:
			t.Fatal("the switch relayed the MVT without putting the movement on its bus")
		}
	}
}
